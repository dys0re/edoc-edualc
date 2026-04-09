package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/memory"
	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/dysorder/edoc-edualc/backend/internal/provider"
)

// ExtractedMemory 是从对话中提取的单条记忆。
type ExtractedMemory struct {
	Filename    string `json:"filename"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"` // user / feedback / project / reference
	Content     string `json:"content"`
}

const extractMemoriesPrompt = `Review this conversation and extract important memories worth persisting for future sessions.

Focus on:
- User preferences, role, expertise level (type: user)
- Feedback about how to approach work — corrections AND confirmations (type: feedback)
- Project context: goals, decisions, constraints, deadlines (type: project)
- References to external systems, URLs, resources (type: reference)

Skip:
- Ephemeral task details or in-progress work
- Things already obvious from the code
- Debugging steps or temporary state

Return a JSON array (may be empty []). Each item:
{
  "filename": "short_snake_case.md",
  "name": "Short title",
  "description": "One-line hook for the memory index",
  "type": "user|feedback|project|reference",
  "content": "Memory body. For feedback/project: lead with the rule/fact, then **Why:** and **How to apply:** lines."
}

Return ONLY the JSON array, no other text.`

// ExtractMemories 从对话历史中提取记忆并写入 memoryDir。
// 在 compact 完成后调用，对标 services/extractMemories/。
// 非阻塞失败：提取失败不影响主流程。
func ExtractMemories(ctx context.Context, cfg CompactConfig, messages []message.Message, memoryDir string) (int, error) {
	if len(messages) < 4 {
		return 0, nil // 对话太短，不值得提取
	}

	// 构建提取请求
	req := provider.ChatRequest{
		Messages:     append(messages, message.NewUserMessage(extractMemoriesPrompt)),
		SystemPrompt: "You extract structured memories from conversations. Return only valid JSON.",
		Model:        cfg.Model,
		MaxTokens:    4096,
	}

	streamCh, err := cfg.Provider.StreamChat(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("extract memories: %w", err)
	}

	var raw string
	for evt := range streamCh {
		switch evt.Type {
		case "text_delta":
			raw += evt.Delta
		case "error":
			return 0, fmt.Errorf("extract memories stream: %w", evt.Error)
		}
	}

	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return 0, nil
	}

	// 去掉可能的 markdown 代码块包装
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var extracted []ExtractedMemory
	if err := json.Unmarshal([]byte(raw), &extracted); err != nil {
		return 0, fmt.Errorf("parse extracted memories: %w", err)
	}

	if err := memory.EnsureMemoryDir(memoryDir); err != nil {
		return 0, err
	}

	saved := 0
	for _, m := range extracted {
		if m.Filename == "" || m.Content == "" {
			continue
		}
		// 确保 .md 后缀
		if !strings.HasSuffix(m.Filename, ".md") {
			m.Filename += ".md"
		}
		// 加时间戳避免覆盖已有记忆（如果文件名冲突）
		filename := deduplicateFilename(memoryDir, m.Filename)

		memType := memory.ParseMemoryType(m.Type)
		if err := memory.SaveMemory(memoryDir, filename, m.Name, m.Description, memType, m.Content); err != nil {
			continue // 单条失败不中断
		}
		saved++
	}

	return saved, nil
}

// deduplicateFilename 如果文件已存在，加时间戳后缀。
func deduplicateFilename(dir, filename string) string {
	path := dir + "/" + filename
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return filename
	}
	ext := ".md"
	base := strings.TrimSuffix(filename, ext)
	return fmt.Sprintf("%s_%s%s", base, time.Now().Format("150405"), ext)
}
