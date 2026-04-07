package memory

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxMemoryFiles      = 200
	memoryIndexFilename = "MEMORY.md"
)

// GetMemoryDir 返回当前项目的记忆目录路径。
// 对标 memdir/paths.ts:getAutoMemPath
// 路径格式: ~/.edoc/projects/<sanitized-workdir>/memory/
func GetMemoryDir(workDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	sanitized := sanitizePath(workDir)
	return filepath.Join(home, ".edoc", "projects", sanitized, "memory")
}

// EnsureMemoryDir 确保记忆目录存在
func EnsureMemoryDir(memoryDir string) error {
	return os.MkdirAll(memoryDir, 0o755)
}

// ScanMemories 扫描记忆目录，返回所有 .md 文件的 Header。
// 排除 MEMORY.md，按修改时间降序，最多 200 个。
// 对标 memdir/memoryScan.ts:scanMemoryFiles
func ScanMemories(memoryDir string) ([]Header, error) {
	entries, err := os.ReadDir(memoryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var headers []Header
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || name == memoryIndexFilename {
			continue
		}

		filePath := filepath.Join(memoryDir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}

		h := parseMemoryHeader(filePath, name, info.ModTime())
		headers = append(headers, h)
	}

	// 按修改时间降序
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].ModTime.After(headers[j].ModTime)
	})

	if len(headers) > maxMemoryFiles {
		headers = headers[:maxMemoryFiles]
	}

	return headers, nil
}

// ReadMemoryIndex 读取 MEMORY.md 索引内容。
// 返回空字符串如果文件不存在。
func ReadMemoryIndex(memoryDir string) (string, error) {
	path := filepath.Join(memoryDir, memoryIndexFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// FormatMemoryManifest 将 Header 列表格式化为文本清单。
// 对标 memdir/memoryScan.ts:formatMemoryManifest
func FormatMemoryManifest(headers []Header) string {
	var lines []string
	for _, h := range headers {
		tag := ""
		if h.Type != "" {
			tag = fmt.Sprintf("[%s] ", h.Type)
		}
		ts := h.ModTime.Format("2006-01-02T15:04:05")
		if h.Description != "" {
			lines = append(lines, fmt.Sprintf("- %s%s (%s): %s", tag, h.Filename, ts, h.Description))
		} else {
			lines = append(lines, fmt.Sprintf("- %s%s (%s)", tag, h.Filename, ts))
		}
	}
	return strings.Join(lines, "\n")
}

// --- internal helpers ---

// sanitizePath 将路径中的特殊字符替换为安全字符
func sanitizePath(p string) string {
	// 统一分隔符
	p = filepath.ToSlash(p)
	// 去掉开头的 / 或盘符
	p = strings.TrimPrefix(p, "/")
	// 替换特殊字符
	re := regexp.MustCompile(`[/\\:*?"<>|]`)
	p = re.ReplaceAllString(p, "-")
	// 去掉连续的 -
	for strings.Contains(p, "--") {
		p = strings.ReplaceAll(p, "--", "-")
	}
	return strings.Trim(p, "-")
}

// parseMemoryHeader 从文件读取 frontmatter 并解析为 Header
func parseMemoryHeader(filePath, filename string, modTime time.Time) Header {
	h := Header{
		Filename: filename,
		FilePath: filePath,
		ModTime:  modTime,
	}

	f, err := os.Open(filePath)
	if err != nil {
		return h
	}
	defer f.Close()

	// 简易 frontmatter 解析：读前 30 行，找 --- 包围的 YAML 头
	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	lineCount := 0
	for scanner.Scan() && lineCount < 30 {
		line := scanner.Text()
		lineCount++

		if line == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break // 结束 frontmatter
		}

		if !inFrontmatter {
			continue
		}

		// 解析 key: value
		key, value, ok := parseYAMLLine(line)
		if !ok {
			continue
		}
		switch key {
		case "name":
			h.Name = value
		case "description":
			h.Description = value
		case "type":
			h.Type = ParseMemoryType(value)
		}
	}

	return h
}

// parseYAMLLine 解析 "key: value" 格式的 YAML 行
func parseYAMLLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	// 去掉引号
	value = strings.Trim(value, "\"'")
	return key, value, true
}
