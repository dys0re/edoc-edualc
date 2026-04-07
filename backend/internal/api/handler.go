package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/compact"
	"github.com/dysorder/edoc-edualc/backend/internal/config"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/prompt"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
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
}

func NewHandler(p provider.Provider, cfg *config.Config, workDir string) *Handler {
	return &Handler{defaultProvider: p, cfg: cfg, workDir: workDir}
}

// ChatSSE handles POST /api/chat with Server-Sent Events streaming.
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
		Provider:     p,
		Registry:     reg,
		SystemPrompt: prompt.BuildSystemPrompt(h.workDir),
		Model:        model,
		MaxTokens:    8192,
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	eventCh := agent.Run(ctx, cfg, req.Prompt)

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
