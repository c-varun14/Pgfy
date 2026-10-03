ALTER TABLE backups ADD COLUMN storage_target TEXT NOT NULL DEFAULT '';
CREATE INDEX backups_target ON backups(storage_target);
