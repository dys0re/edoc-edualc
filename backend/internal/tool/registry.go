package tool

import "fmt"

// Registry holds all registered tools. Maps to tools.ts.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("no such tool: %s", name)
	}
	return t, nil
}

func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// DefaultRegistry creates a registry with all built-in tools (no Agent tool).
// webFetchProvider may be nil — WebFetch will return raw markdown without summarization.
func DefaultRegistry(workDir string, webFetchProvider WebFetchProvider) *Registry {
	r := NewRegistry()
	r.Register(NewBashTool(workDir, ShellAuto))
	r.Register(NewReadTool())
	r.Register(NewWriteTool())
	r.Register(NewGlobTool())
	r.Register(NewGrepTool())
	r.Register(NewEditTool())
	r.Register(&WebFetchTool{Provider: webFetchProvider})
	return r
}

// DefaultRegistryWithAgent creates a registry with all built-in tools including Agent.
// The resolver is wired in by the caller (typically agent.NewSubagentResolver).
func DefaultRegistryWithAgent(workDir string, webFetchProvider WebFetchProvider, resolver AgentResolver) *Registry {
	r := DefaultRegistry(workDir, webFetchProvider)
	r.Register(&AgentTool{Resolver: resolver})
	return r
}
