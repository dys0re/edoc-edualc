package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// TeamDeleteTool disbands a team and stops all its teammates.
// 对标 Claude Code 的 TeamDeleteTool。
type TeamDeleteTool struct {
	Manager TeamManager
}

func (t *TeamDeleteTool) Name() string { return "TeamDelete" }

func (t *TeamDeleteTool) Description() string {
	return "Delete a team and stop all its teammates. All active teammates must be idle before deletion."
}

func (t *TeamDeleteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"team_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the team to delete",
			},
		},
		"required": []string{"team_name"},
	}
}

type teamDeleteInput struct {
	TeamName string `json:"team_name"`
}

func (t *TeamDeleteTool) Execute(ctx context.Context, input json.RawMessage) (*Result, error) {
	if t.Manager == nil {
		return &Result{Content: "Error: Team system not available", IsError: true}, nil
	}

	var in teamDeleteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &Result{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.TeamName == "" {
		return &Result{Content: "Error: team_name is required", IsError: true}, nil
	}

	// Check for active teammates
	brief, err := t.Manager.GetTeamInfo(in.TeamName)
	if err != nil {
		return &Result{Content: err.Error(), IsError: true}, nil
	}

	var activeMembers []string
	for _, m := range brief.Members {
		if m.IsActive && m.Name != "team-lead" {
			activeMembers = append(activeMembers, m.Name)
		}
	}
	if len(activeMembers) > 0 {
		return &Result{
			Content: fmt.Sprintf("Cannot delete team %q: active teammates: %s. Send shutdown requests first.", in.TeamName, activeMembers),
			IsError: true,
		}, nil
	}

	if err := t.Manager.DeleteTeam(in.TeamName); err != nil {
		return &Result{Content: err.Error(), IsError: true}, nil
	}

	return &Result{Content: fmt.Sprintf("Team %q deleted.", in.TeamName)}, nil
}

func (t *TeamDeleteTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *TeamDeleteTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *TeamDeleteTool) NeedsApproval(_ json.RawMessage) bool     { return true }
func (t *TeamDeleteTool) PermissionDescription(_ json.RawMessage) string {
	return "Delete a team"
}
func (t *TeamDeleteTool) IsFileEdit(_ json.RawMessage) bool { return false }
