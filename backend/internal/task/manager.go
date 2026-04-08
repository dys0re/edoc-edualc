package task

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// Manager 管理所有后台任务的生命周期。对标 Claude Code 的 AppState.tasks。
type Manager struct {
	mu     sync.RWMutex
	tasks  map[string]*taskEntry
	nextID atomic.Int64

	// notifyCh 发送任务完成/失败通知。
	// agent loop 在每轮迭代开始时非阻塞检查此 channel。
	notifyCh chan TaskNotification
}

// NewManager 创建 TaskManager 实例
func NewManager() *Manager {
	return &Manager{
		tasks:    make(map[string]*taskEntry),
		notifyCh: make(chan TaskNotification, 16),
	}
}

// Notifications 返回通知 channel。agent loop 消费此 channel。
func (m *Manager) Notifications() <-chan TaskNotification {
	return m.notifyCh
}

// Get 获取任务信息
func (m *Manager) Get(taskID string) (Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.tasks[taskID]
	if !ok {
		return Task{}, fmt.Errorf("task not found: %s", taskID)
	}
	return entry.task, nil
}

// List 列出所有任务
func (m *Manager) List() []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tasks := make([]Task, 0, len(m.tasks))
	for _, entry := range m.tasks {
		tasks = append(tasks, entry.task)
	}
	return tasks
}

// GetBrief 返回任务的简要信息。满足 tool.TaskOutputReader 和 tool.TaskStopper 接口。
func (m *Manager) GetBrief(taskID string) (tool.TaskBrief, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.tasks[taskID]
	if !ok {
		return tool.TaskBrief{}, fmt.Errorf("task not found: %s", taskID)
	}
	return tool.TaskBrief{
		ID:          entry.task.ID,
		Status:      string(entry.task.Status),
		Description: entry.task.Description,
	}, nil
}

// ReadOutput 读取任务输出，从 offset 开始。对标 TaskOutputTool。
// 返回 (内容, 新offset, 是否已完成)。
func (m *Manager) ReadOutput(taskID string, offset int64) (string, int64, bool, error) {
	m.mu.RLock()
	entry, ok := m.tasks[taskID]
	if !ok {
		m.mu.RUnlock()
		return "", 0, false, fmt.Errorf("task not found: %s", taskID)
	}
	m.mu.RUnlock()

	content, newOffset := entry.output.ReadFrom(offset)

	// 检查是否已完成
	select {
	case <-entry.done:
		return content, newOffset, true, nil
	default:
		return content, newOffset, false, nil
	}
}

// Stop 终止指定任务。对标 TaskStopTool
func (m *Manager) Stop(taskID string) error {
	m.mu.RLock()
	entry, ok := m.tasks[taskID]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	m.mu.RUnlock()

	if entry.task.Status.IsTerminal() {
		return fmt.Errorf("task %s already %s", taskID, entry.task.Status)
	}

	if entry.cancel != nil {
		entry.cancel()
	}

	// 等待任务 goroutine 退出
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		// 超时，任务可能仍在清理
	}

	return nil
}

// StartShellTaskFromTool 由 BashTool 调用，启动后台 shell 命令。
// 对标 tasks/LocalShellTask.ts。返回 task ID。
func (m *Manager) StartShellTaskFromTool(ctx context.Context, desc, command, workDir string) (string, error) {
	return m.startShellTask(ctx, desc, command, workDir)
}

// Close 终止所有运行中的任务，关闭 manager。
func (m *Manager) Close() {
	m.mu.Lock()
	for _, entry := range m.tasks {
		if !entry.task.Status.IsTerminal() && entry.cancel != nil {
			entry.cancel()
		}
	}
	m.mu.Unlock()
}
