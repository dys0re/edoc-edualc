package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SleepTool 等待指定毫秒数。对标 Claude Code 的 SleepTool。
type SleepTool struct{}

func (t *SleepTool) Name() string { return "Sleep" }

func (t *SleepTool) Description() string {
	return "Wait for a specified number of milliseconds before continuing."
}

func (t *SleepTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"duration_ms": map[string]interface{}{
				"type":        "integer",
				"description": "Number of milliseconds to wait (max 60000)",
				"minimum":     0,
				"maximum":     60000,
			},
		},
		"required": []string{"duration_ms"},
	}
}

type sleepInput struct {
	DurationMs int `json:"duration_ms"`
}

func (t *SleepTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	var in sleepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	if in.DurationMs < 0 {
		return &Result{Content: "duration_ms must be >= 0", IsError: true}, nil
	}
	if in.DurationMs > 60000 {
		in.DurationMs = 60000
	}

	select {
	case <-time.After(time.Duration(in.DurationMs) * time.Millisecond):
		return &Result{Content: fmt.Sprintf("Slept for %dms", in.DurationMs)}, nil
	case <-ctx.Done():
		return &Result{Content: "Sleep interrupted", IsError: true}, nil
	}
}

func (t *SleepTool) IsReadOnly(_ json.RawMessage) bool           { return true }
func (t *SleepTool) IsConcurrencySafe(_ json.RawMessage) bool    { return true }
func (t *SleepTool) NeedsApproval(_ json.RawMessage) bool        { return false }
func (t *SleepTool) PermissionDescription(_ json.RawMessage) string { return "Sleep" }
func (t *SleepTool) IsFileEdit(_ json.RawMessage) bool           { return false }
