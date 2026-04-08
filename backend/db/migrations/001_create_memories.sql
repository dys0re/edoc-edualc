-- 001_create_memories.sql
-- Memory system: persistent cross-session memory storage

BEGIN;

CREATE TABLE IF NOT EXISTS memories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     VARCHAR(255) NOT NULL DEFAULT '',
    project_key VARCHAR(255) NOT NULL,
    filename    VARCHAR(255) NOT NULL,
    name        VARCHAR(255) NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    type        VARCHAR(20)  NOT NULL DEFAULT 'user',   -- user | feedback | project | reference
    content     TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(user_id, project_key, filename)
);

CREATE INDEX IF NOT EXISTS idx_memories_user_project ON memories(user_id, project_key);
CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(user_id, type);

COMMIT;
