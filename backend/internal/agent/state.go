package agent

import (
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
}

// State is the mutable state carried between loop iterations.
// Maps to query.ts:204 State type.
type State struct {
	Messages []message.Message
	TurnCount int
}

// Event is emitted by the agent loop to the caller (CLI or Web handler).
type Event struct {
	// Type: "text_delta", "thinking_delta", "tool_use", "tool_result",
	//       "message_complete", "turn_complete", "error"
	Type string

	// For text_delta / thinking_delta
	Delta string

	// For tool_use
	ToolName  string
	ToolInput string

	// For tool_result
	ToolResult *tool.Result

	// For message_complete — the full assistant message
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
