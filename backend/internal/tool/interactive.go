package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TodoItem represents a single todo item.
type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`     // "pending" | "in_progress" | "completed"
	ActiveForm string `json:"activeForm"` // present-continuous form shown during execution
}

// TodoWriteTool manages the session task checklist.
// Maps to Claude Code's TodoWriteTool.ts.
// Todos are stored in-memory per session via the registry's shared state.
type TodoWriteTool struct {
	// todos is the current list, shared across calls in the same agent run.
	todos []TodoItem
}

func (t *TodoWriteTool) Name() string { return "TodoWrite" }
func (t *TodoWriteTool) Description() string {
	return "Use this tool to create and manage a structured task list for the current session. " +
		"Helps track progress and organize complex tasks. " +
		"Mark tasks as in_progress before starting, completed immediately after finishing. " +
		"Only one task should be in_progress at a time."
}
func (t *TodoWriteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type":        "array",
				"description": "The updated todo list",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content":    map[string]interface{}{"type": "string", "description": "Imperative form: what needs to be done"},
						"status":     map[string]interface{}{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
						"activeForm": map[string]interface{}{"type": "string", "description": "Present-continuous form shown during execution"},
					},
					"required": []string{"content", "status", "activeForm"},
				},
			},
		},
		"required": []string{"todos"},
	}
}
func (t *TodoWriteTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *TodoWriteTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *TodoWriteTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *TodoWriteTool) IsFileEdit(_ json.RawMessage) bool        { return false }
func (t *TodoWriteTool) PermissionDescription(_ json.RawMessage) string {
	return "Update task list"
}

func (t *TodoWriteTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var req struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return &Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	t.todos = req.Todos

	return &Result{
		Content:  "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable",
		Metadata: map[string]string{"type": "todo_write"},
	}, nil
}

// GetTodos returns the current todo list (for display in REPL/API).
func (t *TodoWriteTool) GetTodos() []TodoItem {
	return t.todos
}

// AskUserQuestionTool lets the LLM ask the user a question and wait for the answer.
// Maps to Claude Code's AskUserQuestionTool.
// In CLI mode, questions are printed and answers read from stdin via PermissionCallback.
type AskUserQuestionTool struct {
	// Callback is called with the formatted question; returns the user's answer.
	Callback func(question string) (string, error)
}

func (t *AskUserQuestionTool) Name() string { return "AskUserQuestion" }
func (t *AskUserQuestionTool) Description() string {
	return "Use this tool when you need to ask the user questions during execution. " +
		"Allows you to: gather user preferences or requirements, clarify ambiguous instructions, " +
		"get decisions on implementation choices, or offer choices to the user. " +
		"In plan mode, use this to clarify requirements BEFORE finalizing your plan."
}
func (t *AskUserQuestionTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questions": map[string]interface{}{
				"type":        "array",
				"description": "Questions to ask the user (1-4 questions)",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"question": map[string]interface{}{
							"type":        "string",
							"description": "The question to ask",
						},
						"header": map[string]interface{}{
							"type":        "string",
							"description": "Short label (max 12 chars)",
						},
						"options": map[string]interface{}{
							"type":        "array",
							"description": "Available choices (2-4 options)",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"label":       map[string]interface{}{"type": "string"},
									"description": map[string]interface{}{"type": "string"},
								},
								"required": []string{"label", "description"},
							},
						},
						"multiSelect": map[string]interface{}{
							"type":        "boolean",
							"description": "Allow multiple selections",
						},
					},
					"required": []string{"question", "header", "options", "multiSelect"},
				},
			},
		},
		"required": []string{"questions"},
	}
}
func (t *AskUserQuestionTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *AskUserQuestionTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *AskUserQuestionTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *AskUserQuestionTool) IsFileEdit(_ json.RawMessage) bool        { return false }
func (t *AskUserQuestionTool) PermissionDescription(_ json.RawMessage) string {
	return "Ask user a question"
}

func (t *AskUserQuestionTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var req struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return &Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	if t.Callback == nil {
		return &Result{Content: "AskUserQuestion: no callback configured", IsError: true}, nil
	}

	var answers []string
	for _, q := range req.Questions {
		// Format the question for CLI display
		var sb strings.Builder
		sb.WriteString(q.Question)
		sb.WriteString("\n")
		for i, opt := range q.Options {
			sb.WriteString(fmt.Sprintf("  %d. %s", i+1, opt.Label))
			if opt.Description != "" {
				sb.WriteString(fmt.Sprintf(" — %s", opt.Description))
			}
			sb.WriteString("\n")
		}
		if q.MultiSelect {
			sb.WriteString("(Enter numbers separated by commas, or type your answer)")
		} else {
			sb.WriteString("(Enter number or type your answer)")
		}

		answer, err := t.Callback(sb.String())
		if err != nil {
			return &Result{Content: fmt.Sprintf("error reading answer: %v", err), IsError: true}, nil
		}

		// Try to resolve numbered answer to label
		resolved := resolveAnswer(answer, q.Options)
		answers = append(answers, fmt.Sprintf("Q: %s\nA: %s", q.Question, resolved))
	}

	return &Result{
		Content:  strings.Join(answers, "\n\n"),
		Metadata: map[string]string{"type": "ask_user_question"},
	}, nil
}

func resolveAnswer(answer string, options []struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}) string {
	answer = strings.TrimSpace(answer)
	// Try numeric selection
	var idx int
	if _, err := fmt.Sscanf(answer, "%d", &idx); err == nil {
		if idx >= 1 && idx <= len(options) {
			return options[idx-1].Label
		}
	}
	return answer
}
