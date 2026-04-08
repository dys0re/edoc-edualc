package task

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// startShellTask 启动后台 shell 任务。对标 tasks/LocalShellTask.ts。
func (m *Manager) startShellTask(ctx context.Context, desc, command, workDir string) (string, error) {
	taskID := fmt.Sprintf("task-%d", m.nextID.Add(1))

	childCtx, cancel := context.WithCancel(ctx)

	entry := &taskEntry{
		task: Task{
			ID:          taskID,
			Type:        TypeLocalShell,
			Status:      StatusRunning,
			Description: desc,
			StartTime:   time.Now(),
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}

	m.mu.Lock()
	m.tasks[taskID] = entry
	m.mu.Unlock()

	// 启动 goroutine 执行命令
	go func() {
		defer close(entry.done)

		// 用 childCtx 构建命令，支持 cancel
		shell := tool.DetectShell()
		cmd := tool.BuildCommand(childCtx, shell, command)
		cmd.Dir = workDir
		cmd.Stdout = &entry.output
		cmd.Stderr = &entry.output

		err := cmd.Run()

		now := time.Now()
		m.mu.Lock()
		entry.task.EndTime = &now
		if err != nil {
			if childCtx.Err() == context.Canceled {
				entry.task.Status = StatusKilled
			} else {
				entry.task.Status = StatusFailed
				if exitErr, ok := err.(*exec.ExitError); ok {
					entry.task.ExitCode = exitErr.ExitCode()
				}
			}
		} else {
			entry.task.Status = StatusCompleted
		}
		m.mu.Unlock()

		// 非阻塞发送通知
		notif := TaskNotification{
			TaskID:   taskID,
			TaskType: TypeLocalShell,
			Status:   entry.task.Status,
			Message:  fmt.Sprintf("Task %q %s", desc, entry.task.Status),
		}
		select {
		case m.notifyCh <- notif:
		default:
		}
	}()

	return taskID, nil
}
