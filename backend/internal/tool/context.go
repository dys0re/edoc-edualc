package tool

import "context"

// agentIDKey is the context key for agent identity.
type agentIDKey struct{}

// AgentIdentity identifies which agent is calling a tool.
// Set by team.Manager in teammate goroutines via ContextWithAgentIdentity.
// Defaults to {Name: "team-lead"} for the primary agent.
type AgentIdentity struct {
	ID   string // "researcher@my-team"
	Name string // "researcher"
	Team string // "my-team"
}

// ContextWithAgentIdentity stamps the context with the caller's agent identity.
func ContextWithAgentIdentity(ctx context.Context, id AgentIdentity) context.Context {
	return context.WithValue(ctx, agentIDKey{}, id)
}

// AgentIdentityFromContext returns the agent identity from the context.
// Returns {Name: "team-lead"} if no identity is set (i.e. the primary agent).
func AgentIdentityFromContext(ctx context.Context) AgentIdentity {
	if id, ok := ctx.Value(agentIDKey{}).(AgentIdentity); ok {
		return id
	}
	return AgentIdentity{Name: "team-lead"}
}
