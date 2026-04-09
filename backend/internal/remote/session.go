// Package remote implements remote session management for edoc-edualc.
// 允许远程客户端通过 WebSocket 连接到服务端的 agent loop，
// 实时接收事件流、发送 prompt、响应权限请求。
//
// 架构:
//   - RemoteSession: 单个 session 的状态 + 事件广播
//   - Manager: 全局 session 注册表
//   - Client: 单个 WebSocket 连接（可多个 client 订阅同一 session）
package remote

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
)

// EventEnvelope 是发给 WebSocket 客户端的消息格式。
type EventEnvelope struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	Payload   interface{} `json:"payload,omitempty"`
}

// ClientMessage 是客户端发给服务端的消息格式。
type ClientMessage struct {
	Type string `json:"type"`
	// type=prompt
	Prompt string `json:"prompt,omitempty"`
	// type=permission_response
	RequestID string `json:"request_id,omitempty"`
	Allow     bool   `json:"allow,omitempty"`
	// type=interrupt
}

// PermissionRequest 是 agent 发出的权限请求，等待客户端响应。
type PermissionRequest struct {
	RequestID string
	ToolName  string
	Desc      string
	ReplyCh   chan bool // true=allow, false=deny
}

// RemoteSession 管理单个 session 的生命周期和事件广播。
type RemoteSession struct {
	ID string

	mu      sync.RWMutex
	clients map[string]*Client // clientID → Client

	// promptCh 接收来自客户端的 prompt，传给 agent loop
	promptCh chan string

	// cancelFn 取消当前 agent loop 的单轮执行
	cancelFn context.CancelFunc

	// ctx/ctxCancel 是 session 级别的 context，session 关闭时取消
	ctx       context.Context
	ctxCancel context.CancelFunc

	// permRequests 待响应的权限请求
	permMu       sync.Mutex
	permRequests map[string]*PermissionRequest

	// running 表示 agent loop 是否正在运行
	running bool

	CreatedAt time.Time
}

func newRemoteSession(id string) *RemoteSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &RemoteSession{
		ID:           id,
		clients:      make(map[string]*Client),
		promptCh:     make(chan string, 8),
		permRequests: make(map[string]*PermissionRequest),
		CreatedAt:    time.Now(),
		ctx:          ctx,
		ctxCancel:    cancel,
	}
}

// addClient 注册一个新的 WebSocket 客户端。
func (s *RemoteSession) addClient(c *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[c.ID] = c
}

// removeClient 移除一个 WebSocket 客户端。
func (s *RemoteSession) removeClient(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, clientID)
}

// broadcast 向所有订阅该 session 的客户端广播事件。
func (s *RemoteSession) broadcast(env EventEnvelope) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		c.send(env)
	}
}

// broadcastAgentEvent 将 agent.Event 转换为 EventEnvelope 并广播。
func (s *RemoteSession) broadcastAgentEvent(evt agent.Event) {
	payload := map[string]interface{}{"type": evt.Type}
	switch evt.Type {
	case "text_delta", "thinking_delta", "warning":
		payload["delta"] = evt.Delta
	case "tool_use":
		payload["tool_name"] = evt.ToolName
		payload["tool_input"] = evt.ToolInput
	case "tool_result":
		if evt.ToolResult != nil {
			payload["tool_name"] = evt.ToolName
			payload["content"] = evt.ToolResult.Content
			payload["is_error"] = evt.ToolResult.IsError
		}
	case "error":
		if evt.Error != nil {
			payload["error"] = evt.Error.Error()
		}
	case "permission_request":
		payload["tool_name"] = evt.PermissionToolName
		payload["description"] = evt.PermissionDesc
	}
	s.broadcast(EventEnvelope{
		Type:      "agent_event",
		SessionID: s.ID,
		Payload:   payload,
	})
}

// sendPrompt 将客户端发来的 prompt 推入 promptCh。
func (s *RemoteSession) sendPrompt(prompt string) error {
	select {
	case s.promptCh <- prompt:
		return nil
	default:
		return fmt.Errorf("session busy, try again")
	}
}

// interrupt 取消当前 agent loop 的单轮执行。
func (s *RemoteSession) interrupt() {
	s.mu.Lock()
	fn := s.cancelFn
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Close 关闭整个 session（取消 session-level context）。
func (s *RemoteSession) Close() {
	s.ctxCancel()
}

// addPermRequest 注册一个权限请求，返回 requestID。
func (s *RemoteSession) addPermRequest(toolName, desc string) *PermissionRequest {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	id := fmt.Sprintf("perm_%d", time.Now().UnixNano())
	req := &PermissionRequest{
		RequestID: id,
		ToolName:  toolName,
		Desc:      desc,
		ReplyCh:   make(chan bool, 1),
	}
	s.permRequests[id] = req
	return req
}

// replyPermRequest 响应一个权限请求。
func (s *RemoteSession) replyPermRequest(requestID string, allow bool) error {
	s.permMu.Lock()
	req, ok := s.permRequests[requestID]
	if ok {
		delete(s.permRequests, requestID)
	}
	s.permMu.Unlock()
	if !ok {
		return fmt.Errorf("permission request not found: %s", requestID)
	}
	req.ReplyCh <- allow
	return nil
}

// clientCount 返回当前连接的客户端数量。
func (s *RemoteSession) clientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// ── Manager ──────────────────────────────────────────────────────────────────

// Manager 管理所有 remote session 的生命周期。
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*RemoteSession
}

// NewManager 创建 Manager 实例。
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*RemoteSession)}
}

// GetOrCreate 获取或创建一个 RemoteSession。
func (m *Manager) GetOrCreate(sessionID string) *RemoteSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		return s
	}
	s := newRemoteSession(sessionID)
	m.sessions[sessionID] = s
	return s
}

// Get 获取一个已存在的 RemoteSession，不存在返回 nil。
func (m *Manager) Get(sessionID string) *RemoteSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

// Remove 移除一个 RemoteSession。
func (m *Manager) Remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

// List 返回所有 session ID。
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}
