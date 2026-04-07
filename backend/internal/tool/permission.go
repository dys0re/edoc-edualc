package tool

import (
	"encoding/json"
	"strings"
)

// PermissionMode controls how tool permissions are handled.
type PermissionMode string

const (
	PermissionDefault     PermissionMode = "default"      // Read-only auto-approve, write/bash need approval
	PermissionAcceptEdits PermissionMode = "accept-edits" // File edits auto-approve, only bash needs approval
	PermissionBypass      PermissionMode = "bypass"       // Skip all permission checks (--dangerously-skip-permissions)
	PermissionStrict      PermissionMode = "strict"       // All tools need approval
)

// PermissionDecision is the result of a permission check.
type PermissionDecision int

const (
	DecisionAllow PermissionDecision = iota // Tool is allowed to run
	DecisionDeny                             // Tool is denied (rule-based)
	DecisionAsk                              // User confirmation required
)

// PermissionCallback is called when user confirmation is needed.
// REPL implementations read from stdin; API implementations use SSE.
type PermissionCallback func(toolName string, description string) (bool, error)

// CheckPermission checks whether a tool invocation is allowed under the given mode and rules.
func CheckPermission(mode PermissionMode, allowRules []string, t Tool, input json.RawMessage) PermissionDecision {
	// 1. Check allow rules first — explicit rules always win
	if matchesAllowRule(allowRules, t.Name(), input) {
		return DecisionAllow
	}

	// 2. Bypass mode — skip all checks
	if mode == PermissionBypass {
		return DecisionAllow
	}

	// 3. Strict mode — everything needs approval
	if mode == PermissionStrict {
		return DecisionAsk
	}

	// 4. Accept-edits mode — only bash needs approval
	if mode == PermissionAcceptEdits {
		if t.Name() == "Bash" {
			return DecisionAsk
		}
		return DecisionAllow
	}

	// 5. Default mode — read-only auto-approve, write ops need approval
	if mode == PermissionDefault {
		if t.NeedsApproval(input) {
			return DecisionAsk
		}
		return DecisionAllow
	}

	return DecisionAsk
}

// matchesAllowRule checks if the tool+input matches any allow rule.
// Rule formats:
//
//	"ToolName"              — matches the entire tool (e.g. "Read")
//	"ToolName:pattern"      — matches tool + content pattern (e.g. "Bash:git *")
func matchesAllowRule(rules []string, toolName string, input json.RawMessage) bool {
	for _, rule := range rules {
		parts := strings.SplitN(rule, ":", 2)
		if parts[0] != toolName {
			continue
		}
		// Whole-tool rule (e.g. "Read")
		if len(parts) == 1 {
			return true
		}
		// Content-pattern rule (e.g. "Bash:git *")
		pattern := parts[1]
		content := extractContentForMatching(toolName, input)
		if matchPattern(pattern, content) {
			return true
		}
	}
	return false
}

// extractContentForMatching extracts the relevant content from tool input for rule matching.
func extractContentForMatching(toolName string, input json.RawMessage) string {
	switch toolName {
	case "Bash":
		var parsed struct{ Command string `json:"command"` }
		json.Unmarshal(input, &parsed)
		return parsed.Command
	case "Write":
		var parsed struct{ FilePath string `json:"file_path"` }
		json.Unmarshal(input, &parsed)
		return parsed.FilePath
	case "Edit":
		var parsed struct{ FilePath string `json:"file_path"` }
		json.Unmarshal(input, &parsed)
		return parsed.FilePath
	case "Read":
		var parsed struct{ FilePath string `json:"file_path"` }
		json.Unmarshal(input, &parsed)
		return parsed.FilePath
	default:
		return string(input)
	}
}

// matchPattern matches a simple glob pattern against content.
// Supports:
//   - "*" — matches everything
//   - "prefix*" — matches content starting with prefix
//   - "exact" — exact match
func matchPattern(pattern, content string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(content, prefix)
	}
	return content == pattern
}

// ParsePermissionMode converts a config string to PermissionMode.
func ParsePermissionMode(s string) PermissionMode {
	switch s {
	case "default":
		return PermissionDefault
	case "accept-edits":
		return PermissionAcceptEdits
	case "bypass":
		return PermissionBypass
	case "strict":
		return PermissionStrict
	default:
		return PermissionBypass
	}
}
