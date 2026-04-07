package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 是基于 PostgreSQL 的记忆存储。
// 对标 Claude Code 的 memdir 文件系统，但用数据库替代文件。
type Store struct {
	pool       *pgxpool.Pool
	userID     string
	projectKey string
}

// NewStore 创建记忆存储实例。
func NewStore(pool *pgxpool.Pool, userID, projectKey string) *Store {
	return &Store{
		pool:       pool,
		userID:     userID,
		projectKey: projectKey,
	}
}

// List 返回当前用户+项目的所有记忆，按更新时间降序。
// 对标 ScanMemories（文件版），返回 Header 列表。
func (s *Store) List(ctx context.Context) ([]Header, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT filename, name, description, type, updated_at
		 FROM memories
		 WHERE user_id = $1 AND project_key = $2
		 ORDER BY updated_at DESC
		 LIMIT 200`,
		s.userID, s.projectKey,
	)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	var headers []Header
	for rows.Next() {
		var h Header
		if err := rows.Scan(&h.Filename, &h.Name, &h.Description, &h.Type, &h.ModTime); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		headers = append(headers, h)
	}
	return headers, rows.Err()
}

// Get 读取单条记忆的完整内容。
func (s *Store) Get(ctx context.Context, filename string) (*HeaderWithContent, error) {
	var h HeaderWithContent
	err := s.pool.QueryRow(ctx,
		`SELECT filename, name, description, type, content, updated_at
		 FROM memories
		 WHERE user_id = $1 AND project_key = $2 AND filename = $3`,
		s.userID, s.projectKey, filename,
	).Scan(&h.Filename, &h.Name, &h.Description, &h.Type, &h.Content, &h.ModTime)
	if err != nil {
		return nil, fmt.Errorf("get memory %s: %w", filename, err)
	}
	return &h, nil
}

// Save 保存一条记忆（INSERT ON CONFLICT UPDATE）。
// 对标 SaveMemory（文件版），但写入 PG。
func (s *Store) Save(ctx context.Context, filename, name, description string, memType MemoryType, content string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO memories (user_id, project_key, filename, name, description, type, content, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		 ON CONFLICT (user_id, project_key, filename)
		 DO UPDATE SET name = $4, description = $5, type = $6, content = $7, updated_at = now()`,
		s.userID, s.projectKey, filename, name, description, string(memType), content,
	)
	if err != nil {
		return fmt.Errorf("save memory %s: %w", filename, err)
	}
	return nil
}

// Delete 删除一条记忆。
func (s *Store) Delete(ctx context.Context, filename string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM memories
		 WHERE user_id = $1 AND project_key = $2 AND filename = $3`,
		s.userID, s.projectKey, filename,
	)
	if err != nil {
		return fmt.Errorf("delete memory %s: %w", filename, err)
	}
	return nil
}

// BuildIndex 从数据库生成 MEMORY.md 格式的索引文本。
// 对标 ReadMemoryIndex（文件版），但动态生成。
func (s *Store) BuildIndex(ctx context.Context) (string, error) {
	headers, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	if len(headers) == 0 {
		return "", nil
	}

	var lines []string
	for _, h := range headers {
		title := h.Name
		if title == "" {
			title = filenameToTitle(h.Filename)
		}
		hook := h.Description
		if hook == "" {
			hook = title
		}
		lines = append(lines, fmt.Sprintf("- [%s](%s) — %s", title, h.Filename, hook))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// Header 扩展：包含 Content 字段（Get 时使用）
// 为了不破坏现有 Header，用一个嵌入结构
type HeaderWithContent struct {
	Header
	Content string
}

// SanitizeProjectKey 将 workDir 转换为安全的 project key
func SanitizeProjectKey(workDir string) string {
	return sanitizePath(workDir)
}
