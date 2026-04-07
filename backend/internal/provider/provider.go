package provider

import (
	"context"
	"errors"
	"strings"

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
	Enabled      bool
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

// --- Error classification ---
// 对标 Claude Code 的 withRetry.ts 错误分类 + query.ts 的错误恢复逻辑

// ErrorType classifies provider errors for the agent loop to handle.
type ErrorType int

const (
	ErrorNone          ErrorType = iota // 无错误
	ErrorRateLimit                      // 429 / overloaded → 降级 fallback model
	ErrorPromptTooLong                  // context 超限 → compact 或报错
	ErrorMaxOutputTokens                // 输出被截断 → 续写恢复
	ErrorAuth                           // 认证失败 → 直接报错
	ErrorServer                         // 5xx → 可重试
	ErrorUnknown                        // 其他
)

// ClassifyError 根据错误信息判断错误类型。
// Anthropic: "overloaded", "rate_limit", "prompt_too_long", "max_output_tokens"
// OpenAI: "rate_limit", "context_length_exceeded", "max_tokens"
func ClassifyError(err error) ErrorType {
	if err == nil {
		return ErrorNone
	}
	msg := strings.ToLower(err.Error())

	// Rate limit / overloaded
	if strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "capacity") {
		return ErrorRateLimit
	}

	// Prompt too long
	if strings.Contains(msg, "prompt_too_long") ||
		strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "request too large") {
		return ErrorPromptTooLong
	}

	// Max output tokens (from stop_reason in stream)
	if strings.Contains(msg, "max_output_tokens") ||
		strings.Contains(msg, "max_tokens") {
		return ErrorMaxOutputTokens
	}

	// Auth
	if strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "invalid api key") ||
		strings.Contains(msg, "invalid x-api-key") ||
		strings.Contains(msg, "401") ||
		strings.Contains(msg, "permission denied") {
		return ErrorAuth
	}

	// Server error (5xx)
	if strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "internal server error") ||
		strings.Contains(msg, "server error") {
		return ErrorServer
	}

	return ErrorUnknown
}

// IsMaxTokensStop 判断 API 的 stop_reason 是否为输出截断。
func IsMaxTokensStop(reason string) bool {
	return reason == "max_tokens" || reason == "length"
}

// IsRetryableError 判断是否值得重试（fallback model 或延迟重试）
func IsRetryableError(err error) bool {
	t := ClassifyError(err)
	return t == ErrorRateLimit || t == ErrorServer
}

// --- Sentinel errors ---

// ErrPromptTooLong is returned when the message history exceeds the model's context window.
var ErrPromptTooLong = errors.New("prompt too long: message history exceeds model context window")

// ErrMaxOutputTokens is used as a stop reason indicator, not a fatal error.
var ErrMaxOutputTokens = errors.New("max output tokens reached: response was truncated")
