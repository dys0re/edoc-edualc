package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate 创建必要的数据库表。
// 对标 Prisma migrate dev，直接用 SQL。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// --- memories 表 ---
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS memories (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id     VARCHAR(255) NOT NULL DEFAULT '',
			project_key VARCHAR(255) NOT NULL,
			filename    VARCHAR(255) NOT NULL,
			name        VARCHAR(255) NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			type        VARCHAR(20)  NOT NULL DEFAULT 'user',
			content     TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(user_id, project_key, filename)
		);
		CREATE INDEX IF NOT EXISTS idx_memories_user_project ON memories(user_id, project_key);
		CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(user_id, type);
	`); err != nil {
		return fmt.Errorf("migrate memories: %w", err)
	}

	// --- sessions 表 ---
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS sessions (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id     VARCHAR(255) NOT NULL DEFAULT '',
			project_key VARCHAR(255) NOT NULL,
			model       VARCHAR(100) NOT NULL DEFAULT '',
			title       TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_user_project ON sessions(user_id, project_key);
	`); err != nil {
		return fmt.Errorf("migrate sessions: %w", err)
	}

	// --- messages 表 ---
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS messages (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			role        VARCHAR(20) NOT NULL,
			seq         INT NOT NULL,
			content     JSONB NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(session_id, seq)
		);
		CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
	`); err != nil {
		return fmt.Errorf("migrate messages: %w", err)
	}

	return nil
}
