package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// TeamCreateTool creates a new team for multi-agent collaboration.
// 对标 Claude Code 的 TeamCreateTool。
type TeamCreateTool struct {
	Manager TeamManager
}

func (t *TeamCreateTool) Name() string { return "TeamCreate" }

func (t *TeamCreateTool) Description() string {
	return "Create a new team for multi-agent collaboration. You become the team lead and can then spawn teammates using the Agent tool with name and team_name parameters."
}

func (t *TeamCreateTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Name for the team (e.g. 'research-team', 'fix-bugs')",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Optional description of the team's purpose",
			},
		},
		"required": []string{"name"},
	}
}

type teamCreateInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (t *TeamCreateTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	if t.Manager == nil {
		return &Result{Content: "Error: Team system not available", IsError: true}, nil
	}

	var in teamCreateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.Name == "" {
		return &Result{Content: "Error: team name is required", IsError: true}, nil
	}

	brief, err := t.Manager.CreateTeam(in.Name, in.Description)
	if err != nil {
		return &Result{Content: err.Error(), IsError: true}, nil
	}

	// Format team info
	memberList := fmt.Sprintf("  Lead: %s", brief.LeadID)
	result := fmt.Sprintf("Team %q created.\n%s\n\nUse the Agent tool with name and team_name to spawn teammates.",
		brief.Name, memberList)

	return &Result{Content: result}, nil
}

func (t *TeamCreateTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *TeamCreateTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *TeamCreateTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *TeamCreateTool) PermissionDescription(_ json.RawMessage) string {
	return "Create a new team"
}
func (t *TeamCreateTool) IsFileEdit(_ json.RawMessage) bool { return false }
