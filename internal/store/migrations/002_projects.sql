CREATE TABLE projects (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 db_name TEXT NOT NULL UNIQUE,
 role_name TEXT NOT NULL UNIQUE,
 idempotency_key TEXT NOT NULL UNIQUE,
 stage TEXT NOT NULL DEFAULT 'identity_persisted',
 failed INTEGER NOT NULL DEFAULT 0,
 stage_error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 ready_at INTEGER
);
CREATE TABLE project_secrets (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 sealed_password TEXT NOT NULL
);
CREATE TABLE policy_revisions (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 addresses TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY (project_id, revision)
);
CREATE TABLE policy_state (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 current_revision INTEGER NOT NULL DEFAULT 0,
 applied_revision INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'idle',
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE connection_checks (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 challenge_hash TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending',
 evidence TEXT,
 fingerprint TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL
);
CREATE INDEX connection_checks_project ON connection_checks(project_id, created_at);
