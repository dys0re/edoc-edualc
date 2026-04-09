package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// SendMessageTool sends messages between agents in a team.
// 对标 Claude Code 的 SendMessageTool。
// Agents MUST use this tool to communicate — plain text output is only visible to the user.
type SendMessageTool struct {
	Manager TeamManager
}

func (t *SendMessageTool) Name() string { return "SendMessage" }

func (t *SendMessageTool) Description() string {
	return "Send a message to a teammate or broadcast to all teammates. Use this to communicate results and coordinate work. Your plain text output is only visible to the user — other agents only see messages sent via this tool."
}

func (t *SendMessageTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Recipient: a teammate name, 'team-lead', or '*' for broadcast",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "The message to send",
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "A 5-10 word preview of the message (required for plain text messages)",
			},
		},
		"required": []string{"to", "message"},
	}
}

type sendMessageInput struct {
	To      string `json:"to"`
	Message string `json:"message"`
	Summary string `json:"summary"`
}

func (t *SendMessageTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	if t.Manager == nil {
		return &Result{Content: "Error: Team system not available", IsError: true}, nil
	}

	var in sendMessageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.To == "" {
		return &Result{Content: "Error: 'to' is required", IsError: true}, nil
	}
	if in.Message == "" {
		return &Result{Content: "Error: 'message' is required", IsError: true}, nil
	}

	// Determine sender from context
	identity := AgentIdentityFromContext(ctx)
	from := identity.Name

	var err error
	if in.To == "*" {
		err = t.Manager.Broadcast(from, in.Message, in.Summary)
		if err != nil {
			return &Result{Content: fmt.Sprintf("Broadcast error: %v", err), IsError: true}, nil
		}
		return &Result{Content: "Message broadcast to all teammates."}, nil
	}

	err = t.Manager.SendMessage(from, in.To, in.Message, in.Summary)
	if err != nil {
		return &Result{Content: fmt.Sprintf("Failed to send message to %q: %v", in.To, err), IsError: true}, nil
	}

	return &Result{Content: fmt.Sprintf("Message sent to %q.", in.To)}, nil
}

func (t *SendMessageTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *SendMessageTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *SendMessageTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *SendMessageTool) PermissionDescription(_ json.RawMessage) string {
	return "Send message to teammate"
}
func (t *SendMessageTool) IsFileEdit(_ json.RawMessage) bool { return false }
