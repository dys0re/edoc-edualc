package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dysorder/edoc-edualc/backend/internal/skill"
)

// SkillTool lets the LLM invoke user-defined skills from .claude/skills/*.md.
// Maps to Claude Code's SkillTool.
//
// Execution modes:
//   - inline: skill content is injected as a user message into the current
//     conversation; the LLM reads and follows it in the same turn.
//   - fork (future): skill runs in an isolated sub-agent.
type SkillTool struct {
	// Registry holds the loaded skills. Set at startup.
	Registry *skill.Registry
}

type skillInput struct {
	Skill string `json:"skill"`
	Args  string `json:"args,omitempty"`
}

func (t *SkillTool) Name() string { return "Skill" }

func (t *SkillTool) Description() string {
	return `Execute a skill within the main conversation.

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.

How to invoke:
- Use this tool with the skill name and optional arguments
- Examples:
  - skill: "commit" — invoke the commit skill
  - skill: "review-pr", args: "123" — invoke with arguments

Important:
- Available skills are listed in system-reminder messages in the conversation
- When a skill matches the user's request, invoke the Skill tool BEFORE generating any other response
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running`
}

func (t *SkillTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"skill": map[string]interface{}{
				"type":        "string",
				"description": `The skill name. E.g., "commit", "review-pr", or "pdf"`,
			},
			"args": map[string]interface{}{
				"type":        "string",
				"description": "Optional arguments for the skill",
			},
		},
		"required": []string{"skill"},
	}
}

func (t *SkillTool) Execute(_ context.Context, input json.RawMessage) (*Result, error) {
	var in skillInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	name := strings.TrimSpace(in.Skill)
	name = strings.TrimPrefix(name, "/") // tolerate leading slash

	if name == "" {
		return &Result{Content: "Error: skill name is required", IsError: true}, nil
	}

	if t.Registry == nil {
		return &Result{Content: "Error: skill registry not configured", IsError: true}, nil
	}

	s := t.Registry.Get(name)
	if s == nil {
		available := t.Registry.Names()
		if len(available) == 0 {
			return &Result{
				Content: fmt.Sprintf("Skill %q not found. No skills are currently loaded.", name),
				IsError: true,
			}, nil
		}
		return &Result{
			Content: fmt.Sprintf("Skill %q not found. Available skills: %s", name, strings.Join(available, ", ")),
			IsError: true,
		}, nil
	}

	// Inline execution: expand skill content as a <command-message> block.
	// The agent loop will inject this as a user message in the next turn,
	// and the LLM will follow the instructions in the skill content.
	content := expandSkill(s, in.Args)

	return &Result{
		Content: content,
		// Mark as command-message so the agent loop can handle it specially
		Metadata: map[string]string{"type": "skill_inline", "skill": name},
	}, nil
}

// expandSkill substitutes $ARGUMENTS in the skill content and returns the
// final text to inject. Maps to processSlashCommand.ts:substituteArguments.
func expandSkill(s *skill.Skill, args string) string {
	content := s.Content
	if args != "" {
		content = strings.ReplaceAll(content, "$ARGUMENTS", args)
	} else {
		content = strings.ReplaceAll(content, "$ARGUMENTS", "")
	}
	return strings.TrimSpace(content)
}

func (t *SkillTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *SkillTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *SkillTool) NeedsApproval(_ json.RawMessage) bool     { return false }
func (t *SkillTool) IsFileEdit(_ json.RawMessage) bool        { return false }

func (t *SkillTool) PermissionDescription(input json.RawMessage) string {
	var in skillInput
	json.Unmarshal(input, &in)
	if in.Skill != "" {
		return "Execute skill: " + in.Skill
	}
	return "Execute skill"
}
