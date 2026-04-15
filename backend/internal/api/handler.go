package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/command"
	"github.com/dysorder/edoc-edualc/backend/internal/compact"
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/session"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
	"github.com/dysorder/edoc-edualc/backend/internal/token"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ChatRequest struct {
	Prompt   string `json:"prompt" binding:"required"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"` // "anthropic" or "openai"
}

type Handler struct {
	defaultProvider provider.Provider
	cfg            *config.Config
	workDir        string
	memoryStore    *memory.Store
	sessionStore   *session.Store
	pool           *pgxpool.Pool
}

func NewHandler(p provider.Provider, cfg *config.Config, workDir string, memoryStore *memory.Store, sessionStore *session.Store, pool *pgxpool.Pool) *Handler {
	return &Handler{
		defaultProvider: p,
		cfg:            cfg,
		workDir:        workDir,
		memoryStore:    memoryStore,
		sessionStore:   sessionStore,
		pool:           pool,
	}
}

// ChatSSE handles POST /api/chat with Server-Sent Events streaming.
// Stateless: each request is an isolated conversation.
func (h *Handler) ChatSSE(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	p := h.defaultProvider
	model := req.Model

	reg := tool.DefaultRegistry(h.workDir, tool.NewProviderWebFetch(p, h.cfg.Provider.ModelBackup))
	envCtx := prompt.QuickEnvContext(h.workDir)
	envCtx.ProviderName = h.cfg.Provider.Default
	cfg := agent.Config{
		Provider:           p,
		Registry:           reg,
		SystemPrompt:       prompt.BuildSystemPromptFull(envCtx, "", ""),
		Model:              model,
		MaxTokens:          8192,
		WorkDir:            h.workDir,
		PermissionMode:     tool.ParsePermissionMode(h.cfg.Tools.PermissionMode),
		AllowRules:         h.cfg.Tools.AllowRules,
		// API mode: no interactive callback — non-bypass tools are denied
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	eventCh := agent.Run(ctx, cfg, req.Prompt)

	h.sseResponse(c, ctx, eventCh)
}

// --- Session endpoints ---

type CreateSessionRequest struct {
	Model string `json:"model,omitempty"`
}

// CreateSession handles POST /api/sessions.
func (h *Handler) CreateSession(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	var req CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Body is optional for create
		req = CreateSessionRequest{}
	}

	model := req.Model
	if model == "" {
		model = h.cfg.Provider.Model
	}

	projectKey := memory.SanitizeProjectKey(h.workDir)
	sess, err := h.sessionStore.Create(c.Request.Context(), "", projectKey, model)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, sess)
}

// ListSessions handles GET /api/sessions.
func (h *Handler) ListSessions(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	projectKey := memory.SanitizeProjectKey(h.workDir)
	sessions, err := h.sessionStore.List(c.Request.Context(), "", projectKey, 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// GetSession handles GET /api/sessions/:id.
func (h *Handler) GetSession(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	id := c.Param("id")
	sess, err := h.sessionStore.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	msgs, err := h.sessionStore.LoadMessages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session":  sess,
		"messages": msgs,
	})
}

// DeleteSession handles DELETE /api/sessions/:id.
func (h *Handler) DeleteSession(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	id := c.Param("id")
	if err := h.sessionStore.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

type UpdateSessionRequest struct {
	Title string `json:"title,omitempty"`
}

// UpdateSession handles PATCH /api/sessions/:id.
func (h *Handler) UpdateSession(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	var req UpdateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id := c.Param("id")
	if req.Title != "" {
		if err := h.sessionStore.UpdateTitle(c.Request.Context(), id, req.Title); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

// SessionChatSSE handles POST /api/sessions/:id/chat with Server-Sent Events.
// Stateful: loads history from PG, appends new messages, persists after completion.
func (h *Handler) SessionChatSSE(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	id := c.Param("id")

	// Verify session exists
	_, err := h.sessionStore.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Load existing messages
	msgs, err := h.sessionStore.LoadMessages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Append new user message to history and persist immediately
	userMsg := message.NewUserMessage(req.Prompt)
	msgs = append(msgs, userMsg)
	if err := h.sessionStore.AppendMessages(c.Request.Context(), id, []message.Message{userMsg}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist user message: " + err.Error()})
		return
	}

	model := req.Model
	if model == "" {
		model = h.cfg.Provider.Model
	}

	p := h.defaultProvider
	reg := tool.DefaultRegistry(h.workDir, tool.NewProviderWebFetch(p, h.cfg.Provider.ModelBackup))
	envCtx2 := prompt.QuickEnvContext(h.workDir)
	envCtx2.ProviderName = h.cfg.Provider.Default
	cfg := agent.Config{
		Provider:           p,
		Registry:           reg,
		SystemPrompt:       prompt.BuildSystemPromptFull(envCtx2, "", ""),
		Model:              model,
		MaxTokens:          8192,
		WorkDir:            h.workDir,
		SessionStore:       h.sessionStore,
		SessionID:          id,
		PermissionMode:     tool.ParsePermissionMode(h.cfg.Tools.PermissionMode),
		AllowRules:         h.cfg.Tools.AllowRules,
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	eventCh := agent.RunWithMessages(ctx, cfg, msgs)

	h.sseResponse(c, ctx, eventCh)
}

// --- Shared helpers ---

// sseResponse writes SSE events from the channel to the HTTP response.
func (h *Handler) sseResponse(c *gin.Context, ctx context.Context, eventCh <-chan agent.Event) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	for evt := range eventCh {
		data := sseEvent(evt)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		flusher.Flush()

		// Check if client disconnected
		select {
		case <-ctx.Done():
			return
		default:
		}
	}

	fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
	flusher.Flush()
}

func sseEvent(evt agent.Event) string {
	payload := map[string]interface{}{
		"type": evt.Type,
	}
	switch evt.Type {
	case "text_delta", "thinking_delta":
		payload["delta"] = evt.Delta
	case "tool_use":
		payload["tool_name"] = evt.ToolName
		payload["tool_input"] = evt.ToolInput
		payload["tool_use_id"] = evt.ToolUseID
	case "tool_result":
		if evt.ToolResult != nil {
			payload["tool_name"] = evt.ToolName
			payload["tool_use_id"] = evt.ToolUseID
			payload["content"] = evt.ToolResult.Content
			payload["is_error"] = evt.ToolResult.IsError
		}
	case "error":
		if evt.Error != nil {
			payload["error"] = evt.Error.Error()
		}
	case "compacted":
		if evt.Message != nil {
			payload["message"] = evt.Message
		}
	case "warning":
		payload["delta"] = evt.Delta
	case "permission_request":
		payload["tool_name"] = evt.PermissionToolName
		payload["description"] = evt.PermissionDesc
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// Health is a simple health check endpoint.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Models returns available models from config.
func (h *Handler) Models(c *gin.Context) {
	provider := h.cfg.Provider.Default

	// 优先用配置里的 models 列表
	modelList := h.cfg.Provider.Models
	if len(modelList) == 0 {
		// 降级：只返回默认模型
		modelList = []string{h.cfg.Provider.Model}
	}

	models := make([]map[string]string, 0, len(modelList))
	for _, m := range modelList {
		models = append(models, map[string]string{
			"provider": provider,
			"model":    m,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"models":  models,
		"default": h.cfg.Provider.Model,
	})
}

// CompactRequest is the request body for POST /api/compact.
type CompactRequest struct {
	Messages    []map[string]interface{} `json:"messages" binding:"required"`
	Model       string                   `json:"model,omitempty"`
	Instructions string                  `json:"instructions,omitempty"`
}

// CompactSSE handles POST /api/compact with Server-Sent Events streaming.
// The client sends the current message history; the server compacts it.
func (h *Handler) CompactSSE(c *gin.Context) {
	var req CompactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Deserialize messages from JSON into internal Message type
	msgsJSON, err := json.Marshal(req.Messages)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid messages format"})
		return
	}
	var msgs []message.Message
	if err := json.Unmarshal(msgsJSON, &msgs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid messages format"})
		return
	}
	if len(msgs) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not enough messages to compact"})
		return
	}

	model := req.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	// Emit compact_start event
	preTokens := token.EstimateMessages(msgs)
	data, _ := json.Marshal(map[string]interface{}{
		"type": "compact_start", "message_count": len(msgs), "token_estimate": preTokens,
	})
	fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	flusher.Flush()

	// Run compaction
	compactCfg := compact.CompactConfig{
		Provider:  h.defaultProvider,
		Model:     model,
		MaxTokens: 8192,
	}
	result, err := compact.Compact(ctx, compactCfg, msgs, req.Instructions)
	if err != nil {
		errData, _ := json.Marshal(map[string]interface{}{"type": "error", "error": err.Error()})
		fmt.Fprintf(c.Writer, "data: %s\n\n", errData)
		flusher.Flush()
		fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// Emit result
	resultData, _ := json.Marshal(map[string]interface{}{
		"type":               "compact_complete",
		"pre_compact_tokens":  result.PreCompactTokens,
		"post_compact_tokens": result.PostCompactTokens,
		"new_messages":        result.NewMessages,
	})
	fmt.Fprintf(c.Writer, "data: %s\n\n", resultData)
	flusher.Flush()

	fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
	flusher.Flush()
}

// --- Command endpoint ---

type CommandRequest struct {
	Command   string `json:"command" binding:"required"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
}

type CommandResponse struct {
	Output string      `json:"output"`
	Error  string      `json:"error,omitempty"`
	Action string      `json:"action,omitempty"`
	Data   interface{} `json:"data,omitempty"`
}

// Command handles POST /api/command — executes a slash command and returns JSON.
func (h *Handler) Command(c *gin.Context) {
	var req CommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	parts := strings.Fields(req.Command)
	if len(parts) == 0 {
		c.JSON(http.StatusBadRequest, CommandResponse{Error: "empty command"})
		return
	}
	cmd := parts[0]
	args := parts[1:]

	resp := CommandResponse{}

	switch cmd {
	case "/help":
		resp.Output = command.Help()
	case "/config":
		resp.Output = command.Config(h.cfg)
	case "/doctor":
		resp.Output = command.Doctor(h.cfg, h.workDir, h.pool)
	case "/mcp":
		resp.Output = command.MCP(h.cfg)
	case "/hooks":
		out, err := command.Hooks(h.workDir)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = out
		}
	case "/permissions":
		resp.Output = command.Permissions(h.cfg)
	case "/tasks":
		resp.Output = "No task manager available in web mode."
	case "/session":
		model := req.Model
		if model == "" {
			model = h.cfg.Provider.Model
		}
		resp.Output = command.Session(req.SessionID, h.sessionStore, model)
	case "/cost":
		if req.SessionID == "" || h.sessionStore == nil {
			resp.Output = "No session specified or database not available."
		} else {
			msgs, err := h.sessionStore.LoadMessages(c.Request.Context(), req.SessionID)
			if err != nil {
				resp.Error = err.Error()
			} else {
				resp.Output = command.Cost(msgs)
			}
		}
	case "/memory":
		resp.Output = command.Memory(h.pool, h.workDir, h.cfg)
	case "/diff":
		out, err := command.Diff(h.workDir, args)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = out
		}
	case "/branch":
		out, err := command.Branch(h.workDir, args)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = out
		}
	case "/commit":
		out, err := command.Commit(h.workDir, args)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = out
		}
	case "/init":
		out, err := command.Init(h.workDir)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = out
		}
	case "/rewind":
		resp.Output = "Snapshot system not available in web mode."
	case "/fast":
		newModel, out := command.Fast(h.cfg)
		resp.Output = out
		resp.Action = "model_changed"
		resp.Data = map[string]string{"model": newModel}
	case "/effort":
		if len(args) == 0 {
			resp.Error = "Usage: /effort <low|medium|high>"
		} else {
			newModel, out := command.Effort(h.cfg, args[0])
			resp.Output = out
			resp.Action = "model_changed"
			resp.Data = map[string]string{"model": newModel}
		}
	case "/review":
		diff, err := command.ReviewDiff(h.workDir, args)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = diff
			resp.Action = "review"
		}
	default:
		resp.Error = fmt.Sprintf("Unknown command: %s", cmd)
	}

	c.JSON(http.StatusOK, resp)
}

// SessionCompactSSE handles POST /api/sessions/:id/compact — compacts session messages in-place.
func (h *Handler) SessionCompactSSE(c *gin.Context) {
	if h.sessionStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
		return
	}

	id := c.Param("id")
	if _, err := h.sessionStore.Get(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	msgs, err := h.sessionStore.LoadMessages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(msgs) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not enough messages to compact"})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	preTokens := token.EstimateMessages(msgs)
	startData, _ := json.Marshal(map[string]interface{}{
		"type": "compact_start", "message_count": len(msgs), "token_estimate": preTokens,
	})
	fmt.Fprintf(c.Writer, "data: %s\n\n", startData)
	flusher.Flush()

	compactCfg := compact.CompactConfig{
		Provider:  h.defaultProvider,
		Model:     h.cfg.Provider.Model,
		MaxTokens: 8192,
	}
	result, err := compact.Compact(ctx, compactCfg, msgs, "")
	if err != nil {
		errData, _ := json.Marshal(map[string]interface{}{"type": "error", "error": err.Error()})
		fmt.Fprintf(c.Writer, "data: %s\n\n", errData)
		flusher.Flush()
		fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// 持久化压缩后的消息
	if err := h.sessionStore.ReplaceMessages(c.Request.Context(), id, result.NewMessages); err != nil {
		errData, _ := json.Marshal(map[string]interface{}{"type": "error", "error": "compact ok but persist failed: " + err.Error()})
		fmt.Fprintf(c.Writer, "data: %s\n\n", errData)
		flusher.Flush()
		fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	doneData, _ := json.Marshal(map[string]interface{}{
		"type":                "compact_complete",
		"pre_compact_tokens":  result.PreCompactTokens,
		"post_compact_tokens": result.PostCompactTokens,
	})
	fmt.Fprintf(c.Writer, "data: %s\n\n", doneData)
	flusher.Flush()
	fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
	flusher.Flush()
}