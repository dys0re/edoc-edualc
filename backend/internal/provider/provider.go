package provider

import (
	"context"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
)

// Provider abstracts LLM API calls. Maps to claude.ts:queryModelWithStreaming.
type Provider interface {
	// StreamChat sends messages and streams back events via channel.
	// The channel is closed when the response is complete or an error occurs.
	StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
	Name() string
}

type ChatRequest struct {
	Messages     []message.Message
	SystemPrompt string
	Tools        []message.ToolSchema
	Model        string
	MaxTokens    int
	// Thinking enables extended thinking (Anthropic only).
	Thinking *ThinkingConfig
}

type ThinkingConfig struct {
	Enabled   bool
	BudgetTokens int
}

// StreamEvent is a single event from the streaming response.
type StreamEvent struct {
	// Type: "text_delta", "tool_use", "thinking_delta", "message_complete", "error"
	Type string

	// For text_delta / thinking_delta
	Delta string

	// For tool_use — emitted when a complete tool_use block is received
	ToolUse *message.ToolUseBlock

	// For message_complete — the full assembled assistant message
	Message *message.Message

	// For error
	Error error

	// StopReason from the API (e.g. "end_turn", "tool_use", "max_tokens")
	StopReason string
}
