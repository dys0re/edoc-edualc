package remote

import (
	"context"
	"fmt"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// RunAgent 在 RemoteSession 上启动 agent loop。
// 从 promptCh 读取 prompt，执行 agent，将事件广播给所有订阅的客户端。
// 支持多轮对话：每次收到 prompt 就跑一轮，直到 ctx 取消。
func RunAgent(ctx context.Context, sess *RemoteSession, cfg agent.Config, sessStore *session.Store) {
	sess.mu.Lock()
	if sess.running {
		sess.mu.Unlock()
		sess.broadcast(EventEnvelope{
			Type:      "error",
			SessionID: sess.ID,
			Payload:   map[string]string{"error": "agent already running"},
		})
		return
	}
	sess.running = true
	sess.mu.Unlock()

	defer func() {
		sess.mu.Lock()
		sess.running = false
		sess.cancelFn = nil
		sess.mu.Unlock()
	}()

	// 加载历史消息
	var history []message.Message
	if sessStore != nil && sess.ID != "" {
		loaded, err := sessStore.LoadMessages(ctx, sess.ID)
		if err == nil {
			history = loaded
		}
	}

	// 注入权限回调：agent 请求权限时，广播给客户端并等待响应
	cfg.PermissionCallback = func(toolName, desc string) (bool, error) {
		req := sess.addPermRequest(toolName, desc)
		// 广播权限请求给所有客户端
		sess.broadcast(EventEnvelope{
			Type:      "permission_request",
			SessionID: sess.ID,
			Payload: map[string]interface{}{
				"request_id": req.RequestID,
				"tool_name":  req.ToolName,
				"description": req.Desc,
			},
		})
		// 等待客户端响应或 ctx 取消
		select {
		case allow := <-req.ReplyCh:
			return allow, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case prompt, ok := <-sess.promptCh:
			if !ok {
				return
			}
			runOneTurn(ctx, sess, cfg, &history, prompt)

			// 持久化更新后的历史（agent loop 内部已 append，这里同步本地 history）
			if sessStore != nil && sess.ID != "" {
				updated, err := sessStore.LoadMessages(ctx, sess.ID)
				if err == nil {
					history = updated
				}
			}
		}
	}
}

// runOneTurn 执行一轮 agent loop，将事件广播给客户端。
func runOneTurn(ctx context.Context, sess *RemoteSession, cfg agent.Config, history *[]message.Message, prompt string) {
	loopCtx, cancel := context.WithCancel(ctx)

	sess.mu.Lock()
	sess.cancelFn = cancel
	sess.mu.Unlock()

	defer cancel()

	sess.broadcast(EventEnvelope{
		Type:      "turn_start",
		SessionID: sess.ID,
		Payload:   map[string]string{"prompt": prompt},
	})

	var eventCh <-chan agent.Event
	if len(*history) > 0 {
		msgs := append(*history, message.NewUserMessage(prompt))
		eventCh = agent.RunWithMessages(loopCtx, cfg, msgs)
	} else {
		eventCh = agent.Run(loopCtx, cfg, prompt)
	}

	for evt := range eventCh {
		sess.broadcastAgentEvent(evt)
		if evt.Type == "message_complete" && evt.Message != nil {
			*history = append(*history, *evt.Message)
		}
	}

	sess.broadcast(EventEnvelope{
		Type:      "turn_end",
		SessionID: sess.ID,
	})
}

// buildPermissionMode 根据 viewerOnly 决定权限模式。
// viewer 连接时 agent 已在运行，不需要再设置。
func buildRemotePermissionMode(viewerOnly bool) tool.PermissionMode {
	if viewerOnly {
		return tool.PermissionBypass
	}
	return tool.PermissionDefault
}

// formatStatus 返回 session 状态摘要。
func formatStatus(sess *RemoteSession) map[string]interface{} {
	sess.mu.RLock()
	running := sess.running
	clients := len(sess.clients)
	sess.mu.RUnlock()
	return map[string]interface{}{
		"session_id": sess.ID,
		"running":    running,
		"clients":    clients,
		"created_at": fmt.Sprintf("%v", sess.CreatedAt),
	}
}
