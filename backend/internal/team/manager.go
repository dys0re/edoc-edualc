package team

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/agent"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/tool"
)

// Manager implements tool.TeamManager. Manages team lifecycle and teammate goroutines.
// 对标 Claude Code 的 teamHelpers + teammateMailbox + InProcessBackend。
type Manager struct {
	mu        sync.RWMutex
	teams     map[string]*Team               // keyed by team name
	leadInbox chan tool.MailboxMessage        // cap 32
	parentCfg agent.Config                   // base config for deriving teammate configs
}

// NewManager creates a team manager. The parentCfg is used as the base for
// deriving teammate agent configs (inheriting Provider, Registry, etc.).
func NewManager(parentCfg agent.Config) *Manager {
	return &Manager{
		teams:     make(map[string]*Team),
		leadInbox: make(chan tool.MailboxMessage, 32),
		parentCfg: parentCfg,
	}
}

// LeadInbox returns the channel for delivering messages to the team lead.
func (m *Manager) LeadInbox() <-chan tool.MailboxMessage {
	return m.leadInbox
}

// CreateTeam creates a new team with the given name and description.
// 对标 TeamCreateTool。
func (m *Manager) CreateTeam(name, description string) (*tool.TeamBrief, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("team name is required")
	}
	if _, exists := m.teams[name]; exists {
		return nil, fmt.Errorf("team %q already exists", name)
	}

	leadID := "team-lead"
	t := &Team{
		Name:        name,
		Description: description,
		CreatedAt:   time.Now(),
		LeadID:      leadID,
		Members: map[string]*Member{
			leadID: {
				AgentID:  leadID,
				Name:     leadID,
				IsActive: true,
				IsLead:   true,
				JoinedAt: time.Now(),
				// Lead reads from m.leadInbox, not a per-member inbox
			},
		},
	}
	m.teams[name] = t

	return m.teamToBrief(t), nil
}

// DeleteTeam disbands a team and cancels all teammate goroutines.
// 对标 TeamDeleteTool。
func (m *Manager) DeleteTeam(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.teams[name]
	if !ok {
		return fmt.Errorf("team %q not found", name)
	}

	// Cancel all non-lead members
	for _, member := range t.Members {
		if !member.IsLead && member.CancelFunc != nil {
			member.CancelFunc()
		}
	}

	// Wait for goroutines to exit (with timeout)
	for _, member := range t.Members {
		if !member.IsLead && member.Done != nil {
			select {
			case <-member.Done:
			case <-time.After(5 * time.Second):
				// Timeout — goroutine leaked
			}
		}
	}

	delete(m.teams, name)
	return nil
}

// SpawnTeammate creates an in-process teammate in the specified team.
// Returns the agent ID (e.g. "researcher@my-team").
// 对标 Claude Code 的 spawnInProcessTeammate。
func (m *Manager) SpawnTeammate(ctx context.Context, name, teamName, prompt, model string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name == "" {
		return "", fmt.Errorf("teammate name is required")
	}

	t, ok := m.teams[teamName]
	if !ok {
		return "", fmt.Errorf("team %q not found", name)
	}

	agentID := fmt.Sprintf("%s@%s", name, teamName)
	if _, exists := t.Members[agentID]; exists {
		return "", fmt.Errorf("teammate %q already exists in team %q", name, teamName)
	}

	childCtx, cancel := context.WithCancel(ctx)
	member := &Member{
		AgentID:    agentID,
		Name:       name,
		Model:      model,
		IsActive:   true,
		IsLead:     false,
		JoinedAt:   time.Now(),
		Inbox:      make(chan tool.MailboxMessage, 16),
		CancelFunc: cancel,
		Done:       make(chan struct{}),
	}
	t.Members[agentID] = member

	// Start teammate goroutine (release lock first would be ideal,
	// but the goroutine only reads from member.Inbox and doesn't re-lock)
	go m.runTeammate(childCtx, t, member, prompt, model)

	return agentID, nil
}

// SendMessage sends a direct message from one agent to another by name.
// 对标 Claude Code 的 handleMessage。
func (m *Manager) SendMessage(from, to, text, summary string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, t := range m.teams {
		for _, member := range t.Members {
			if member.Name == to && !member.IsLead {
				if member.Inbox == nil {
					return fmt.Errorf("teammate %q has no inbox", to)
				}
				msg := tool.MailboxMessage{
					From:      from,
					FromID:    from,
					Text:      text,
					Timestamp: time.Now(),
					Type:      "text",
					Summary:   summary,
				}
				select {
				case member.Inbox <- msg:
					return nil
				default:
					return fmt.Errorf("teammate %q inbox full", to)
				}
			}
		}
	}
	return fmt.Errorf("teammate %q not found in any team", to)
}

// Broadcast sends a message from one agent to all non-lead teammates in its team.
// 对标 Claude Code 的 handleBroadcast。
func (m *Manager) Broadcast(from, text, summary string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, t := range m.teams {
		for _, member := range t.Members {
			if member.IsLead || member.Name == from || member.Inbox == nil {
				continue
			}
			msg := tool.MailboxMessage{
				From:      from,
				FromID:    from,
				Text:      text,
				Timestamp: time.Now(),
				Type:      "text",
				Summary:   summary,
			}
			select {
			case member.Inbox <- msg:
			default:
				// Drop if full — best-effort broadcast
			}
		}
	}
	return nil
}

// GetTeamInfo returns team state by name.
func (m *Manager) GetTeamInfo(teamName string) (*tool.TeamBrief, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.teams[teamName]
	if !ok {
		return nil, fmt.Errorf("team %q not found", teamName)
	}
	brief := m.teamToBrief(t)
	return brief, nil
}

// ListTeams returns all teams.
func (m *Manager) ListTeams() []tool.TeamBrief {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]tool.TeamBrief, 0, len(m.teams))
	for _, t := range m.teams {
		result = append(result, *m.teamToBrief(t))
	}
	return result
}

// Close cancels all teammate goroutines in all teams.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.teams {
		for _, member := range t.Members {
			if !member.IsLead && member.CancelFunc != nil {
				member.CancelFunc()
			}
		}
		for _, member := range t.Members {
			if !member.IsLead && member.Done != nil {
				select {
				case <-member.Done:
				case <-time.After(5 * time.Second):
				}
			}
		}
	}
}

// --- Internal helpers ---

func (m *Manager) teamToBrief(t *Team) *tool.TeamBrief {
	brief := &tool.TeamBrief{
		Name:        t.Name,
		Description: t.Description,
		LeadID:      t.LeadID,
		Members:     make([]tool.MemberBrief, 0, len(t.Members)),
	}
	for _, member := range t.Members {
		brief.Members = append(brief.Members, tool.MemberBrief{
			AgentID:  member.AgentID,
			Name:     member.Name,
			IsActive: member.IsActive,
		})
	}
	return brief
}

// notifyLead sends a non-blocking message to the lead's inbox.
func (m *Manager) notifyLead(member *Member, msgType, text, summary string) {
	msg := tool.MailboxMessage{
		From:      member.Name,
		FromID:    member.AgentID,
		Text:      text,
		Timestamp: time.Now(),
		Type:      msgType,
		Summary:   summary,
	}
	select {
	case m.leadInbox <- msg:
	default:
		// Drop if lead inbox full — same pattern as task.Manager notification
	}
}

// runTeammate is the main goroutine for an in-process teammate.
// Implements a message pump: run agent loop → idle → wait for message → repeat.
// 对标 Claude Code 的 startInProcessTeammate + waitForNextPromptOrShutdown。
func (m *Manager) runTeammate(ctx context.Context, team *Team, member *Member, initialPrompt, model string) {
	defer close(member.Done)

	// Stamp agent identity on context so tools know who's calling
	ctx = tool.ContextWithAgentIdentity(ctx, tool.AgentIdentity{
		ID:   member.AgentID,
		Name: member.Name,
		Team: team.Name,
	})

	// Derive teammate config from parent
	cfg := m.deriveConfig(model, member.Name, team.Name)

	var history []message.Message
	prompt := initialPrompt

	for {
		member.IsActive = true

		// Run agent loop with current prompt
		history = append(history, message.NewUserMessage(prompt))

		for evt := range agent.RunWithMessages(ctx, cfg, history) {
			switch evt.Type {
			case "message_complete":
				if evt.Message != nil {
					history = append(history, *evt.Message)
				}
			case "error":
				m.notifyLead(member, "error",
					fmt.Sprintf("teammate %s error: %v", member.Name, evt.Error),
					fmt.Sprintf("teammate %s error", member.Name))
				return
			case "max_turns_reached":
				// Agent hit max turns — still transition to idle
				m.notifyLead(member, "warning",
					fmt.Sprintf("teammate %s hit max turns", member.Name),
					"max turns reached")
			}
		}

		// Agent loop finished (turn_complete or max_turns) → idle
		member.IsActive = false
		m.notifyLead(member, "idle_notification",
			fmt.Sprintf("teammate %s is idle and waiting for instructions", member.Name),
			"teammate idle")

		// Wait for next message or shutdown
		select {
		case <-ctx.Done():
			return
		case msg := <-member.Inbox:
			if msg.Type == "shutdown_request" {
				// MVP: auto-approve shutdown
				return
			}
			prompt = formatTeammateMessage(msg)
		}
	}
}

// deriveConfig creates a teammate-specific agent.Config from the parent config.
// 对标 Claude Code 的 TEAMMATE_SYSTEM_PROMPT_ADDENDUM。
func (m *Manager) deriveConfig(model, name, teamName string) agent.Config {
	cfg := m.parentCfg // copy

	// Override model if specified
	if model != "" {
		cfg.Model = model
	}

	// Teammates get a modified system prompt with team context
	cfg.SystemPrompt = cfg.SystemPrompt + fmt.Sprintf(teammatePromptAddendum, name, teamName)

	// Teammates don't persist sessions
	cfg.SessionStore = nil
	cfg.SessionID = ""

	// Teammates don't use auto-compact for now
	cfg.AutoCompactThreshold = 0

	// Default max turns per conversation round
	cfg.MaxTurns = 10

	// MVP: teammates bypass permissions
	cfg.PermissionMode = tool.PermissionBypass
	cfg.PermissionCallback = nil

	// Teammates don't manage background tasks for now
	cfg.TaskNotifier = nil

	// Teammates don't read from a lead inbox
	cfg.TeamInbox = nil

	// Teammates don't use hooks or LSP for now
	cfg.HookRunner = nil
	cfg.LSPManager = nil

	// Clone registry: share read-only tools, exclude team management tools
	cfg.Registry = tool.NewRegistry()
	for _, t := range m.parentCfg.Registry.All() {
		switch t.Name() {
		case "TeamCreate", "TeamDelete":
			// Teammates cannot create/delete teams
			continue
		case "EnterPlanMode", "ExitPlanMode", "EnterWorktree", "ExitWorktree":
			// Teammates don't use plan mode or worktrees for now
			continue
		default:
			cfg.Registry.Register(t)
		}
	}

	return cfg
}

// formatTeammateMessage formats an incoming MailboxMessage as XML for injection as user message.
func formatTeammateMessage(msg tool.MailboxMessage) string {
	return fmt.Sprintf(
		"<teammate-message from=%q from_id=%q type=%q summary=%q>\n%s\n</teammate-message>",
		msg.From, msg.FromID, msg.Type, msg.Summary, msg.Text,
	)
}

const teammatePromptAddendum = `

You are teammate %q in team %q.

Key behaviors:
- You receive tasks from the team lead via messages.
- When you finish a task, report your results via SendMessage to the team lead.
- You can communicate with other teammates via SendMessage.
- Always use SendMessage to share results — your plain text output is only visible to the user, not to other agents.

Team communication:
- Use SendMessage(to="team-lead", message="...") to report to the lead.
- Use SendMessage(to="teammate-name", message="...") to message a peer.
- Use SendMessage(to="*", message="...") to broadcast to all teammates.
`
