package tool

import (
	"context"
	"time"
)

// TeamManager is implemented by team.Manager. Routes messages and manages team lifecycle.
// Follows the same decoupling pattern as TaskStarter/TaskOutputReader in task_ref.go.
// 对标 Claude Code 的 TeamCreateTool + TeammateMailbox。
type TeamManager interface {
	// CreateTeam creates a new team with the given name and description.
	// Returns a brief snapshot of the created team.
	CreateTeam(name, description string) (*TeamBrief, error)

	// DeleteTeam disbands a team. Fails if active teammates exist.
	DeleteTeam(name string) error

	// SpawnTeammate creates an in-process teammate (goroutine) in the specified team.
	// Returns the agent ID (e.g. "researcher@my-team").
	SpawnTeammate(ctx context.Context, name, teamName, prompt, model string) (string, error)

	// SendMessage sends a direct message from one agent to another by name.
	SendMessage(from, to, text, summary string) error

	// Broadcast sends a message from one agent to all teammates in its team.
	Broadcast(from, text, summary string) error

	// LeadInbox returns the channel for delivering messages to the team lead.
	// The agent loop reads this channel at the top of each iteration.
	LeadInbox() <-chan MailboxMessage

	// GetTeamInfo returns team state by name. Returns nil if not found.
	GetTeamInfo(teamName string) (*TeamBrief, error)

	// ListTeams returns all teams.
	ListTeams() []TeamBrief

	// Close cancels all teammate goroutines and cleans up resources.
	Close()
}

// TeamBrief is a snapshot of team state returned to tools.
type TeamBrief struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	LeadID      string        `json:"lead_id"`
	Members     []MemberBrief `json:"members"`
}

// MemberBrief is a summary of a teammate.
type MemberBrief struct {
	AgentID  string `json:"agent_id"` // "researcher@my-team"
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
}

// MailboxMessage is the unit of inter-agent communication.
// Sent over buffered channels (replaces file-based mailboxes from the TypeScript version).
type MailboxMessage struct {
	From      string    `json:"from"`       // sender display name
	FromID    string    `json:"from_id"`    // sender agent ID
	Text      string    `json:"text"`       // message body
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`       // "text", "idle_notification", "shutdown_request"
	Summary   string    `json:"summary"`    // 5-10 word preview for notification
}
