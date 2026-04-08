package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// subagentResolver implements tool.AgentResolver.
type subagentResolver struct {
	parentConfig Config
}

// NewSubagentResolver creates a resolver that inherits from the parent agent config.
func NewSubagentResolver(parentConfig Config) tool.AgentResolver {
	return &subagentResolver{parentConfig: parentConfig}
}

func (r *subagentResolver) RunSubAgent(ctx context.Context, params tool.SubAgentParams) (<-chan tool.SubAgentEvent, error) {
	ch := make(chan tool.SubAgentEvent, 64)

	go func() {
		defer close(ch)
		r.run(ctx, params, ch)
	}()

	return ch, nil
}

func (r *subagentResolver) run(ctx context.Context, params tool.SubAgentParams, ch chan<- tool.SubAgentEvent) {
	// Resolve inherited values
	model := params.Model
	if model == "" {
		model = r.parentConfig.Model
	}

	systemPrompt := params.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = r.parentConfig.SystemPrompt
	}

	permMode := params.PermissionMode
	if permMode == "" {
		permMode = r.parentConfig.PermissionMode
	}

	allowRules := params.AllowRules
	if allowRules == nil {
		allowRules = r.parentConfig.AllowRules
	}

	permCallback := params.PermissionCallback
	if permCallback == nil {
		permCallback = r.parentConfig.PermissionCallback
	}

	var reg *tool.Registry
	if len(params.Tools) > 0 {
		reg = tool.NewRegistry()
		for _, t := range params.Tools {
			reg.Register(t)
		}
	} else {
		reg = r.parentConfig.Registry
	}

	maxTokens := params.MaxTokens
	if maxTokens == 0 {
		maxTokens = r.parentConfig.MaxTokens
	}

	maxTurns := params.MaxTurns
	if maxTurns == 0 {
		maxTurns = 10
	}

	cfg := Config{
		Provider:           r.parentConfig.Provider,
		Registry:           reg,
		SystemPrompt:       systemPrompt,
		Model:              model,
		MaxTokens:          maxTokens,
		MaxTurns:           maxTurns,
		PermissionMode:     permMode,
		AllowRules:         allowRules,
		PermissionCallback: permCallback,
		// Sub-agents don't auto-compact by default
		AutoCompactThreshold: 0,
		MemoryStore:         r.parentConfig.MemoryStore,
		SessionStore:        nil, // sub-agents don't persist their own sessions
	}

	// Start with just the user prompt
	messages := []message.Message{message.NewUserMessage(params.Prompt)}

	// Run the sub-agent loop using the existing infrastructure
	parentCh := runWithMessages(ctx, cfg, messages)

	// Collect events and assemble the final output.
	// We only accumulate text from message_complete (which has the full text),
	// not from text_delta (which would cause duplication).
	var textParts []string

	for evt := range parentCh {
		switch evt.Type {
		case "text_delta":
			ch <- tool.SubAgentEvent{Type: "text_delta", Delta: evt.Delta}

		case "thinking_delta":
			ch <- tool.SubAgentEvent{Type: "thinking_delta", Delta: evt.Delta}

		case "tool_use":
			ch <- tool.SubAgentEvent{
				Type:      "tool_use",
				ToolName:  evt.ToolName,
				ToolInput: evt.ToolInput,
			}

		case "tool_result":
			ch <- tool.SubAgentEvent{
				Type:       "tool_result",
				ToolName:   evt.ToolName,
				ToolResult: evt.ToolResult,
			}

		case "message_complete":
			if evt.Message != nil {
				for _, block := range evt.Message.Content {
					if block.Type == message.BlockText && block.Text != nil {
						textParts = append(textParts, block.Text.Text)
					}
				}
			}

		case "error":
			ch <- tool.SubAgentEvent{Type: "error", Error: evt.Error}
			return

		case "max_turns_reached":
			ch <- tool.SubAgentEvent{
				Type:  "error",
				Error: fmt.Errorf("sub-agent reached max turns (%d)", maxTurns),
			}
			return
		}
	}

	// Send final assembled output
	output := strings.Join(textParts, "")
	ch <- tool.SubAgentEvent{Type: "done", Output: output}
}
