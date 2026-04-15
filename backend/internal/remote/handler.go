package remote

import (
	"fmt"
	"net/http"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // CORS 由 Gin middleware 处理
}

// Handler 处理 remote session 相关的 HTTP/WebSocket 请求。
type Handler struct {
	manager      *Manager
	cfg          *config.Config
	workDir      string
	memoryStore  *memory.Store
	sessionStore *session.Store
	provider     provider.Provider
}

// NewHandler 创建 remote Handler。
func NewHandler(mgr *Manager, p provider.Provider, cfg *config.Config, workDir string, memStore *memory.Store, sessStore *session.Store) *Handler {
	return &Handler{
		manager:      mgr,
		cfg:          cfg,
		workDir:      workDir,
		memoryStore:  memStore,
		sessionStore: sessStore,
		provider:     p,
	}
}

// Connect handles GET /api/remote/:session_id/ws
// 客户端通过 WebSocket 连接到指定 session。
// Query params:
//   - viewer=true  只读模式，不能发 prompt
func (h *Handler) Connect(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id required"})
		return
	}

	viewerOnly := c.Query("viewer") == "true"

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	sess := h.manager.GetOrCreate(sessionID)
	clientID := fmt.Sprintf("client_%d", time.Now().UnixNano())
	client := newClient(clientID, sessionID, conn, viewerOnly)

	sess.addClient(client)

	// 发送欢迎消息
	client.send(EventEnvelope{
		Type:      "connected",
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"client_id":   clientID,
			"viewer_only": viewerOnly,
			"status":      formatStatus(sess),
		},
	})

	// 如果 session 还没有 agent 在跑，且不是 viewer，启动 agent loop
	if !viewerOnly {
		sess.mu.Lock()
		alreadyRunning := sess.running
		sess.mu.Unlock()

		if !alreadyRunning {
			agentCfg := h.buildAgentConfig(sessionID)
			go RunAgent(sess.ctx, sess, agentCfg, h.sessionStore)
		}
	}

	// 启动 write pump
	go client.writePump()

	// read pump 阻塞直到连接关闭
	client.readPump(sess, func() {
		sess.removeClient(clientID)
		// 广播客户端离开
		sess.broadcast(EventEnvelope{
			Type:      "client_left",
			SessionID: sessionID,
			Payload:   map[string]string{"client_id": clientID},
		})
	})
}

// Status handles GET /api/remote/:session_id/status
// 返回 session 状态（不需要 WebSocket）。
func (h *Handler) Status(c *gin.Context) {
	sessionID := c.Param("session_id")
	sess := h.manager.Get(sessionID)
	if sess == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	c.JSON(http.StatusOK, formatStatus(sess))
}

// ListSessions handles GET /api/remote
// 返回所有活跃的 remote session 列表。
func (h *Handler) ListSessions(c *gin.Context) {
	ids := h.manager.List()
	sessions := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		if sess := h.manager.Get(id); sess != nil {
			sessions = append(sessions, formatStatus(sess))
		}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// buildAgentConfig 构建 agent.Config，权限模式为 default（等待客户端响应）。
func (h *Handler) buildAgentConfig(sessionID string) agent.Config {
	p := h.provider
	reg := tool.DefaultRegistry(h.workDir, tool.NewProviderWebFetch(p, h.cfg.Provider.ModelBackup))

	env := prompt.QuickEnvContext(h.workDir)
	env.Model = h.cfg.Provider.Model
	env.ProviderName = h.cfg.Provider.Default

	var memSection string
	if h.memoryStore != nil {
		memSection = memory.BuildMemoryPromptSectionPG(nil, h.memoryStore)
	}
	if memSection == "" {
		memSection = memory.BuildMemoryPromptSection(memory.GetMemoryDir(h.workDir))
	}

	return agent.Config{
		Provider:       p,
		Registry:       reg,
		SystemPrompt:   prompt.BuildSystemPromptFull(env, memSection, ""),
		Model:          h.cfg.Provider.Model,
		MaxTokens:      h.cfg.Agent.MaxTokens,
		MaxTurns:       h.cfg.Agent.MaxTurns,
		ModelBackup:    h.cfg.Provider.ModelBackup,
		MemoryStore:    h.memoryStore,
		SessionStore:   h.sessionStore,
		SessionID:      sessionID,
		PermissionMode: tool.PermissionDefault,
		AllowRules:     h.cfg.Tools.AllowRules,
		// PermissionCallback 由 RunAgent 注入
	}
}
