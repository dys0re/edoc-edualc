package tool

import (
	"context"
	"encoding/json"
)

// Tool is the core tool interface. Maps to Tool.ts:362.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]interface{}
	Execute(ctx context.Context, input json.RawMessage) (*Result, error)
	IsReadOnly(input json.RawMessage) bool
	IsConcurrencySafe(input json.RawMessage) bool
	// NeedsApproval returns true if this tool invocation requires user approval.
	NeedsApproval(input json.RawMessage) bool
	// PermissionDescription returns a human-readable description for the permission prompt.
	PermissionDescription(input json.RawMessage) string
	// IsFileEdit returns true if this tool modifies files (Write/Edit).
	// Used by accept-edits mode to auto-approve file changes.
	IsFileEdit(input json.RawMessage) bool
}

// Result is what a tool returns after execution.
type Result struct {
	Content  string            `json:"content"`
	IsError  bool              `json:"is_error,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
