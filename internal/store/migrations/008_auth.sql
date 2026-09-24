-- Every short-lived credential in one place: setup, SSH-issued reset, an
-- enrolment waiting for its first code, and a password step waiting for its code.
CREATE TABLE auth_tokens (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 purpose TEXT NOT NULL CHECK(purpose IN ('setup','reset','enrol','pending')),
 token_hash TEXT NOT NULL UNIQUE,
 expires_at INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 max_attempts INTEGER NOT NULL,
 consumed_at INTEGER,
 parent_id INTEGER REFERENCES auth_tokens(id) ON DELETE SET NULL,
 sealed_payload TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL
);
INSERT INTO auth_tokens(purpose,token_hash,expires_at,max_attempts,created_at)
 SELECT 'setup', token_hash, expires_at, 1, CAST(strftime('%s','now') AS INTEGER) FROM setup_token;
DROP TABLE setup_token;
-- NULL until the administrator enrols a second factor.
ALTER TABLE administrator ADD COLUMN totp_secret_sealed TEXT;
ALTER TABLE administrator ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
-- Wrong codes across pending tokens and restarts: guessing is slowed per administrator.
ALTER TABLE administrator ADD COLUMN code_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE administrator ADD COLUMN code_locked_until INTEGER NOT NULL DEFAULT 0;
