package memory

import (
	"context"
	"fmt"
	"strings"
)

const maxIndexLines = 200

// BuildMemoryPromptSection 构建注入 system prompt 的记忆段落（文件版）。
// 如果 MEMORY.md 存在且非空，返回格式化的记忆段落；否则返回空字符串。
// 对标 memdir/memoryTypes.ts 的 TYPES_SECTION_INDIVIDUAL
func BuildMemoryPromptSection(memoryDir string) string {
	index, err := ReadMemoryIndex(memoryDir)
	if err != nil || index == "" {
		return ""
	}
	return buildPromptFromIndex(index, memoryDir)
}

// BuildMemoryPromptSectionPG 构建注入 system prompt 的记忆段落（PG 版）。
func BuildMemoryPromptSectionPG(ctx context.Context, store *Store) string {
	index, err := store.BuildIndex(ctx)
	if err != nil || index == "" {
		return ""
	}
	return buildPromptFromIndex(index, "")
}

func buildPromptFromIndex(index, memoryDir string) string {
	// 截断到 200 行
	lines := strings.Split(index, "\n")
	if len(lines) > maxIndexLines {
		lines = lines[:maxIndexLines]
	}
	truncated := strings.Join(lines, "\n")

	var sb strings.Builder
	sb.WriteString("\n# Memory\n\n")
	sb.WriteString("You have a persistent memory system. The index below contains pointers to your memory files.\n")
	sb.WriteString("Read the referenced files when relevant to the current task.\n\n")
	sb.WriteString("## Memory Index\n\n")
	sb.WriteString(truncated)
	sb.WriteString("\n\n## Types of memory\n\n")
	sb.WriteString("- **user**: User preferences, role, and knowledge background\n")
	sb.WriteString("- **feedback**: Corrections or confirmations about your approach\n")
	sb.WriteString("- **project**: Ongoing work context not derivable from code\n")
	sb.WriteString("- **reference**: Pointers to external systems and resources\n\n")
	sb.WriteString("## When to save\n\n")
	sb.WriteString("- When the user explicitly asks you to remember something\n")
	sb.WriteString("- When you learn important non-obvious context (user preferences, project constraints)\n")
	sb.WriteString("- Save as whichever type fits best\n\n")
	sb.WriteString("## When to access\n\n")
	sb.WriteString("- When memories seem relevant to the current task\n")
	sb.WriteString("- When the user references prior-conversation work\n\n")
	sb.WriteString("## What NOT to save\n\n")
	sb.WriteString("- Code patterns, conventions, architecture — derivable from the codebase\n")
	sb.WriteString("- Git history or recent changes — use git log instead\n")
	sb.WriteString("- Anything already in CLAUDE.md\n")
	sb.WriteString("- Ephemeral task details or in-progress work\n")
	if memoryDir != "" {
		sb.WriteString(fmt.Sprintf("\nMemory directory: %s\n", memoryDir))
	}

	return sb.String()
}
