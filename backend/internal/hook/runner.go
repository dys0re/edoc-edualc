package hook

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

const defaultTimeoutSec = 600 // 10 minutes, 对标 TOOL_HOOK_EXECUTION_TIMEOUT_MS

// Runner executes hooks. 对标 hooks.ts 中的 executeHooks 逻辑。
type Runner struct {
	Config  HooksConfig
	WorkDir string
	Shell   string // "auto" / "bash" / "powershell" / "cmd"
}

// RunPreToolUse executes PreToolUse hooks for the given tool.
// 对标 hooks.ts:3394 executePreToolHooks
func (r *Runner) RunPreToolUse(ctx context.Context, toolName, toolUseID string, toolInput json.RawMessage) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(PreToolUse),
		ToolName:      toolName,
		ToolInput:     json.RawMessage(toolInput),
		ToolUseID:     toolUseID,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, PreToolUse, toolName, input)
}

// RunPostToolUse executes PostToolUse hooks for the given tool.
// 对标 hooks.ts:3450 executePostToolHooks
func (r *Runner) RunPostToolUse(ctx context.Context, toolName, toolUseID string, toolInput json.RawMessage, toolResponse interface{}) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(PostToolUse),
		ToolName:      toolName,
		ToolInput:     json.RawMessage(toolInput),
		ToolUseID:     toolUseID,
		ToolResponse:  toolResponse,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, PostToolUse, toolName, input)
}

// RunUserPromptSubmit executes UserPromptSubmit hooks.
// 对标 hooks.ts executeUserPromptSubmitHooks
func (r *Runner) RunUserPromptSubmit(ctx context.Context, prompt string) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(UserPromptSubmit),
		Prompt:        prompt,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, UserPromptSubmit, "", input)
}

// RunStop executes Stop hooks.
// 对标 hooks.ts executeStopHooks
func (r *Runner) RunStop(ctx context.Context) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(Stop),
		CWD:           r.WorkDir,
	}
	return r.run(ctx, Stop, "", input)
}

// run is the core execution logic shared by all hook event types.
func (r *Runner) run(ctx context.Context, event HookEvent, matchQuery string, input HookInput) (*AggregatedResult, error) {
	hooks := r.getMatchingHooks(event, matchQuery, input)
	if len(hooks) == 0 {
		return nil, nil
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal hook input: %w", err)
	}

	agg := &AggregatedResult{}

	for _, h := range hooks {
		if h.Async {
			// 后台执行，不等待结果
			go func(hook HookConfig) {
				execCtx, cancel := context.WithTimeout(context.Background(), r.hookTimeout(hook))
				defer cancel()
				r.execCommand(execCtx, hook, inputJSON)
			}(h)
			continue
		}

		result := r.execAndProcess(ctx, h, inputJSON)
		r.mergeResult(agg, result)

		// block 决策立即停止后续 hooks
		if agg.Decision == "block" {
			break
		}
	}

	return agg, nil
}

// getMatchingHooks returns hooks that match the event and query.
// 对标 hooks.ts:1603 getMatchingHooks
func (r *Runner) getMatchingHooks(event HookEvent, matchQuery string, input HookInput) []HookConfig {
	matchers, ok := r.Config[event]
	if !ok {
		return nil
	}

	var result []HookConfig
	for _, m := range matchers {
		// matcher 过滤: 空 matcher 匹配所有，否则按 tool_name 匹配
		if m.Matcher != "" && matchQuery != "" && !matchesPattern(matchQuery, m.Matcher) {
			continue
		}

		for _, h := range m.Hooks {
			if h.Type != "command" {
				continue // Phase 1 只支持 command 类型
			}
			// if 条件过滤
			if h.If != "" && !checkIfCondition(h.If, input) {
				continue
			}
			result = append(result, h)
		}
	}
	return result
}

// matchesPattern checks if query matches the matcher pattern.
// 支持精确匹配和通配符。对标 hooks.ts 中的 matchesPattern。
func matchesPattern(query, pattern string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// 精确匹配
	if query == pattern {
		return true
	}
	// 前缀通配符: "Bash*" matches "Bash", "BashTool"
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(query, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// checkIfCondition evaluates the `if` field using permission rule syntax.
// 格式: "ToolName(pattern)" 或 "ToolName"
// 对标原版的 IfConditionSchema + permissionRuleValueFromString
func checkIfCondition(ifCond string, input HookInput) bool {
	if ifCond == "" {
		return true
	}

	// 解析 "ToolName(pattern)" 格式
	toolName, pattern := parseIfCondition(ifCond)

	// 匹配 tool_name
	if input.ToolName != "" && toolName != "" {
		if !matchesPattern(input.ToolName, toolName) {
			return false
		}
	}

	// 匹配 pattern（如果有）
	if pattern != "" && input.ToolInput != nil {
		content := extractContentForIf(input.ToolName, input.ToolInput)
		if !matchesPattern(content, pattern) {
			return false
		}
	}

	return true
}

// parseIfCondition parses "ToolName(pattern)" into (toolName, pattern).
// "Bash(git *)" → ("Bash", "git *")
// "Bash" → ("Bash", "")
func parseIfCondition(s string) (string, string) {
	idx := strings.Index(s, "(")
	if idx < 0 {
		return s, ""
	}
	toolName := s[:idx]
	rest := s[idx+1:]
	if end := strings.LastIndex(rest, ")"); end >= 0 {
		return toolName, rest[:end]
	}
	return toolName, rest
}

// extractContentForIf extracts the relevant content from tool input for if-condition matching.
// 对标 tool/permission.go:extractContentForMatching
func extractContentForIf(toolName string, toolInput interface{}) string {
	raw, ok := toolInput.(json.RawMessage)
	if !ok {
		return ""
	}
	switch toolName {
	case "Bash":
		var parsed struct{ Command string `json:"command"` }
		json.Unmarshal(raw, &parsed)
		return parsed.Command
	case "Write", "Edit", "Read":
		var parsed struct{ FilePath string `json:"file_path"` }
		json.Unmarshal(raw, &parsed)
		return parsed.FilePath
	default:
		return string(raw)
	}
}

// execAndProcess executes a single hook command and processes its output.
func (r *Runner) execAndProcess(ctx context.Context, h HookConfig, inputJSON []byte) *HookResult {
	timeout := r.hookTimeout(h)
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, exitCode, err := r.execCommand(execCtx, h, inputJSON)
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return &HookResult{
				ExitCode:      -1,
				Stderr:        fmt.Sprintf("hook timed out after %v", timeout),
				BlockingError: fmt.Sprintf("hook timed out after %v", timeout),
			}
		}
		return &HookResult{
			ExitCode: -1,
			Stderr:   err.Error(),
		}
	}

	result := &HookResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}

	// Exit code 语义，对标 hooks.ts:2617-2697
	switch exitCode {
	case 0:
		// 尝试 JSON 解析
		jsonOut, _, _ := parseHookOutput(stdout)
		if jsonOut != nil {
			parsed := processHookJSON(jsonOut, h.Command)
			return parsed
		}
		// 纯文本输出 — 成功
	case 2:
		// Blocking error
		errMsg := strings.TrimSpace(stderr)
		if errMsg == "" {
			errMsg = "No stderr output"
		}
		result.BlockingError = fmt.Sprintf("[%s]: %s", h.Command, errMsg)
		result.Decision = "block"
	default:
		// Non-blocking error — 仅显示给用户
		result.Stderr = strings.TrimSpace(stderr)
	}

	return result
}

// execCommand runs a shell command with JSON input on stdin.
// 复用 bash.go 的 shell 检测逻辑。
func (r *Runner) execCommand(ctx context.Context, h HookConfig, inputJSON []byte) (stdout, stderr string, exitCode int, err error) {
	shell := r.resolveShell()
	cmd := buildHookCommand(ctx, shell, h.Command)
	cmd.Dir = r.WorkDir
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return stdout, stderr, -1, runErr
		}
	}

	return stdout, stderr, exitCode, nil
}

// buildHookCommand creates the exec.Cmd for the given shell type.
func buildHookCommand(ctx context.Context, shell, command string) *exec.Cmd {
	switch shell {
	case "powershell":
		if path, err := exec.LookPath("pwsh"); err == nil {
			return exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-Command", command)
		}
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	case "cmd":
		return exec.CommandContext(ctx, "cmd", "/C", command)
	default: // "bash"
		return exec.CommandContext(ctx, "bash", "-c", command)
	}
}

// resolveShell determines which shell to use.
func (r *Runner) resolveShell() string {
	switch r.Shell {
	case "powershell", "bash", "cmd":
		return r.Shell
	default: // "auto" or ""
		if runtime.GOOS == "windows" {
			if _, err := exec.LookPath("pwsh"); err == nil {
				return "powershell"
			}
			if _, err := exec.LookPath("powershell"); err == nil {
				return "powershell"
			}
			if _, err := exec.LookPath("bash"); err == nil {
				return "bash"
			}
			return "cmd"
		}
		return "bash"
	}
}

func (r *Runner) hookTimeout(h HookConfig) time.Duration {
	if h.Timeout > 0 {
		return time.Duration(h.Timeout) * time.Second
	}
	return time.Duration(defaultTimeoutSec) * time.Second
}

// mergeResult aggregates a single hook result into the accumulated result.
func (r *Runner) mergeResult(agg *AggregatedResult, result *HookResult) {
	if result == nil {
		return
	}

	// Decision: block wins over approve
	if result.Decision == "block" {
		agg.Decision = "block"
	} else if result.Decision == "approve" && agg.Decision == "" {
		agg.Decision = "approve"
	}

	if result.BlockingError != "" {
		agg.BlockingErrors = append(agg.BlockingErrors, result.BlockingError)
	}

	if result.AdditionalContext != "" {
		agg.AdditionalContext = append(agg.AdditionalContext, result.AdditionalContext)
	}

	// Last updatedInput wins
	if result.UpdatedInput != nil {
		agg.UpdatedInput = result.UpdatedInput
	}

	if result.PreventContinue {
		agg.PreventContinue = true
		if result.StopReason != "" {
			agg.StopReason = result.StopReason
		}
	}
}
