CREATE TABLE settings (
 key TEXT PRIMARY KEY,
 sealed_value TEXT NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE jobs (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL,
 project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
 state TEXT NOT NULL DEFAULT 'queued',
 stage TEXT NOT NULL DEFAULT 'queued',
 error TEXT NOT NULL DEFAULT '',
 input TEXT NOT NULL DEFAULT '{}',
 result TEXT NOT NULL DEFAULT '{}',
 created_at INTEGER NOT NULL,
 started_at INTEGER,
 finished_at INTEGER,
 stage_at INTEGER
);
CREATE INDEX jobs_state ON jobs(state, created_at);
CREATE INDEX jobs_project ON jobs(project_id, created_at);
CREATE TABLE backups (
 id TEXT PRIMARY KEY,
 project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
 job_id TEXT NOT NULL,
 object_key TEXT NOT NULL UNIQUE,
 manifest TEXT NOT NULL,
 size_bytes INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE INDEX backups_project ON backups(project_id, created_at);
