-- Test builds only (go build -tags updatetest): a migration the next release
-- applies, so the update integration test can prove a rollback undoes it.
CREATE TABLE update_probe (id INTEGER PRIMARY KEY);
