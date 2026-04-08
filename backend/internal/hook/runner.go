package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const defaultTimeoutSec = 600 // 10 minutes, 对标 TOOL_HOOK_EXECUTION_TIMEOUT_MS
const defaultHTTPTimeoutSec = 600 // 10 minutes, 对标 DEFAULT_HTTP_HOOK_TIMEOUT_MS

// PromptEvaluator is called by prompt hooks to evaluate a prompt via LLM.
// Returns {"ok": true} or {"ok": false, "reason": "..."}.
// Injected by the caller (main.go) to avoid circular dependency on provider.
type PromptEvaluator func(ctx context.Context, prompt string, model string) (ok bool, reason string, err error)

// AsyncRewakeCallback is called when an asyncRewake hook finishes with exit code 2.
// The agent loop should inject a blocking error message and continue.
type AsyncRewakeCallback func(hookName string, blockingError string)

// Runner executes hooks. 对标 hooks.ts 中的 executeHooks 逻辑。
type Runner struct {
	Config   HooksConfig
	WorkDir  string
	Shell    string // "auto" / "bash" / "powershell" / "cmd"

	// PromptEval is called for type=prompt hooks. nil = prompt hooks disabled.
	PromptEval PromptEvaluator

	// OnAsyncRewake is called when an asyncRewake hook fires (exit code 2).
	// nil = asyncRewake hooks run but rewake is ignored.
	OnAsyncRewake AsyncRewakeCallback

	// mu protects Config for once-hook removal
	mu sync.Mutex
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

// RunPostToolUseFailure executes PostToolUseFailure hooks.
// 对标 hooks.ts:3492 executePostToolUseFailureHooks
func (r *Runner) RunPostToolUseFailure(ctx context.Context, toolName, toolUseID string, toolInput json.RawMessage, errMsg string, isInterrupt bool) (*AggregatedResult, error) {
	boolPtr := &isInterrupt
	input := HookInput{
		HookEventName: string(PostToolUseFailure),
		ToolName:      toolName,
		ToolInput:     json.RawMessage(toolInput),
		ToolUseID:     toolUseID,
		Error:         errMsg,
		IsInterrupt:   boolPtr,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, PostToolUseFailure, toolName, input)
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

// RunNotification executes Notification hooks.
// 对标 hooks.ts:3570 executeNotificationHooks
func (r *Runner) RunNotification(ctx context.Context, message, title, notificationType string) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName:    string(Notification),
		Message:          message,
		Title:            title,
		NotificationType: notificationType,
		CWD:              r.WorkDir,
	}
	return r.run(ctx, Notification, notificationType, input)
}

// RunSessionStart executes SessionStart hooks.
func (r *Runner) RunSessionStart(ctx context.Context) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(SessionStart),
		CWD:           r.WorkDir,
	}
	return r.run(ctx, SessionStart, "", input)
}

// RunSessionEnd executes SessionEnd hooks.
func (r *Runner) RunSessionEnd(ctx context.Context) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(SessionEnd),
		CWD:           r.WorkDir,
	}
	return r.run(ctx, SessionEnd, "", input)
}

// RunSubagentStart executes SubagentStart hooks.
func (r *Runner) RunSubagentStart(ctx context.Context, agentType string) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(SubagentStart),
		AgentType:     agentType,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, SubagentStart, agentType, input)
}

// RunSubagentStop executes SubagentStop hooks.
func (r *Runner) RunSubagentStop(ctx context.Context, agentType string) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(SubagentStop),
		AgentType:     agentType,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, SubagentStop, agentType, input)
}

// RunPreCompact executes PreCompact hooks.
func (r *Runner) RunPreCompact(ctx context.Context) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(PreCompact),
		CWD:           r.WorkDir,
	}
	return r.run(ctx, PreCompact, "", input)
}

// RunPostCompact executes PostCompact hooks.
func (r *Runner) RunPostCompact(ctx context.Context) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(PostCompact),
		CWD:           r.WorkDir,
	}
	return r.run(ctx, PostCompact, "", input)
}

// RunPermissionDenied executes PermissionDenied hooks.
// 对标 hooks.ts:3529 executePermissionDeniedHooks
func (r *Runner) RunPermissionDenied(ctx context.Context, toolName, toolUseID string, toolInput json.RawMessage, reason string) (*AggregatedResult, error) {
	input := HookInput{
		HookEventName: string(PermissionDenied),
		ToolName:      toolName,
		ToolInput:     json.RawMessage(toolInput),
		ToolUseID:     toolUseID,
		DenyReason:    reason,
		CWD:           r.WorkDir,
	}
	return r.run(ctx, PermissionDenied, toolName, input)
}

// run is the core execution logic shared by all hook event types.
func (r *Runner) run(ctx context.Context, event HookEvent, matchQuery string, input HookInput) (*AggregatedResult, error) {
	hooks, matcherIndices := r.getMatchingHooks(event, matchQuery, input)
	if len(hooks) == 0 {
		return nil, nil
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal hook input: %w", err)
	}

	agg := &AggregatedResult{}

	// Track once-hooks to remove after execution
	var onceIndices []onceIndex

	for i, h := range hooks {
		if h.Once {
			onceIndices = append(onceIndices, matcherIndices[i])
		}

		// asyncRewake: run in background, fire callback on exit code 2
		if h.AsyncRewake {
			go func(hook HookConfig, hookName string) {
				execCtx, cancel := context.WithTimeout(context.Background(), r.hookTimeout(hook))
				defer cancel()
				result := r.execAndProcess(execCtx, hook, inputJSON)
				if result != nil && result.Decision == "block" && r.OnAsyncRewake != nil {
					errMsg := result.BlockingError
					if errMsg == "" {
						errMsg = "asyncRewake hook triggered"
					}
					r.OnAsyncRewake(hookName, errMsg)
				}
			}(h, string(event)+":"+matchQuery)
			continue
		}

		if h.Async {
			// 后台执行，不等待结果
			go func(hook HookConfig) {
				execCtx, cancel := context.WithTimeout(context.Background(), r.hookTimeout(hook))
				defer cancel()
				r.execAndProcess(execCtx, hook, inputJSON)
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

	// Remove once-hooks after execution
	if len(onceIndices) > 0 {
		r.removeOnceHooks(event, onceIndices)
	}

	return agg, nil
}

// onceIndex tracks the position of a hook for removal.
type onceIndex struct {
	matcherIdx int
	hookIdx    int
}

// getMatchingHooks returns hooks that match the event and query, plus their indices for once-removal.
// 对标 hooks.ts:1603 getMatchingHooks
func (r *Runner) getMatchingHooks(event HookEvent, matchQuery string, input HookInput) ([]HookConfig, []onceIndex) {
	r.mu.Lock()
	defer r.mu.Unlock()

	matchers, ok := r.Config[event]
	if !ok {
		return nil, nil
	}

	var result []HookConfig
	var indices []onceIndex
	for mi, m := range matchers {
		// matcher 过滤: 空 matcher 匹配所有，否则按 tool_name 匹配
		if m.Matcher != "" && matchQuery != "" && !matchesPattern(matchQuery, m.Matcher) {
			continue
		}

		for hi, h := range m.Hooks {
			// 支持 command / http / prompt 类型
			switch h.Type {
			case "command", "http", "prompt":
				// ok
			default:
				continue
			}
			// if 条件过滤
			if h.If != "" && !checkIfCondition(h.If, input) {
				continue
			}
			result = append(result, h)
			indices = append(indices, onceIndex{matcherIdx: mi, hookIdx: hi})
		}
	}
	return result, indices
}

// removeOnceHooks removes hooks marked with once=true after execution.
func (r *Runner) removeOnceHooks(event HookEvent, indices []onceIndex) {
	r.mu.Lock()
	defer r.mu.Unlock()

	matchers, ok := r.Config[event]
	if !ok {
		return
	}

	// Remove in reverse order to preserve indices
	for i := len(indices) - 1; i >= 0; i-- {
		idx := indices[i]
		if idx.matcherIdx < len(matchers) && idx.hookIdx < len(matchers[idx.matcherIdx].Hooks) {
			hooks := matchers[idx.matcherIdx].Hooks
			matchers[idx.matcherIdx].Hooks = append(hooks[:idx.hookIdx], hooks[idx.hookIdx+1:]...)
		}
	}

	r.Config[event] = matchers
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

// execAndProcess executes a single hook and processes its output.
// Dispatches to command, http, or prompt based on hook type.
func (r *Runner) execAndProcess(ctx context.Context, h HookConfig, inputJSON []byte) *HookResult {
	switch h.Type {
	case "http":
		return r.execHTTPHook(ctx, h, inputJSON)
	case "prompt":
		return r.execPromptHook(ctx, h, inputJSON)
	default: // "command"
		return r.execCommandHook(ctx, h, inputJSON)
	}
}

// --- command hook execution ---

// execCommandHook executes a shell command hook and processes its output.
func (r *Runner) execCommandHook(ctx context.Context, h HookConfig, inputJSON []byte) *HookResult {
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
			if jsonOut.IsAsync() {
				return result // async response — treat as success
			}
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
func (r *Runner) execCommand(ctx context.Context, h HookConfig, inputJSON []byte) (stdout, stderr string, exitCode int, err error) {
	// Per-hook shell override, fallback to runner default
	shell := r.resolveShellForHook(h)
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

// resolveShellForHook returns the shell for a specific hook (per-hook override or runner default).
func (r *Runner) resolveShellForHook(h HookConfig) string {
	if h.Shell != "" {
		switch h.Shell {
		case "powershell", "bash", "cmd":
			return h.Shell
		}
	}
	return r.resolveShell()
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

// --- HTTP hook execution ---
// 对标 execHttpHook.ts

func (r *Runner) execHTTPHook(ctx context.Context, h HookConfig, inputJSON []byte) *HookResult {
	timeout := r.hookTimeout(h)
	if h.Timeout == 0 {
		timeout = time.Duration(defaultHTTPTimeoutSec) * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(execCtx, "POST", h.URL, bytes.NewReader(inputJSON))
	if err != nil {
		return &HookResult{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("http hook: invalid URL %q: %v", h.URL, err),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return &HookResult{
				ExitCode:      -1,
				Stderr:        fmt.Sprintf("http hook timed out after %v", timeout),
				BlockingError: fmt.Sprintf("http hook timed out after %v", timeout),
			}
		}
		return &HookResult{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("http hook error: %v", err),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HookResult{
			ExitCode: resp.StatusCode,
			Stderr:   fmt.Sprintf("HTTP %d from %s", resp.StatusCode, h.URL),
			Stdout:   bodyStr,
		}
	}

	// Parse JSON response (HTTP hooks must return JSON)
	jsonOut, _, _ := parseHookOutput(bodyStr)
	if jsonOut == nil {
		// No valid JSON — treat as success with plain text
		return &HookResult{
			ExitCode: 0,
			Stdout:   bodyStr,
		}
	}

	if jsonOut.IsAsync() {
		return &HookResult{ExitCode: 0} // async — success
	}

	return processHookJSON(jsonOut, h.URL)
}

// --- Prompt hook execution ---
// 对标 execPromptHook.ts — 调用 LLM 评估 prompt

func (r *Runner) execPromptHook(ctx context.Context, h HookConfig, inputJSON []byte) *HookResult {
	if r.PromptEval == nil {
		return &HookResult{
			ExitCode: -1,
			Stderr:   "prompt hook: PromptEvaluator not configured",
		}
	}

	timeout := r.hookTimeout(h)
	if h.Timeout == 0 {
		timeout = 30 * time.Second // prompt hooks default 30s
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Replace $ARGUMENTS with the JSON input
	prompt := strings.ReplaceAll(h.Prompt, "$ARGUMENTS", string(inputJSON))

	ok, reason, err := r.PromptEval(execCtx, prompt, h.Model)
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return &HookResult{
				ExitCode: -1,
				Stderr:   fmt.Sprintf("prompt hook timed out after %v", timeout),
			}
		}
		return &HookResult{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("prompt hook error: %v", err),
		}
	}

	if !ok {
		return &HookResult{
			Decision:        "block",
			BlockingError:   fmt.Sprintf("Prompt hook condition not met: %s", reason),
			PreventContinue: true,
			StopReason:      reason,
		}
	}

	return &HookResult{ExitCode: 0} // condition met — success
}

// --- shared helpers ---

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
