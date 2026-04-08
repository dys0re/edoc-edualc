package skill

import "sort"

// Registry holds loaded skills indexed by name.
type Registry struct {
	skills map[string]*Skill
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{skills: make(map[string]*Skill)}
}

// Load scans the given directories and populates the registry.
func Load(dirs []string) (*Registry, error) {
	skills, err := LoadDirs(dirs)
	if err != nil {
		return nil, err
	}
	r := NewRegistry()
	for _, s := range skills {
		r.skills[s.Name] = s
	}
	return r, nil
}

// Get returns the skill with the given name, or nil if not found.
func (r *Registry) Get(name string) *Skill {
	if r == nil {
		return nil
	}
	return r.skills[name]
}

// All returns all skills sorted by name.
func (r *Registry) All() []*Skill {
	if r == nil {
		return nil
	}
	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// Names returns all skill names sorted.
func (r *Registry) Names() []string {
	skills := r.All()
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}

// Len returns the number of loaded skills.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.skills)
}
