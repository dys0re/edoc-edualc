package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TaskOutputTool 读取后台任务的输出。对标 TaskOutputTool.ts。
type TaskOutputTool struct {
	Manager TaskOutputReader
}

func (t *TaskOutputTool) Name() string { return "TaskOutput" }

func (t *TaskOutputTool) Description() string {
	return "Read output from a background task. Supports blocking mode to wait for task completion."
}

func (t *TaskOutputTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the background task to read output from",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Byte offset to start reading from (0 = beginning, default 0)",
			},
			"blocking": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, wait until the task completes before returning output (default false)",
			},
		},
		"required": []string{"task_id"},
	}
}

type taskOutputInput struct {
	TaskID   string `json:"task_id"`
	Offset   int64  `json:"offset,omitempty"`
	Blocking bool   `json:"blocking,omitempty"`
}

func (t *TaskOutputTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	var in taskOutputInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.TaskID == "" {
		return &Result{Content: "task_id is required", IsError: true}, nil
	}

	if t.Manager == nil {
		return &Result{Content: "Task system not available", IsError: true}, nil
	}

	// 获取任务信息
	brief, err := t.Manager.GetBrief(in.TaskID)
	if err != nil {
		return &Result{Content: err.Error(), IsError: true}, nil
	}

	// Blocking 模式：轮询等待任务完成
	if in.Blocking {
		for {
			content, newOffset, done, err := t.Manager.ReadOutput(in.TaskID, in.Offset)
			if err != nil {
				return &Result{Content: err.Error(), IsError: true}, nil
			}
			if done {
				return &Result{Content: formatTaskOutput(brief, content, newOffset, true)}, nil
			}
			select {
			case <-ctx.Done():
				// 返回当前已有的输出
				return &Result{Content: formatTaskOutput(brief, content, newOffset, false)}, nil
			case <-time.After(100 * time.Millisecond):
				// 继续轮询
			}
		}
	}

	// 非阻塞模式：立即返回当前输出
	content, newOffset, done, err := t.Manager.ReadOutput(in.TaskID, in.Offset)
	if err != nil {
		return &Result{Content: err.Error(), IsError: true}, nil
	}

	return &Result{Content: formatTaskOutput(brief, content, newOffset, done)}, nil
}

func formatTaskOutput(brief TaskBrief, content string, offset int64, done bool) string {
	status := "running"
	if done {
		status = "completed"
	}
	result := fmt.Sprintf("Task %s (%s) — status: %s\n", brief.ID, brief.Description, status)
	if content != "" {
		result += fmt.Sprintf("Output (offset %d):\n%s", offset, content)
	} else {
		result += "(no output yet)"
	}
	return result
}

func (t *TaskOutputTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *TaskOutputTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *TaskOutputTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *TaskOutputTool) PermissionDescription(_ json.RawMessage) string {
	return "Read background task output"
}
func (t *TaskOutputTool) IsFileEdit(_ json.RawMessage) bool { return false }
