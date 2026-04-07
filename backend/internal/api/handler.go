package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
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
}

func NewHandler(p provider.Provider, cfg *config.Config, workDir string, memoryStore *memory.Store, sessionStore *session.Store) *Handler {
	return &Handler{
		defaultProvider: p,
		cfg:            cfg,
		workDir:        workDir,
		memoryStore:    memoryStore,
		sessionStore:   sessionStore,
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

	reg := tool.DefaultRegistry(h.workDir)
	cfg := agent.Config{
		Provider:           p,
		Registry:           reg,
		SystemPrompt:       prompt.BuildSystemPrompt(h.workDir),
		Model:              model,
		MaxTokens:          8192,
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

	// Append new user message to history
	msgs = append(msgs, message.NewUserMessage(req.Prompt))

	model := req.Model
	if model == "" {
		model = h.cfg.Provider.Model
	}

	p := h.defaultProvider
	reg := tool.DefaultRegistry(h.workDir)
	cfg := agent.Config{
		Provider:           p,
		Registry:           reg,
		SystemPrompt:       prompt.BuildSystemPrompt(h.workDir),
		Model:              model,
		MaxTokens:          8192,
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

// Models returns available models/providers.
func (h *Handler) Models(c *gin.Context) {
	models := []map[string]string{}

	if h.cfg.Anthropic.APIKey != "" {
		models = append(models,
			map[string]string{"provider": "anthropic", "model": "claude-sonnet-4-20250514"},
			map[string]string{"provider": "anthropic", "model": "claude-opus-4-20250514"},
			map[string]string{"provider": "anthropic", "model": "claude-haiku-4-5-20251001"},
		)
	}
	if h.cfg.OpenAI.APIKey != "" {
		models = append(models,
			map[string]string{"provider": "openai", "model": "gpt-4o"},
			map[string]string{"provider": "openai", "model": "o3"},
		)
	}

	c.JSON(http.StatusOK, gin.H{"models": models})
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
