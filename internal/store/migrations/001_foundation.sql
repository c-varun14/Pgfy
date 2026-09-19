CREATE TABLE administrator (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 email TEXT NOT NULL UNIQUE,
 password_hash TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE setup_token (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 token_hash TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE TABLE sessions (
 token_hash TEXT PRIMARY KEY,
 admin_id INTEGER NOT NULL REFERENCES administrator(id) ON DELETE CASCADE,
 scope TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
