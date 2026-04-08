// Package hook implements the hooks system for edoc-edualc.
// 对标 Claude Code 的 hooks 系统（src/utils/hooks.ts），支持在工具执行前后
// 自动运行 shell 命令，实现自动化审查、日志记录、权限控制等功能。
//
// Phase 1 支持:
//   - 事件: PreToolUse, PostToolUse, UserPromptSubmit, Stop
//   - Hook 类型: command（shell 命令执行）
//   - matcher 匹配、if 条件过滤、JSON stdin/stdout 协议、exit code 语义
package hook

import "encoding/json"

// HookEvent 枚举，对标 src/entrypoints/agentSdkTypes.ts:HOOK_EVENTS
type HookEvent string

const (
	PreToolUse       HookEvent = "PreToolUse"
	PostToolUse      HookEvent = "PostToolUse"
	UserPromptSubmit HookEvent = "UserPromptSubmit"
	Stop             HookEvent = "Stop"
)

// HookConfig 单个 hook 定义，对标 BashCommandHookSchema
type HookConfig struct {
	Type          string `json:"type"`                      // "command"
	Command       string `json:"command"`                   // shell 命令
	If            string `json:"if,omitempty"`              // permission rule 语法过滤，如 "Bash(git *)"
	Timeout       int    `json:"timeout,omitempty"`         // 超时秒数
	Async         bool   `json:"async,omitempty"`           // 后台执行
	StatusMessage string `json:"statusMessage,omitempty"`   // 自定义状态消息
}

// HookMatcher matcher + hooks 数组，对标 HookMatcherSchema
type HookMatcher struct {
	Matcher string       `json:"matcher,omitempty"` // 匹配模式（如 tool name "Bash"）
	Hooks   []HookConfig `json:"hooks"`
}

// HooksConfig 事件 → matcher 数组映射，对标 HooksSchema
type HooksConfig map[HookEvent][]HookMatcher

// HookInput 传给 hook 命令的 JSON（通过 stdin），对标 PreToolUseHookInput 等
type HookInput struct {
	HookEventName string      `json:"hook_event_name"`
	ToolName      string      `json:"tool_name,omitempty"`
	ToolInput     interface{} `json:"tool_input,omitempty"`
	ToolUseID     string      `json:"tool_use_id,omitempty"`
	ToolResponse  interface{} `json:"tool_response,omitempty"`
	SessionID     string      `json:"session_id,omitempty"`
	CWD           string      `json:"cwd,omitempty"`
	// UserPromptSubmit
	Prompt string `json:"prompt,omitempty"`
}

// HookJSONOutput hook 命令的 JSON stdout 输出，对标 syncHookResponseSchema
type HookJSONOutput struct {
	Decision          string `json:"decision,omitempty"`      // "approve" / "block"
	Reason            string `json:"reason,omitempty"`
	Continue          *bool  `json:"continue,omitempty"`      // false = prevent continuation
	StopReason        string `json:"stopReason,omitempty"`
	SuppressOutput    bool   `json:"suppressOutput,omitempty"`
	SystemMessage     string `json:"systemMessage,omitempty"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput 事件特定的输出字段
type HookSpecificOutput struct {
	HookEventName          string                 `json:"hookEventName"`
	PermissionDecision     string                 `json:"permissionDecision,omitempty"` // "allow" / "deny" / "ask"
	PermissionDecisionReason string               `json:"permissionDecisionReason,omitempty"`
	UpdatedInput           map[string]interface{} `json:"updatedInput,omitempty"`
	AdditionalContext      string                 `json:"additionalContext,omitempty"`
}

// HookResult 单个 hook 执行结果
type HookResult struct {
	Decision          string                 // "approve" / "block" / ""
	Reason            string
	BlockingError     string
	AdditionalContext string
	UpdatedInput      map[string]interface{}
	PreventContinue   bool
	StopReason        string
	Stdout            string
	Stderr            string
	ExitCode          int
}

// AggregatedResult 多个 hook 聚合结果
type AggregatedResult struct {
	Decision          string   // 最终决策: "approve" / "block" / ""
	BlockingErrors    []string
	AdditionalContext []string
	UpdatedInput      map[string]interface{}
	PreventContinue   bool
	StopReason        string
}

// parseHookOutput 解析 hook 命令的 stdout，尝试 JSON 解析。
// 对标 hooks.ts:399 parseHookOutput
func parseHookOutput(stdout string) (*HookJSONOutput, string, error) {
	trimmed := trimLeadingWhitespace(stdout)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, stdout, nil // plain text
	}
	var out HookJSONOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, stdout, nil // JSON 解析失败，当作 plain text
	}
	return &out, "", nil
}

// processHookJSON 处理 JSON 输出，提取 decision/updatedInput/additionalContext。
// 对标 hooks.ts:489 processHookJSONOutput
func processHookJSON(out *HookJSONOutput, command string) *HookResult {
	r := &HookResult{}

	if out.Continue != nil && !*out.Continue {
		r.PreventContinue = true
		r.StopReason = out.StopReason
	}

	switch out.Decision {
	case "approve":
		r.Decision = "approve"
	case "block":
		r.Decision = "block"
		r.BlockingError = out.Reason
		if r.BlockingError == "" {
			r.BlockingError = "Blocked by hook"
		}
	}

	r.Reason = out.Reason

	// hookSpecificOutput
	if hso := out.HookSpecificOutput; hso != nil {
		switch hso.PermissionDecision {
		case "allow":
			r.Decision = "approve"
		case "deny":
			r.Decision = "block"
			reason := hso.PermissionDecisionReason
			if reason == "" {
				reason = out.Reason
			}
			if reason == "" {
				reason = "Blocked by hook"
			}
			r.BlockingError = reason
		case "ask":
			r.Decision = "ask"
		}
		if hso.PermissionDecisionReason != "" {
			r.Reason = hso.PermissionDecisionReason
		}
		if hso.UpdatedInput != nil {
			r.UpdatedInput = hso.UpdatedInput
		}
		if hso.AdditionalContext != "" {
			r.AdditionalContext = hso.AdditionalContext
		}
	}

	return r
}

func trimLeadingWhitespace(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			return s[i:]
		}
	}
	return ""
}
