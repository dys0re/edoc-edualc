package agent

import (
	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// Config holds agent loop configuration.
type Config struct {
	Provider     provider.Provider
	Registry     *tool.Registry
	SystemPrompt string
	Model        string
	MaxTokens    int
	MaxTurns     int // 0 = unlimited

	// AutoCompactThreshold is the token count at which auto-compact triggers.
	// 0 = disabled. Maps to autoCompact.ts:getAutoCompactThreshold.
	AutoCompactThreshold int

	// ModelBackup is the backup model when the primary model is rate-limited.
	// Same provider, different model (e.g., sonnet → haiku).
	// Empty = no fallback. Maps to query.ts:188 fallbackModel.
	ModelBackup string

	// MemoryStore is the PG-backed memory store. nil = memory disabled.
	MemoryStore *memory.Store
}

// State is the mutable state carried between loop iterations.
// Maps to query.ts:204 State type.
type State struct {
	Messages []message.Message
	TurnCount int

	// Recovery tracking
	MaxOutputTokensRecoveryCount int  // 截断续写恢复次数，上限 3
	HasAttemptedFallback         bool // 是否已尝试过 fallback model
	HasAttemptedCompactRecovery  bool // 是否已尝试过 compact 恢复 prompt_too_long
}

// Event is emitted by the agent loop to the caller (CLI or Web handler).
type Event struct {
	// Type: "text_delta", "thinking_delta", "tool_use", "tool_result",
	//       "message_complete", "turn_complete", "compacted", "error",
	//       "max_tokens_recovery", "max_turns_reached", "warning"
	Type string

	// For text_delta / thinking_delta
	Delta string

	// For tool_use
	ToolName  string
	ToolInput string

	// For tool_result
	ToolResult *tool.Result

	// For message_complete / compacted
	Message *message.Message

	// For error
	Error error
}

// ToolSchemas converts the registry's tools into provider-agnostic schemas
// for inclusion in API requests.
func ToolSchemas(reg *tool.Registry) []message.ToolSchema {
	tools := reg.All()
	schemas := make([]message.ToolSchema, 0, len(tools))
	for _, t := range tools {
		schemas = append(schemas, message.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return schemas
}
