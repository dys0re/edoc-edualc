package skill

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultDirs returns the standard skill search directories in priority order:
// project-level > user-level.
// Maps to Claude Code's getSkillsPath() for 'projectSettings' and 'userSettings'.
func DefaultDirs(workDir string) []string {
	var dirs []string

	// Project-level: .claude/skills/ relative to workDir
	dirs = append(dirs, filepath.Join(workDir, ".claude", "skills"))
	// Legacy: .claude/commands/ (backwards compat)
	dirs = append(dirs, filepath.Join(workDir, ".claude", "commands"))

	// User-level: ~/.claude/skills/
	home, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
		dirs = append(dirs, filepath.Join(home, ".claude", "commands"))
	}

	// Windows: also check APPDATA
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			dirs = append(dirs, filepath.Join(appdata, "Claude", "skills"))
		}
	}

	return dirs
}

// BuildSystemReminderSection builds the <system-reminder> block injected each
// turn to inform the LLM about available skills.
// Maps to Claude Code's constants/prompts.ts skill listing injection.
func BuildSystemReminderSection(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("The following skills are available for use with the Skill tool:\n\n")
	for _, s := range skills {
		line := "- " + s.Name
		desc := s.Description
		if s.WhenToUse != "" {
			desc = desc + " - " + s.WhenToUse
		}
		if desc != "" {
			line += ": " + desc
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\nWhen a skill matches the user's request, invoke it with the Skill tool BEFORE generating any other response.")
	return sb.String()
}
