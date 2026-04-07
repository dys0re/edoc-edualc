package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BuildSystemPrompt assembles the system prompt for the agent (无记忆版).
func BuildSystemPrompt(workDir string) string {
	return BuildSystemPromptWithMemory(workDir, "")
}

// BuildSystemPromptWithMemory assembles the system prompt with optional memory section.
// memorySection 为空则跳过记忆注入。
func BuildSystemPromptWithMemory(workDir string, memorySection string) string {
	var sb strings.Builder

	sb.WriteString("You are an AI assistant that helps users with software engineering tasks. ")
	sb.WriteString("You have access to tools for reading files, writing files, executing commands, and searching code.\n\n")

	sb.WriteString("# Environment\n")
	sb.WriteString(fmt.Sprintf("- Working directory: %s\n", workDir))
	sb.WriteString(fmt.Sprintf("- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))

	if isGitRepo(workDir) {
		sb.WriteString("- Git repository: yes\n")
	}

	sb.WriteString("\n# Tool Usage\n")
	sb.WriteString("- Use the Read tool to read files instead of cat/head/tail via Bash\n")
	sb.WriteString("- Use the Write tool to create files instead of echo/heredoc via Bash\n")
	sb.WriteString("- Use Glob to find files instead of find/ls via Bash\n")
	sb.WriteString("- Use Grep to search file contents instead of grep/rg via Bash\n")
	sb.WriteString("- Reserve Bash for commands that genuinely need shell execution\n")

	sb.WriteString("\n# Rules\n")
	sb.WriteString("- Read files before modifying them\n")
	sb.WriteString("- Be concise and direct\n")
	sb.WriteString("- Do not add features beyond what was asked\n")

	// 注入记忆
	if memorySection != "" {
		sb.WriteString(memorySection)
	}

	// Load CLAUDE.md if present
	claudeMd := loadClaudeMd(workDir)
	if claudeMd != "" {
		sb.WriteString("\n# Project Instructions (CLAUDE.md)\n")
		sb.WriteString(claudeMd)
		sb.WriteString("\n")
	}

	return sb.String()
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func loadClaudeMd(dir string) string {
	path := filepath.Join(dir, "CLAUDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
