package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnterPlanModeTool switches the agent into plan-only exploration mode.
// Maps to Claude Code's EnterPlanModeTool.ts.
// No parameters needed — just sets the plan mode flag via Metadata.
type EnterPlanModeTool struct {
	// PlansDir is where plan files are stored. Set by buildAgentConfig.
	PlansDir string
}

func (t *EnterPlanModeTool) Name() string { return "EnterPlanMode" }
func (t *EnterPlanModeTool) Description() string {
	return "Use this tool proactively when you're about to start a non-trivial implementation task. " +
		"Transitions into plan mode where you can explore the codebase and design an implementation approach for user approval. " +
		"In plan mode: only read-only tools are allowed. Write your plan to the plan file, then call ExitPlanMode."
}
func (t *EnterPlanModeTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}
func (t *EnterPlanModeTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *EnterPlanModeTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *EnterPlanModeTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *EnterPlanModeTool) IsFileEdit(_ json.RawMessage) bool        { return false }
func (t *EnterPlanModeTool) PermissionDescription(_ json.RawMessage) string {
	return "Enter plan mode (read-only exploration)"
}

func (t *EnterPlanModeTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, error) {
	planFile := planFilePath(t.PlansDir)
	instructions := fmt.Sprintf(`Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.

In plan mode, you should:
1. Thoroughly explore the codebase to understand existing patterns
2. Identify similar features and architectural approaches
3. Consider multiple approaches and their trade-offs
4. Use AskUserQuestion if you need to clarify the approach
5. Design a concrete implementation strategy
6. Write your plan to: %s
7. When ready, call ExitPlanMode to present your plan for approval

Remember: DO NOT write or edit any files yet (except the plan file). This is a read-only exploration and planning phase.`, planFile)

	return &Result{
		Content:  instructions,
		Metadata: map[string]string{"type": "enter_plan_mode", "plan_file": planFile},
	}, nil
}

// ExitPlanModeTool reads the plan file and asks the user to approve it.
// Maps to Claude Code's ExitPlanModeV2Tool.ts.
type ExitPlanModeTool struct {
	PlansDir           string
	PermissionCallback PermissionCallback
}

func (t *ExitPlanModeTool) Name() string { return "ExitPlanMode" }
func (t *ExitPlanModeTool) Description() string {
	return "Use this tool ONLY when in plan mode and have finished writing your plan to the plan file and are ready for user approval. " +
		"Presents the plan to the user and waits for approval. " +
		"IMPORTANT: Only use this tool when the task requires planning the implementation steps of a task that requires writing code."
}
func (t *ExitPlanModeTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"allowedPrompts": map[string]interface{}{
				"type":        "array",
				"description": "Prompt-based permissions needed to implement the plan (e.g. 'run tests', 'install dependencies')",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool":   map[string]interface{}{"type": "string", "enum": []string{"Bash"}},
						"prompt": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}
}
func (t *ExitPlanModeTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *ExitPlanModeTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *ExitPlanModeTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *ExitPlanModeTool) IsFileEdit(_ json.RawMessage) bool        { return false }
func (t *ExitPlanModeTool) PermissionDescription(_ json.RawMessage) string {
	return "Exit plan mode and present plan for approval"
}

func (t *ExitPlanModeTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	planFile := planFilePath(t.PlansDir)

	// Read plan from disk
	planBytes, err := os.ReadFile(planFile)
	plan := ""
	if err == nil {
		plan = strings.TrimSpace(string(planBytes))
	}

	// Ask user for approval
	if t.PermissionCallback != nil {
		var preview string
		if plan != "" {
			lines := strings.Split(plan, "\n")
			if len(lines) > 10 {
				preview = strings.Join(lines[:10], "\n") + "\n..."
			} else {
				preview = plan
			}
		} else {
			preview = "(no plan file found)"
		}

		approved, cbErr := t.PermissionCallback("ExitPlanMode",
			fmt.Sprintf("Approve plan?\n\n%s", preview))
		if cbErr != nil || !approved {
			return &Result{
				Content: "Plan rejected by user. Continue refining your plan and call ExitPlanMode again when ready.",
				Metadata: map[string]string{"type": "exit_plan_mode_rejected"},
			}, nil
		}
	}

	// Build approval response
	var content string
	if plan == "" {
		content = "User has approved exiting plan mode. You can now proceed with implementation."
	} else {
		content = fmt.Sprintf(`User has approved your plan. You can now start coding.

Your plan has been saved to: %s
You can refer back to it if needed during implementation.

## Approved Plan:
%s`, planFile, plan)
	}

	return &Result{
		Content:  content,
		Metadata: map[string]string{"type": "exit_plan_mode"},
	}, nil
}

// planFilePath returns the path to the plan file for the current session.
// Uses PlansDir if set, otherwise ~/.edoc/plans/plan.md.
func planFilePath(plansDir string) string {
	if plansDir == "" {
		home, _ := os.UserHomeDir()
		plansDir = filepath.Join(home, ".edoc", "plans")
	}
	if err := os.MkdirAll(plansDir, 0755); err == nil {
		// ok
	}
	return filepath.Join(plansDir, "plan.md")
}
