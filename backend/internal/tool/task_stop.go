package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// TaskStopTool 停止运行中的后台任务。对标 TaskStopTool.ts。
type TaskStopTool struct {
	Manager TaskStopper
}

func (t *TaskStopTool) Name() string { return "TaskStop" }

func (t *TaskStopTool) Description() string {
	return "Stop a running background task by its ID."
}

func (t *TaskStopTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "The ID of the background task to stop",
			},
		},
		"required": []string{"task_id"},
	}
}

type taskStopInput struct {
	TaskID string `json:"task_id"`
}

func (t *TaskStopTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	var in taskStopInput
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

	// 停止任务
	if err := t.Manager.Stop(in.TaskID); err != nil {
		return &Result{Content: fmt.Sprintf("Failed to stop task %s: %v", in.TaskID, err), IsError: true}, nil
	}

	return &Result{Content: fmt.Sprintf("Task %s (%s) stopped.", brief.ID, brief.Description)}, nil
}

func (t *TaskStopTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *TaskStopTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *TaskStopTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *TaskStopTool) PermissionDescription(_ json.RawMessage) string {
	return "Stop background task"
}
func (t *TaskStopTool) IsFileEdit(_ json.RawMessage) bool { return false }
