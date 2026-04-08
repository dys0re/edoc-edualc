package tool

import (
	"context"
)

// TaskStarter 由 task.Manager 实现，供 BashTool 启动后台任务。
// 遵循 AgentResolver 的解耦模式：tool 包定义接口，task 包实现。
type TaskStarter interface {
	StartShellTaskFromTool(ctx context.Context, desc, command, workDir string) (string, error)
}

// TaskOutputReader 由 task.Manager 实现，供 TaskOutputTool 读取任务输出。
type TaskOutputReader interface {
	ReadOutput(taskID string, offset int64) (content string, newOffset int64, done bool, err error)
	GetBrief(taskID string) (TaskBrief, error)
}

// TaskStopper 由 task.Manager 实现，供 TaskStopTool 停止任务。
type TaskStopper interface {
	Stop(taskID string) error
	GetBrief(taskID string) (TaskBrief, error)
}

// TaskBrief 任务的简要信息，避免 tool 包导入 task 包。
type TaskBrief struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Description string `json:"description"`
}
