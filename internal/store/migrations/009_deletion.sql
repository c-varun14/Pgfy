-- Set when a project is marked for deletion; the row stays, with stage
-- 'deleting', until the database and role are gone.
ALTER TABLE projects ADD COLUMN deleting_at INTEGER;
