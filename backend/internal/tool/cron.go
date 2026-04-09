package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// CronJob 表示一个定时任务。对标 Claude Code 的 ScheduleCronTool。
type CronJob struct {
	ID        string
	Cron      string
	Prompt    string
	Recurring bool
	CreatedAt time.Time
	cancel    context.CancelFunc
}

// CronManager 管理所有定时任务。
type CronManager struct {
	mu   sync.RWMutex
	jobs map[string]*CronJob
	seq  int
}

func NewCronManager() *CronManager {
	return &CronManager{jobs: make(map[string]*CronJob)}
}

func (m *CronManager) Add(job *CronJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	job.ID = fmt.Sprintf("cron_%d", m.seq)
	m.jobs[job.ID] = job
}

func (m *CronManager) Delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return false
	}
	if job.cancel != nil {
		job.cancel()
	}
	delete(m.jobs, id)
	return true
}

func (m *CronManager) List() []*CronJob {
	m.mu.RLock()
	defer m.mu.RUnlock()
	jobs := make([]*CronJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	return jobs
}

func (m *CronManager) Get(id string) (*CronJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// ── CronCreate ──────────────────────────────────────────────────────────────

// CronCreateTool 创建定时任务。对标 CronCreate tool。
type CronCreateTool struct {
	Manager *CronManager
}

func (t *CronCreateTool) Name() string { return "CronCreate" }

func (t *CronCreateTool) Description() string {
	return "Schedule a prompt to run on a cron schedule. Uses standard 5-field cron syntax (minute hour day month weekday). Returns a job ID."
}

func (t *CronCreateTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"cron": map[string]interface{}{
				"type":        "string",
				"description": "5-field cron expression, e.g. \"*/5 * * * *\" for every 5 minutes",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The prompt to enqueue when the cron fires",
			},
			"recurring": map[string]interface{}{
				"type":        "boolean",
				"description": "true = repeat on every match; false = fire once then delete (default true)",
			},
		},
		"required": []string{"cron", "prompt"},
	}
}

type cronCreateInput struct {
	Cron      string `json:"cron"`
	Prompt    string `json:"prompt"`
	Recurring *bool  `json:"recurring"`
}

func (t *CronCreateTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in cronCreateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	if in.Cron == "" {
		return &Result{Content: "cron is required", IsError: true}, nil
	}
	if in.Prompt == "" {
		return &Result{Content: "prompt is required", IsError: true}, nil
	}

	recurring := true
	if in.Recurring != nil {
		recurring = *in.Recurring
	}

	job := &CronJob{
		Cron:      in.Cron,
		Prompt:    in.Prompt,
		Recurring: recurring,
		CreatedAt: time.Now(),
	}
	t.Manager.Add(job)

	recurStr := "recurring"
	if !recurring {
		recurStr = "one-shot"
	}
	return &Result{Content: fmt.Sprintf("Cron job created: id=%s cron=%q prompt=%q [%s]", job.ID, job.Cron, job.Prompt, recurStr)}, nil
}

func (t *CronCreateTool) IsReadOnly(_ json.RawMessage) bool           { return false }
func (t *CronCreateTool) IsConcurrencySafe(_ json.RawMessage) bool    { return false }
func (t *CronCreateTool) NeedsApproval(_ json.RawMessage) bool        { return false }
func (t *CronCreateTool) PermissionDescription(_ json.RawMessage) string { return "Create cron job" }
func (t *CronCreateTool) IsFileEdit(_ json.RawMessage) bool           { return false }

// ── CronDelete ──────────────────────────────────────────────────────────────

// CronDeleteTool 删除定时任务。对标 CronDelete tool。
type CronDeleteTool struct {
	Manager *CronManager
}

func (t *CronDeleteTool) Name() string { return "CronDelete" }

func (t *CronDeleteTool) Description() string {
	return "Cancel a cron job previously scheduled with CronCreate."
}

func (t *CronDeleteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Job ID returned by CronCreate",
			},
		},
		"required": []string{"id"},
	}
}

type cronDeleteInput struct {
	ID string `json:"id"`
}

func (t *CronDeleteTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in cronDeleteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	if in.ID == "" {
		return &Result{Content: "id is required", IsError: true}, nil
	}
	if !t.Manager.Delete(in.ID) {
		return &Result{Content: fmt.Sprintf("Cron job not found: %s", in.ID), IsError: true}, nil
	}
	return &Result{Content: fmt.Sprintf("Cron job %s deleted.", in.ID)}, nil
}

func (t *CronDeleteTool) IsReadOnly(_ json.RawMessage) bool           { return false }
func (t *CronDeleteTool) IsConcurrencySafe(_ json.RawMessage) bool    { return false }
func (t *CronDeleteTool) NeedsApproval(_ json.RawMessage) bool        { return false }
func (t *CronDeleteTool) PermissionDescription(_ json.RawMessage) string { return "Delete cron job" }
func (t *CronDeleteTool) IsFileEdit(_ json.RawMessage) bool           { return false }

// ── CronList ─────────────────────────────────────────────────────────────────

// CronListTool 列出所有定时任务。对标 CronList tool。
type CronListTool struct {
	Manager *CronManager
}

func (t *CronListTool) Name() string { return "CronList" }

func (t *CronListTool) Description() string {
	return "List all cron jobs scheduled via CronCreate in this session."
}

func (t *CronListTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *CronListTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	jobs := t.Manager.List()
	if len(jobs) == 0 {
		return &Result{Content: "No cron jobs scheduled."}, nil
	}
	out := fmt.Sprintf("%d cron job(s):\n", len(jobs))
	for _, j := range jobs {
		recurStr := "recurring"
		if !j.Recurring {
			recurStr = "one-shot"
		}
		out += fmt.Sprintf("  %s  cron=%q  [%s]  prompt=%q\n", j.ID, j.Cron, recurStr, j.Prompt)
	}
	return &Result{Content: out}, nil
}

func (t *CronListTool) IsReadOnly(_ json.RawMessage) bool           { return true }
func (t *CronListTool) IsConcurrencySafe(_ json.RawMessage) bool    { return true }
func (t *CronListTool) NeedsApproval(_ json.RawMessage) bool        { return false }
func (t *CronListTool) PermissionDescription(_ json.RawMessage) string { return "List cron jobs" }
func (t *CronListTool) IsFileEdit(_ json.RawMessage) bool           { return false }
