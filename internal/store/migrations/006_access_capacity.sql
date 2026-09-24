-- Append-only record of administrative actions. No foreign key: history must
-- outlive the administrator row it names.
CREATE TABLE audit (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 at INTEGER NOT NULL,
 admin_id INTEGER,
 action TEXT NOT NULL,
 target TEXT NOT NULL DEFAULT '',
 request_id TEXT NOT NULL DEFAULT '',
 detail TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;

-- Per-role guardrails. revision is what the operator asked for; applied_revision
-- is what PostgreSQL was last seen to carry.
CREATE TABLE project_limits (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 statement_timeout_ms INTEGER NOT NULL,
 idle_in_transaction_ms INTEGER NOT NULL,
 temp_file_limit_kb INTEGER NOT NULL,
 lock_timeout_ms INTEGER NOT NULL,
 connection_limit INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 applied_revision INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL
);
-- Existing roles were created with CONNECTION LIMIT 25; everything else is new.
INSERT INTO project_limits(project_id,statement_timeout_ms,idle_in_transaction_ms,temp_file_limit_kb,lock_timeout_ms,connection_limit,updated_at)
 SELECT id, 60000, 300000, 1048576, 10000, 25, created_at FROM projects;

-- A rotation in flight: set before the role changes, cleared by the swap.
ALTER TABLE project_secrets ADD COLUMN pending_password_sealed TEXT;
ALTER TABLE project_secrets ADD COLUMN pending_request_id TEXT NOT NULL DEFAULT '';
