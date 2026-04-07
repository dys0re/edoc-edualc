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

// DefaultRegistry creates a registry with all built-in tools.
func DefaultRegistry(workDir string) *Registry {
	r := NewRegistry()
	r.Register(NewBashTool(workDir, ShellAuto))
	r.Register(NewReadTool())
	r.Register(NewWriteTool())
	r.Register(NewGlobTool())
	r.Register(NewGrepTool())
	r.Register(NewEditTool())
	return r
}
