package team

import (
	"context"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// Team represents a named group of agents working together.
// 对标 Claude Code 的 TeamFile。
type Team struct {
	Name        string
	Description string
	CreatedAt   time.Time
	LeadID      string                    // "team-lead"
	Members     map[string]*Member        // keyed by agent ID "name@team"
}

// Member represents a teammate (or lead) in a team.
type Member struct {
	AgentID    string                   // "researcher@my-team"
	Name       string                   // "researcher"
	Model      string
	IsActive   bool
	IsLead     bool
	JoinedAt   time.Time
	Inbox      chan tool.MailboxMessage // buffered cap 16
	CancelFunc context.CancelFunc
	Done       chan struct{}            // closed when goroutine exits
}
