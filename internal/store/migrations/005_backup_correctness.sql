CREATE TABLE backup_policy (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 target_interval_hours INTEGER NOT NULL DEFAULT 24,
 retention_daily INTEGER NOT NULL DEFAULT 14,
 retention_weekly INTEGER NOT NULL DEFAULT 8,
 updated_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO backup_policy(id) VALUES (1);
CREATE TABLE backup_schedule (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 last_attempt_at INTEGER NOT NULL DEFAULT 0,
 failures INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bucket_targets (
 storage_target TEXT PRIMARY KEY,
 endpoint TEXT NOT NULL,
 bucket TEXT NOT NULL,
 prefix TEXT NOT NULL,
 active INTEGER NOT NULL DEFAULT 0,
 reconciled_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE bucket_prefixes (
 storage_target TEXT NOT NULL,
 db_name TEXT NOT NULL,
 reconciled_at INTEGER NOT NULL DEFAULT 0,
 complete INTEGER NOT NULL DEFAULT 0,
 manifest_only INTEGER NOT NULL DEFAULT 0,
 archive_only INTEGER NOT NULL DEFAULT 0,
 damaged INTEGER NOT NULL DEFAULT 0,
 mixed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (storage_target, db_name)
);
CREATE TABLE bucket_backups (
 storage_target TEXT NOT NULL,
 manifest_key TEXT NOT NULL,
 db_name TEXT NOT NULL,
 taken_at INTEGER NOT NULL,
 state TEXT NOT NULL,
 archive_key TEXT NOT NULL DEFAULT '',
 installation_id TEXT NOT NULL DEFAULT '',
 project_id TEXT NOT NULL DEFAULT '',
 project_name TEXT NOT NULL DEFAULT '',
 postgres_version TEXT NOT NULL DEFAULT '',
 table_count INTEGER NOT NULL DEFAULT 0,
 size_bytes INTEGER NOT NULL DEFAULT 0,
 seen_at INTEGER NOT NULL,
 delete_started_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (storage_target, manifest_key)
);
CREATE INDEX bucket_backups_db ON bucket_backups(storage_target, db_name, taken_at DESC);
ALTER TABLE jobs ADD COLUMN target_storage TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN target_key TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN scheduled INTEGER NOT NULL DEFAULT 0;
