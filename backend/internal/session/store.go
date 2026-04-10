package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dysorder/edoc-edualc/backend/internal/message"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session represents a conversation session.
type Session struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	ProjectKey string    `json:"project_key"`
	Model      string    `json:"model"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Store is the PostgreSQL-backed session store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a session store instance.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create creates a new session and returns it with the auto-generated UUID.
func (s *Store) Create(ctx context.Context, userID, projectKey, model string) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx,
		`INSERT INTO sessions (user_id, project_key, model)
		 VALUES ($1, $2, $3)
		 RETURNING id, user_id, project_key, model, title, created_at, updated_at`,
		userID, projectKey, model,
	).Scan(&sess.ID, &sess.UserID, &sess.ProjectKey, &sess.Model, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &sess, nil
}

// Get returns a session by ID.
func (s *Store) Get(ctx context.Context, id string) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, project_key, model, title, created_at, updated_at
		 FROM sessions WHERE id = $1`,
		id,
	).Scan(&sess.ID, &sess.UserID, &sess.ProjectKey, &sess.Model, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", id, err)
	}
	return &sess, nil
}

// List returns sessions for a user+project, ordered by updated_at descending.
func (s *Store) List(ctx context.Context, userID, projectKey string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, project_key, model, title, created_at, updated_at
		 FROM sessions
		 WHERE user_id = $1 AND project_key = $2
		 ORDER BY updated_at DESC
		 LIMIT $3`,
		userID, projectKey, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.ProjectKey, &sess.Model, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// AppendMessages appends messages to a session with auto-incrementing seq.
func (s *Store) AppendMessages(ctx context.Context, sessionID string, msgs []message.Message) error {
	if len(msgs) == 0 {
		return nil
	}

	// Get current max seq
	var maxSeq int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM messages WHERE session_id = $1`,
		sessionID,
	).Scan(&maxSeq)
	if err != nil {
		return fmt.Errorf("get max seq: %w", err)
	}

	// Batch insert
	for i, msg := range msgs {
		contentJSON, err := json.Marshal(msg.Content)
		if err != nil {
			return fmt.Errorf("marshal message content[%d]: %w", i, err)
		}
		_, err = s.pool.Exec(ctx,
			`INSERT INTO messages (session_id, role, seq, content)
			 VALUES ($1, $2, $3, $4)`,
			sessionID, string(msg.Role), maxSeq+i+1, contentJSON,
		)
		if err != nil {
			return fmt.Errorf("insert message[%d]: %w", i, err)
		}
	}

	// Update session timestamp
	_, err = s.pool.Exec(ctx,
		`UPDATE sessions SET updated_at = now() WHERE id = $1`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session timestamp: %w", err)
	}
	return nil
}

// LoadMessages loads all messages for a session, ordered by seq ascending.
func (s *Store) LoadMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT role, content FROM messages
		 WHERE session_id = $1
		 ORDER BY seq`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()

	var msgs []message.Message
	for rows.Next() {
		var role string
		var contentJSON []byte
		if err := rows.Scan(&role, &contentJSON); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		var blocks []message.ContentBlock
		if err := json.Unmarshal(contentJSON, &blocks); err != nil {
			return nil, fmt.Errorf("unmarshal message content: %w", err)
		}
		msgs = append(msgs, message.Message{
			Role:    message.Role(role),
			Content: blocks,
		})
	}
	return msgs, rows.Err()
}

// UpdateTitle updates the session title.
func (s *Store) UpdateTitle(ctx context.Context, sessionID, title string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET title = $1, updated_at = now() WHERE id = $2`,
		title, sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session title: %w", err)
	}
	return nil
}

// Delete deletes a session and all its messages (CASCADE).
func (s *Store) Delete(ctx context.Context, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE id = $1`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ReplaceMessages deletes all messages for a session and inserts new ones (for compact).
func (s *Store) ReplaceMessages(ctx context.Context, sessionID string, msgs []message.Message) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE session_id = $1`, sessionID); err != nil {
		return fmt.Errorf("delete old messages: %w", err)
	}

	for i, msg := range msgs {
		contentJSON, err := json.Marshal(msg.Content)
		if err != nil {
			return fmt.Errorf("marshal message[%d]: %w", i, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO messages (session_id, role, seq, content) VALUES ($1, $2, $3, $4)`,
			sessionID, string(msg.Role), i+1, contentJSON,
		); err != nil {
			return fmt.Errorf("insert message[%d]: %w", i, err)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE sessions SET updated_at = now() WHERE id = $1`, sessionID); err != nil {
		return fmt.Errorf("update session timestamp: %w", err)
	}

	return tx.Commit(ctx)
}
