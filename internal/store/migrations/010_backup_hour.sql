-- The UTC hour daily backups start at or after; -1 is any time.
ALTER TABLE backup_policy ADD COLUMN preferred_hour INTEGER NOT NULL DEFAULT -1;
