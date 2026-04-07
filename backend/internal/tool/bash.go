package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type BashTool struct {
	workDir string
}

func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir}
}

func (t *BashTool) Name() string { return "Bash" }

func (t *BashTool) Description() string {
	return "Execute a bash command and return its output."
}

func (t *BashTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The bash command to execute",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in milliseconds (max 600000)",
			},
		},
		"required": []string{"command"},
	}
}

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if strings.TrimSpace(in.Command) == "" {
		return &Result{Content: "Empty command", IsError: true}, nil
	}

	timeout := 120 * time.Second
	if in.Timeout > 0 {
		timeout = time.Duration(min(in.Timeout, 600000)) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// Use bash if available (Git Bash, WSL), fall back to cmd
		if _, err := exec.LookPath("bash"); err == nil {
			cmd = exec.CommandContext(ctx, "bash", "-c", in.Command)
		} else {
			cmd = exec.CommandContext(ctx, "cmd", "/C", in.Command)
		}
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", in.Command)
	}

	cmd.Dir = t.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var output strings.Builder
	if stdout.Len() > 0 {
		output.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString(stderr.String())
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{Content: fmt.Sprintf("Command timed out after %v\n%s", timeout, output.String()), IsError: true}, nil
		}
		// Non-zero exit code — still return output, mark as error
		content := output.String()
		if content == "" {
			content = err.Error()
		}
		return &Result{Content: content, IsError: true}, nil
	}

	content := output.String()
	if content == "" {
		content = "(no output)"
	}
	return &Result{Content: content}, nil
}

func (t *BashTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *BashTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
