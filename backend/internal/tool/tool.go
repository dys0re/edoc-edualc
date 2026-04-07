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
}

// Result is what a tool returns after execution.
type Result struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}
