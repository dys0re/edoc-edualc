package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentTool lets the LLM spawn sub-agents for delegated tasks.
// Maps to Claude Code's AgentTool.
type AgentTool struct {
	// Resolver is called to create and run the sub-agent.
	// Returns a channel of events (same as agent.Run).
	Resolver AgentResolver
}

// AgentResolver is implemented by the agent package to run a sub-agent
// without creating a circular import. The agent package registers itself
// at startup via AgentTool.Resolver.
type AgentResolver interface {
	// RunSubAgent starts a sub-agent loop. The returned channel is closed when done.
	// The caller reads events to collect the final result.
	RunSubAgent(ctx context.Context, params SubAgentParams) (<-chan SubAgentEvent, error)
}

// SubAgentParams is the input to a sub-agent invocation.
type SubAgentParams struct {
	Prompt           string            // Task description
	SystemPrompt     string            // Override system prompt (empty = inherit parent's)
	Model            string            // Model to use (empty = inherit parent's)
	MaxTokens        int               // Max output tokens per turn
	MaxTurns         int               // Max turns (0 = unlimited)
	Tools            []Tool            // Tools available to sub-agent (nil = inherit parent's)
	PermissionMode   PermissionMode    // Permission mode
	AllowRules       []string          // Permission allow rules
	PermissionCallback PermissionCallback // Permission callback
	Metadata         map[string]string // Extra metadata for logging
}

// SubAgentEvent is emitted by a running sub-agent.
type SubAgentEvent struct {
	Type string // "text_delta", "tool_use", "tool_result", "done", "error"

	// For text_delta
	Delta string

	// For tool_use
	ToolName  string
	ToolInput string

	// For tool_result
	ToolResult *Result

	// For done — the final assembled text output
	Output string

	// For error
	Error error
}

// agentInput matches the LLM tool call schema.
type agentInput struct {
	Description string `json:"description"` // Short task description
	Prompt      string `json:"prompt"`      // Full task prompt
	SubagentType string `json:"subagent_type,omitempty"` // Agent type (reserved)
}

func (t *AgentTool) Name() string        { return "Agent" }
func (t *AgentTool) Description() string { return "Launch a sub-agent to handle a delegated task. Use for research, exploration, or any multi-step work that benefits from focused attention." }

func (t *AgentTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"description": map[string]interface{}{
				"type":        "string",
				"description": "A short (3-5 word) description of the task",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The full task description for the sub-agent to perform",
			},
		},
		"required": []string{"description", "prompt"},
	}
}

func (t *AgentTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	if t.Resolver == nil {
		return &Result{Content: "Error: Agent resolver not configured", IsError: true}, nil
	}

	var in agentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Error parsing input: %v", err), IsError: true}, nil
	}

	if in.Prompt == "" {
		return &Result{Content: "Error: prompt is required", IsError: true}, nil
	}

	// Build sub-agent params — caller (the agent loop) fills in Model, Tools, etc.
	// via the Resolver. Here we just pass what the LLM specified.
	params := SubAgentParams{
		Prompt:       in.Prompt,
		SystemPrompt: "", // inherit
		MaxTurns:     10,
	}

	eventCh, err := t.Resolver.RunSubAgent(ctx, params)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Error starting sub-agent: %v", err), IsError: true}, nil
	}

	// Consume events and assemble the final output.
	var output string
	for evt := range eventCh {
		switch evt.Type {
		case "done":
			output = evt.Output
		case "error":
			if output == "" {
				output = fmt.Sprintf("Sub-agent error: %v", evt.Error)
			}
		}
	}

	if output == "" {
		output = "(sub-agent completed with no output)"
	}

	return &Result{Content: output, IsError: false}, nil
}

func (t *AgentTool) IsReadOnly(input json.RawMessage) bool {
	// Sub-agents can write files via their own tools
	return false
}

func (t *AgentTool) IsConcurrencySafe(input json.RawMessage) bool {
	// Don't run sub-agents concurrently — they may modify the same files
	return false
}

func (t *AgentTool) NeedsApproval(input json.RawMessage) bool {
	// Sub-agents are read/write, always need approval in default mode
	return true
}

func (t *AgentTool) PermissionDescription(input json.RawMessage) string {
	var in agentInput
	json.Unmarshal(input, &in)
	desc := in.Description
	if desc == "" {
		desc = "(untitled task)"
	}
	return "Launch sub-agent: " + desc
}

func (t *AgentTool) IsFileEdit(input json.RawMessage) bool {
	return false
}
