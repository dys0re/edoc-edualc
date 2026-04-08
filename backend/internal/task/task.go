package task

import (
	"context"
	"sync"
	"time"
)

// TaskType 区分不同类型的后台任务。对标 Claude Code tasks/Task.ts:TaskType
type TaskType string

const (
	TypeLocalShell TaskType = "local_bash"  // 后台 shell 命令
	TypeLocalAgent TaskType = "local_agent" // 后台子代理（预留）
)

// TaskStatus 任务状态。对标 tasks/Task.ts:TaskStatus
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusKilled    TaskStatus = "killed"
)

// IsTerminal 返回任务是否已结束（completed/failed/killed）
func (s TaskStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusKilled
}

// Task 后台任务的统一表示。对标 Claude Code tasks/types.ts:TaskStateBase
type Task struct {
	ID          string     `json:"id"`
	Type        TaskType   `json:"type"`
	Status      TaskStatus `json:"status"`
	Description string     `json:"description"`
	StartTime   time.Time  `json:"start_time"`
	EndTime     *time.Time `json:"end_time,omitempty"`
	ExitCode    int        `json:"exit_code,omitempty"`
}

// TaskNotification 当后台任务状态变更时发送的通知。
// 对标 Claude Code 的 task_notification XML 注入机制。
type TaskNotification struct {
	TaskID   string
	TaskType TaskType
	Status   TaskStatus
	Message  string
}

// taskEntry 内部追踪，包含输出 buffer 和取消函数
type taskEntry struct {
	task   Task
	output threadSafeBuffer // shell stdout+stderr
	cancel context.CancelFunc
	done   chan struct{} // 关闭表示任务结束
}

// threadSafeBuffer 线程安全的 bytes.Buffer
type threadSafeBuffer struct {
	mu  sync.RWMutex
	buf []byte
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *threadSafeBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.buf)
}

// ReadFrom 返回 offset 之后的所有数据和新 offset
func (b *threadSafeBuffer) ReadFrom(offset int64) (string, int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if offset >= int64(len(b.buf)) {
		return "", int64(len(b.buf))
	}
	data := string(b.buf[offset:])
	return data, int64(len(b.buf))
}
