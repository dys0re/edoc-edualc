package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// ShellType controls which shell is used to execute commands.
type ShellType string

const (
	ShellAuto       ShellType = ""           // auto-detect: powershell > bash > cmd on Windows, bash on Unix
	ShellPowerShell ShellType = "powershell" // pwsh or powershell
	ShellBash       ShellType = "bash"       // bash (Git Bash on Windows)
	ShellCmd        ShellType = "cmd"        // cmd.exe (Windows only)
)

type BashTool struct {
	workDir string
	shell   ShellType
	taskMgr TaskStarter // 后台任务管理器。nil = 后台执行不可用。
}

func NewBashTool(workDir string, shell ShellType) *BashTool {
	return &BashTool{workDir: workDir, shell: shell}
}

// SetTaskManager 设置后台任务管理器。由 main.go 接线时调用。
func (t *BashTool) SetTaskManager(mgr TaskStarter) {
	t.taskMgr = mgr
}

// SetWorkDir dynamically changes the working directory.
// Used by agent loop when entering/exiting a worktree.
func (t *BashTool) SetWorkDir(dir string) {
	t.workDir = dir
}

// GetWorkDir returns the current working directory.
func (t *BashTool) GetWorkDir() string {
	return t.workDir
}

func (t *BashTool) Name() string { return "Bash" }

func (t *BashTool) Description() string {
	return "Execute a command and return its output."
}

func (t *BashTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The command to execute",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in milliseconds (max 600000)",
			},
			"run_in_background": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, run the command in the background and return immediately. Use TaskOutput to read results.",
			},
		},
		"required": []string{"command"},
	}
}

type bashInput struct {
	Command         string `json:"command"`
	Timeout         int    `json:"timeout,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if strings.TrimSpace(in.Command) == "" {
		return &Result{Content: "Empty command", IsError: true}, nil
	}

	// ── 后台执行模式 ──
	// 对标 Claude Code 的 BashTool run_in_background 参数。
	if in.RunInBackground && t.taskMgr != nil {
		taskID, err := t.taskMgr.StartShellTaskFromTool(ctx, in.Command, in.Command, t.workDir)
		if err != nil {
			return &Result{Content: fmt.Sprintf("Failed to start background task: %v", err), IsError: true}, nil
		}
		return &Result{
			Content: fmt.Sprintf("Background task started: %s\nUse TaskOutput with task_id %q to read output.", taskID, taskID),
			Metadata: map[string]string{
				"background_task_id": taskID,
			},
		}, nil
	}

	timeout := 120 * time.Second
	if in.Timeout > 0 {
		timeout = time.Duration(min(in.Timeout, 600000)) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := t.shell
	if shell == ShellAuto {
		shell = DetectShell()
	}

	cmd := BuildCommand(ctx, shell, in.Command)
	cmd.Dir = t.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	outStr := DecodeOutput(stdout.Bytes())
	errStr := DecodeOutput(stderr.Bytes())

	var output string
	if outStr != "" && errStr != "" {
		output = outStr + "\n" + errStr
	} else if outStr != "" {
		output = outStr
	} else if errStr != "" {
		output = errStr
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{Content: fmt.Sprintf("Command timed out after %v\n%s", timeout, output), IsError: true}, nil
		}
		if output == "" {
			output = err.Error()
		}
		return &Result{Content: output, IsError: true}, nil
	}

	if output == "" {
		output = "(no output)"
	}
	return &Result{Content: output}, nil
}

func (t *BashTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *BashTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *BashTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *BashTool) PermissionDescription(input json.RawMessage) string {
	var parsed struct{ Command string `json:"command"` }
	json.Unmarshal(input, &parsed)
	return "Execute command: " + parsed.Command
}
func (t *BashTool) IsFileEdit(_ json.RawMessage) bool { return false }

// BuildCommand creates the exec.Cmd for the given shell type.
// Exported for use by the task package (background shell execution).
func BuildCommand(ctx context.Context, shell ShellType, command string) *exec.Cmd {
	switch shell {
	case ShellPowerShell:
		// pwsh (PowerShell 7+) preferred, fall back to Windows PowerShell
		if _, err := exec.LookPath("pwsh"); err == nil {
			return exec.CommandContext(ctx, "pwsh", "-NoProfile", "-Command", command)
		}
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	case ShellBash:
		return exec.CommandContext(ctx, "bash", "-c", command)
	case ShellCmd:
		return exec.CommandContext(ctx, "cmd", "/C", command)
	default:
		// Unix default
		return exec.CommandContext(ctx, "bash", "-c", command)
	}
}

// DetectShell picks the best available shell on the current OS.
// Windows: pwsh > powershell > bash > cmd
// Unix: bash
func DetectShell() ShellType {
	if runtime.GOOS != "windows" {
		return ShellBash
	}
	if _, err := exec.LookPath("pwsh"); err == nil {
		return ShellPowerShell
	}
	if _, err := exec.LookPath("powershell"); err == nil {
		return ShellPowerShell
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return ShellBash
	}
	return ShellCmd
}

// DecodeOutput decodes command output bytes to a UTF-8 string.
// On Windows, command output is often GBK (CP936) on Chinese systems.
// Strategy: if bytes are valid UTF-8, use as-is; otherwise try GBK → UTF-8.
// Exported for use by the task package (background shell output).
func DecodeOutput(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if isValidUTF8(data) {
		return string(data)
	}
	decoded, err := decodeGBK(data)
	if err == nil {
		return decoded
	}
	return string(data)
}

func isValidUTF8(data []byte) bool {
	for _, b := range string(data) {
		if b == '\uFFFD' {
			return false
		}
	}
	return true
}

func decodeGBK(data []byte) (string, error) {
	reader := transform.NewReader(bytes.NewReader(data), simplifiedchinese.GBK.NewDecoder())
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
