// Package hook implements the hooks system for edoc-edualc.
// 对标 Claude Code 的 hooks 系统（src/utils/hooks.ts），支持在工具执行前后
// 自动运行 shell 命令/HTTP 请求/LLM 评估，实现自动化审查、日志记录、权限控制等功能。
//
// 支持:
//   - 事件: PreToolUse, PostToolUse, PostToolUseFailure, UserPromptSubmit, Stop,
//     Notification, SessionStart, SessionEnd, SubagentStart, SubagentStop,
//     PreCompact, PostCompact, PermissionDenied
//   - Hook 类型: command（shell 命令）, http（HTTP POST）, prompt（LLM 评估）
//   - matcher 匹配、if 条件过滤、JSON stdin/stdout 协议、exit code 语义
//   - once 标志（执行一次后移除）、asyncRewake（后台执行完成后唤醒模型）
//   - per-hook shell 覆盖
package hook

import "encoding/json"

// HookEvent 枚举，对标 src/entrypoints/sdk/coreTypes.ts:HOOK_EVENTS
type HookEvent string

const (
	PreToolUse         HookEvent = "PreToolUse"
	PostToolUse        HookEvent = "PostToolUse"
	PostToolUseFailure HookEvent = "PostToolUseFailure"
	UserPromptSubmit   HookEvent = "UserPromptSubmit"
	Stop               HookEvent = "Stop"
	Notification       HookEvent = "Notification"
	SessionStart       HookEvent = "SessionStart"
	SessionEnd         HookEvent = "SessionEnd"
	SubagentStart      HookEvent = "SubagentStart"
	SubagentStop       HookEvent = "SubagentStop"
	PreCompact         HookEvent = "PreCompact"
	PostCompact        HookEvent = "PostCompact"
	PermissionDenied   HookEvent = "PermissionDenied"
)

// HookConfig 单个 hook 定义，对标 HookCommandSchema (discriminated union)
type HookConfig struct {
	Type          string            `json:"type"`                      // "command" / "http" / "prompt"
	Command       string            `json:"command,omitempty"`         // shell 命令 (type=command)
	URL           string            `json:"url,omitempty"`             // HTTP URL (type=http)
	Prompt        string            `json:"prompt,omitempty"`          // LLM prompt (type=prompt)
	Headers       map[string]string `json:"headers,omitempty"`         // HTTP headers (type=http)
	Model         string            `json:"model,omitempty"`           // LLM model (type=prompt)
	If            string            `json:"if,omitempty"`              // permission rule 语法过滤
	Shell         string            `json:"shell,omitempty"`           // per-hook shell 覆盖
	Timeout       int               `json:"timeout,omitempty"`         // 超时秒数
	Async         bool              `json:"async,omitempty"`           // 后台执行
	AsyncRewake   bool              `json:"asyncRewake,omitempty"`     // 后台执行，exit code 2 唤醒模型
	Once          bool              `json:"once,omitempty"`            // 执行一次后移除
	StatusMessage string            `json:"statusMessage,omitempty"`   // 自定义状态消息
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
	// PostToolUseFailure
	Error       string `json:"error,omitempty"`
	IsInterrupt *bool  `json:"is_interrupt,omitempty"`
	// Notification
	Message          string `json:"message,omitempty"`
	Title            string `json:"title,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
	// PermissionDenied
	DenyReason string `json:"reason,omitempty"`
	// SubagentStart/SubagentStop
	AgentType string `json:"agent_type,omitempty"`
}

// HookJSONOutput hook 命令的 JSON stdout 输出，对标 syncHookResponseSchema
type HookJSONOutput struct {
	// Async response: {"async": true} — hook runs in background
	AsyncFlag         *bool  `json:"async,omitempty"`
	// Sync response fields
	Decision          string `json:"decision,omitempty"`      // "approve" / "block"
	Reason            string `json:"reason,omitempty"`
	Continue          *bool  `json:"continue,omitempty"`      // false = prevent continuation
	StopReason        string `json:"stopReason,omitempty"`
	SuppressOutput    bool   `json:"suppressOutput,omitempty"`
	SystemMessage     string `json:"systemMessage,omitempty"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// IsAsync returns true if this is an async hook response.
func (o *HookJSONOutput) IsAsync() bool {
	return o.AsyncFlag != nil && *o.AsyncFlag
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
