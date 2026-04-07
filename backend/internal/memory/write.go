package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SaveMemory 保存一条记忆（写文件 + 更新 MEMORY.md 索引）。
// filename 如 "user_role.md"，不含路径。
func SaveMemory(memoryDir, filename, name, description string, memType MemoryType, content string) error {
	if err := EnsureMemoryDir(memoryDir); err != nil {
		return fmt.Errorf("ensure memory dir: %w", err)
	}

	// 写记忆文件（带 frontmatter）
	filePath := filepath.Join(memoryDir, filename)
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	sb.WriteString(fmt.Sprintf("description: %s\n", description))
	sb.WriteString(fmt.Sprintf("type: %s\n", string(memType)))
	sb.WriteString("---\n\n")
	sb.WriteString(content)
	sb.WriteString("\n")

	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write memory file: %w", err)
	}

	// 更新 MEMORY.md 索引
	return UpdateMemoryIndex(memoryDir)
}

// DeleteMemory 删除一条记忆（删文件 + 更新 MEMORY.md 索引）。
func DeleteMemory(memoryDir, filename string) error {
	filePath := filepath.Join(memoryDir, filename)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete memory file: %w", err)
	}

	return UpdateMemoryIndex(memoryDir)
}

// UpdateMemoryIndex 重新生成 MEMORY.md 索引。
// 扫描目录所有 .md 文件（排除自身），按 frontmatter 生成索引行。
func UpdateMemoryIndex(memoryDir string) error {
	headers, err := ScanMemories(memoryDir)
	if err != nil {
		return fmt.Errorf("scan memories: %w", err)
	}

	var lines []string
	for _, h := range headers {
		title := h.Name
		if title == "" {
			// 从文件名推导标题
			title = filenameToTitle(h.Filename)
		}
		hook := h.Description
		if hook == "" {
			hook = title
		}
		lines = append(lines, fmt.Sprintf("- [%s](%s) — %s", title, h.Filename, hook))
	}

	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}

	indexPath := filepath.Join(memoryDir, memoryIndexFilename)
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write MEMORY.md: %w", err)
	}

	return nil
}

// filenameToTitle 从文件名推导记忆标题。
// "user_role.md" → "User Role"
func filenameToTitle(filename string) string {
	name := strings.TrimSuffix(filename, ".md")
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	return strings.Title(name)
}
