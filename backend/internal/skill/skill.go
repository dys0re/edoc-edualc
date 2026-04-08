// Package skill implements the skill system: scanning .md files, parsing
// frontmatter, and providing the skill registry used by SkillTool.
// Maps to Claude Code's skills/loadSkillsDir.ts + commands.ts.
package skill

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a loaded skill from a .md file.
type Skill struct {
	// Name is the skill's invocation name (derived from filename, e.g. "commit")
	Name string
	// Description is shown to the LLM in the skill listing
	Description string
	// WhenToUse is an optional hint for when to invoke this skill
	WhenToUse string
	// Content is the full markdown body (after frontmatter)
	Content string
	// AllowedTools restricts which tools are available during skill execution
	AllowedTools []string
	// Model overrides the model for this skill (empty = inherit)
	Model string
	// FilePath is the source file path
	FilePath string
}

// Frontmatter fields we care about (YAML between --- delimiters).
type frontmatter struct {
	description   string
	whenToUse     string
	allowedTools  []string
	model         string
}

// LoadDir scans a directory for *.md files and returns parsed skills.
// Non-existent directories are silently skipped.
func LoadDir(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var skills []*Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := loadFile(path)
		if err != nil {
			// Skip unreadable files silently
			continue
		}
		skills = append(skills, s)
	}
	return skills, nil
}

// LoadDirs loads skills from multiple directories, deduplicating by name
// (first occurrence wins). Typical order: project > user > global.
func LoadDirs(dirs []string) ([]*Skill, error) {
	seen := make(map[string]bool)
	var all []*Skill
	for _, dir := range dirs {
		skills, err := LoadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, s := range skills {
			if !seen[s.Name] {
				seen[s.Name] = true
				all = append(all, s)
			}
		}
	}
	return all, nil
}

// loadFile parses a single .md skill file.
func loadFile(path string) (*Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fm, body := parseFrontmatter(f)

	name := skillName(path)
	desc := fm.description
	if desc == "" {
		// Fall back to first non-empty line of body
		desc = firstLine(body)
	}

	return &Skill{
		Name:         name,
		Description:  desc,
		WhenToUse:    fm.whenToUse,
		Content:      body,
		AllowedTools: fm.allowedTools,
		Model:        fm.model,
		FilePath:     path,
	}, nil
}

// skillName derives the invocation name from the file path.
// e.g. ".claude/skills/commit.md" → "commit"
//      ".claude/skills/review-pr.md" → "review-pr"
func skillName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".md")
}

// firstLine returns the first non-empty, non-heading line of text.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// parseFrontmatter reads YAML frontmatter between --- delimiters from r.
// Returns parsed fields and the remaining body content.
// Only handles the simple key: value pairs we need; no full YAML parser.
func parseFrontmatter(f *os.File) (frontmatter, string) {
	scanner := bufio.NewScanner(f)
	var fm frontmatter
	var bodyLines []string

	// Check for opening ---
	if !scanner.Scan() {
		return fm, ""
	}
	firstLine := strings.TrimSpace(scanner.Text())
	if firstLine != "---" {
		// No frontmatter — first line is body
		bodyLines = append(bodyLines, scanner.Text())
		for scanner.Scan() {
			bodyLines = append(bodyLines, scanner.Text())
		}
		return fm, strings.Join(bodyLines, "\n")
	}

	// Read frontmatter lines until closing ---
	inFrontmatter := true
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			inFrontmatter = false
			break
		}
		if inFrontmatter {
			parseFrontmatterLine(line, &fm)
		}
	}

	// Read remaining body
	for scanner.Scan() {
		bodyLines = append(bodyLines, scanner.Text())
	}
	return fm, strings.TrimSpace(strings.Join(bodyLines, "\n"))
}

// parseFrontmatterLine handles a single "key: value" line.
func parseFrontmatterLine(line string, fm *frontmatter) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])
	// Strip inline YAML quotes
	val = strings.Trim(val, `"'`)

	switch key {
	case "description":
		fm.description = val
	case "when_to_use", "whenToUse":
		fm.whenToUse = val
	case "model":
		fm.model = val
	case "allowed-tools", "allowedTools":
		// Comma-separated or YAML list on same line: "Bash, Read, Write"
		for _, t := range strings.Split(val, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				fm.allowedTools = append(fm.allowedTools, t)
			}
		}
	}
}
