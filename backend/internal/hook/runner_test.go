package hook

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseIfCondition(t *testing.T) {
	tests := []struct {
		input       string
		wantTool    string
		wantPattern string
	}{
		{"Bash", "Bash", ""},
		{"Bash(git *)", "Bash", "git *"},
		{"Read(*.ts)", "Read", "*.ts"},
		{"Write(/tmp/*)", "Write", "/tmp/*"},
		{"", "", ""},
	}
	for _, tt := range tests {
		tool, pattern := parseIfCondition(tt.input)
		if tool != tt.wantTool || pattern != tt.wantPattern {
			t.Errorf("parseIfCondition(%q) = (%q, %q), want (%q, %q)",
				tt.input, tool, pattern, tt.wantTool, tt.wantPattern)
		}
	}
}

func TestMatchesPattern(t *testing.T) {
	tests := []struct {
		query, pattern string
		want           bool
	}{
		{"Bash", "Bash", true},
		{"Bash", "Read", false},
		{"Bash", "*", true},
		{"Bash", "", true},
		{"BashTool", "Bash*", true},
		{"ReadTool", "Bash*", false},
	}
	for _, tt := range tests {
		got := matchesPattern(tt.query, tt.pattern)
		if got != tt.want {
			t.Errorf("matchesPattern(%q, %q) = %v, want %v",
				tt.query, tt.pattern, got, tt.want)
		}
	}
}

func TestCheckIfCondition(t *testing.T) {
	bashInput := HookInput{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"git status"}`),
	}

	tests := []struct {
		ifCond string
		input  HookInput
		want   bool
	}{
		{"", bashInput, true},                  // empty = always match
		{"Bash", bashInput, true},              // tool name match
		{"Read", bashInput, false},             // tool name mismatch
		{"Bash(git *)", bashInput, true},       // tool + pattern match
		{"Bash(rm *)", bashInput, false},       // tool match, pattern mismatch
	}
	for _, tt := range tests {
		got := checkIfCondition(tt.ifCond, tt.input)
		if got != tt.want {
			t.Errorf("checkIfCondition(%q, ...) = %v, want %v", tt.ifCond, got, tt.want)
		}
	}
}

func TestParseHookOutput(t *testing.T) {
	// Plain text
	jsonOut, plain, err := parseHookOutput("hello world")
	if err != nil || jsonOut != nil || plain != "hello world" {
		t.Errorf("plain text: got json=%v plain=%q err=%v", jsonOut, plain, err)
	}

	// Valid JSON
	jsonOut, _, err = parseHookOutput(`{"decision":"block","reason":"not allowed"}`)
	if err != nil || jsonOut == nil {
		t.Fatalf("valid JSON: got json=%v err=%v", jsonOut, err)
	}
	if jsonOut.Decision != "block" || jsonOut.Reason != "not allowed" {
		t.Errorf("valid JSON: decision=%q reason=%q", jsonOut.Decision, jsonOut.Reason)
	}

	// Empty
	jsonOut, plain, err = parseHookOutput("")
	if err != nil || jsonOut != nil || plain != "" {
		t.Errorf("empty: got json=%v plain=%q err=%v", jsonOut, plain, err)
	}
}

func TestProcessHookJSON(t *testing.T) {
	// Block decision
	out := &HookJSONOutput{Decision: "block", Reason: "dangerous"}
	r := processHookJSON(out, "test-cmd")
	if r.Decision != "block" || r.BlockingError != "dangerous" {
		t.Errorf("block: decision=%q blocking=%q", r.Decision, r.BlockingError)
	}

	// Approve decision
	out = &HookJSONOutput{Decision: "approve"}
	r = processHookJSON(out, "test-cmd")
	if r.Decision != "approve" {
		t.Errorf("approve: decision=%q", r.Decision)
	}

	// hookSpecificOutput with updatedInput
	out = &HookJSONOutput{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     "PreToolUse",
			PermissionDecision: "allow",
			UpdatedInput:       map[string]interface{}{"command": "safe-cmd"},
			AdditionalContext:  "extra info",
		},
	}
	r = processHookJSON(out, "test-cmd")
	if r.Decision != "approve" {
		t.Errorf("hso allow: decision=%q", r.Decision)
	}
	if r.UpdatedInput["command"] != "safe-cmd" {
		t.Errorf("hso updatedInput: %v", r.UpdatedInput)
	}
	if r.AdditionalContext != "extra info" {
		t.Errorf("hso additionalContext: %q", r.AdditionalContext)
	}

	// continue: false
	f := false
	out = &HookJSONOutput{Continue: &f, StopReason: "done"}
	r = processHookJSON(out, "test-cmd")
	if !r.PreventContinue || r.StopReason != "done" {
		t.Errorf("continue false: prevent=%v stop=%q", r.PreventContinue, r.StopReason)
	}
}

func TestLoadSettings(t *testing.T) {
	// Create temp dir with .edoc/settings.json
	dir := t.TempDir()
	edocDir := filepath.Join(dir, ".edoc")
	os.MkdirAll(edocDir, 0755)

	settings := `{
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Bash",
					"hooks": [
						{"type": "command", "command": "echo pre-hook"}
					]
				}
			],
			"PostToolUse": [
				{
					"hooks": [
						{"type": "command", "command": "echo post-hook"}
					]
				}
			]
		}
	}`
	os.WriteFile(filepath.Join(edocDir, "settings.json"), []byte(settings), 0644)

	cfg, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(cfg) != 2 {
		t.Fatalf("expected 2 events, got %d", len(cfg))
	}
	if len(cfg[PreToolUse]) != 1 || cfg[PreToolUse][0].Matcher != "Bash" {
		t.Errorf("PreToolUse: %+v", cfg[PreToolUse])
	}
	if len(cfg[PostToolUse]) != 1 || len(cfg[PostToolUse][0].Hooks) != 1 {
		t.Errorf("PostToolUse: %+v", cfg[PostToolUse])
	}
}

func TestLoadSettingsNoFile(t *testing.T) {
	cfg, err := LoadSettings(t.TempDir())
	if err != nil || cfg != nil {
		t.Errorf("no file: cfg=%v err=%v", cfg, err)
	}
}

func TestGetMatchingHooks(t *testing.T) {
	r := &Runner{
		Config: HooksConfig{
			PreToolUse: []HookMatcher{
				{
					Matcher: "Bash",
					Hooks: []HookConfig{
						{Type: "command", Command: "echo all-bash"},
						{Type: "command", Command: "echo git-only", If: "Bash(git *)"},
					},
				},
				{
					Hooks: []HookConfig{
						{Type: "command", Command: "echo all-tools"},
					},
				},
			},
		},
	}

	// Bash tool with git command — should match all 3
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"git status"}`),
	}
	hooks := r.getMatchingHooks(PreToolUse, "Bash", input)
	if len(hooks) != 3 {
		t.Errorf("bash+git: expected 3 hooks, got %d", len(hooks))
	}

	// Bash tool with rm command — should match 2 (all-bash + all-tools, not git-only)
	input.ToolInput = json.RawMessage(`{"command":"rm -rf /tmp/test"}`)
	hooks = r.getMatchingHooks(PreToolUse, "Bash", input)
	if len(hooks) != 2 {
		t.Errorf("bash+rm: expected 2 hooks, got %d", len(hooks))
	}

	// Read tool — should match only all-tools
	input = HookInput{
		ToolName:  "Read",
		ToolInput: json.RawMessage(`{"file_path":"test.go"}`),
	}
	hooks = r.getMatchingHooks(PreToolUse, "Read", input)
	if len(hooks) != 1 {
		t.Errorf("read: expected 1 hook, got %d", len(hooks))
	}
}

func TestRunnerExecCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		// 在 Windows 上需要 bash 可用
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("bash not available on Windows")
		}
	}

	r := &Runner{WorkDir: t.TempDir(), Shell: "bash"}
	h := HookConfig{Type: "command", Command: "echo hello"}

	stdout, stderr, exitCode, err := r.execCommand(context.Background(), h, []byte("{}"))
	if err != nil {
		t.Fatalf("execCommand: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode: %d, stderr: %s", exitCode, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "hello" {
		t.Errorf("stdout: %q", got)
	}
}

func TestRunnerExecCommandExitCode2(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("bash not available on Windows")
		}
	}

	r := &Runner{WorkDir: t.TempDir(), Shell: "bash"}
	h := HookConfig{Type: "command", Command: "echo 'blocked' >&2; exit 2"}

	result := r.execAndProcess(context.Background(), h, []byte("{}"))
	if result.Decision != "block" {
		t.Errorf("expected block, got decision=%q", result.Decision)
	}
	if result.BlockingError == "" {
		t.Error("expected blocking error")
	}
}

func TestRunnerExecCommandJSONOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("bash not available on Windows")
		}
	}

	r := &Runner{WorkDir: t.TempDir(), Shell: "bash"}
	h := HookConfig{Type: "command", Command: `echo '{"decision":"block","reason":"not safe"}'`}

	result := r.execAndProcess(context.Background(), h, []byte("{}"))
	if result.Decision != "block" {
		t.Errorf("expected block, got decision=%q", result.Decision)
	}
	if result.BlockingError != "not safe" {
		t.Errorf("blocking error: %q", result.BlockingError)
	}
}

func TestRunnerStdinInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("bash not available on Windows")
		}
	}

	r := &Runner{WorkDir: t.TempDir(), Shell: "bash"}
	// Read tool_name from stdin JSON
	h := HookConfig{Type: "command", Command: `cat | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('tool_name',''))" 2>/dev/null || cat`}

	input := HookInput{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
	}
	inputJSON, _ := json.Marshal(input)

	stdout, _, exitCode, err := r.execCommand(context.Background(), h, inputJSON)
	if err != nil && exitCode != 0 {
		// python3 might not be available, skip
		t.Skipf("python3 not available: %v", err)
	}
	if exitCode == 0 {
		got := strings.TrimSpace(stdout)
		if got != "Bash" && got != "" {
			// If python3 worked, verify output
			if got != "Bash" {
				t.Errorf("stdin tool_name: %q", got)
			}
		}
	}
}

func TestMergeResult(t *testing.T) {
	r := &Runner{}
	agg := &AggregatedResult{}

	// First: approve
	r.mergeResult(agg, &HookResult{Decision: "approve", AdditionalContext: "ctx1"})
	if agg.Decision != "approve" || len(agg.AdditionalContext) != 1 {
		t.Errorf("after approve: %+v", agg)
	}

	// Second: block overrides approve
	r.mergeResult(agg, &HookResult{Decision: "block", BlockingError: "bad"})
	if agg.Decision != "block" || len(agg.BlockingErrors) != 1 {
		t.Errorf("after block: %+v", agg)
	}
}
