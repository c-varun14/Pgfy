# Recovery runbook

Recover a project database on a replacement server using only the external backups. Nothing from the
original server (its SQLite metadata, encryption key, or disk) is required.

## Keep these outside the server

Store the following somewhere that survives the loss of the server (a password manager works):

- Storage endpoint URL, signing region, bucket name and folder (prefix), and whether path-style addressing is used.
- Access key ID and secret access key (and session token, if used) with read access to the bucket. The keys used
  by the original server are fine; separately issued read-only keys also work.
- The dashboard hostname you will reuse (or a new one) and where its DNS record is managed.
- The application connection details you will need to update (where `DATABASE_URL` lives).

## Steps

1. Provision a fresh Ubuntu 24.04 x86-64 server (2 vCPU / 2 GiB / 20 GiB free is the tested minimum). Point the dashboard
   hostname at it (or reassign the static IP) and open TCP 80, 443 and 5432 in the provider firewall.
2. Install Pgfy with the one-line bootstrap and create the administrator with the setup token.
3. In **Backups → Backup storage**, enter the retained storage values, state how the bucket is protected
   (versioning enabled, or an acknowledgment where the provider cannot report it) and press **Test storage**.
4. Open **Backups**. Pgfy reads the whole bucket once — the page says *checking* until it has — and then lists
   every database it found, each with its own backups newest first, with project name, time, size and
   PostgreSQL version. Backups written by another server appear under **From other servers**. An incomplete
   backup (a manifest with no archive, or a folder that does not match the layout) is counted but never
   offered: it cannot be restored.
5. Select the backup, name the new project, and press **Restore into a new project**. The restore downloads the archive,
   verifies its SHA-256 against the manifest, creates a new database and role, restores as that role, and re-counts the
   tables recorded at backup time. Watch the stage and elapsed time; you may close the browser.
6. Read the result. **Restore verified** means every check the backup supports passed. **Restored — partly
   verified** means the checks that could be made passed, but the backup carries no object counts (it predates
   them) or its table list was truncated at 5000 tables. **Restored — not verified** means `pg_restore`
   reported errors, or a check disagreed: the database exists and can be inspected, but do not rely on it
   until you have looked at the listed checks and the error excerpt. Then open the recovered project, copy the
   new connection URL into your application, and run a read and a write from the application.
7. Review the project's allowed addresses (they default to open over TLS) and restrict them if the original was restricted.
8. Record: backup age (backup time vs. now), the verification results shown, and the wall-clock recovery time.

## What the verification covers

Row counts per table as captured in the backup snapshot, counts of sequences, views, functions, indexes and
constraints captured in that same snapshot, table ownership by the new project role, and the new role's ability
to connect. It does not verify application-level correctness or data written after the backup time.

## What is kept in the bucket

The newest backup of every database is always kept, then a daily series (14 by default) and a weekly series
(8 by default), configured in **Settings → Backups** along with the backup target interval. Retention runs
after a successful backup and only when the bucket is protected. Deletion removes the manifest first and the
archive second, so a backup is never offered without the archive behind it. Backups belonging to another
installation, and anything unreadable, are never deleted: clean those up yourself if you need to.

## If something fails

- **Storage test fails** — check endpoint/region/bucket/keys; the step that failed is named.
- **Checksum mismatch** — the archive in the bucket is damaged or incomplete; choose another backup. Nothing was restored.
- **Version mismatch** — the backup comes from a different PostgreSQL major version; restore on a matching release.
- **Interrupted** — the application restarted mid-restore. Start the restore again; it creates another fresh project.
- **Restored — not verified** — the restore ran but reported errors or a check disagreed. The database is there;
  read the checks and the `pg_restore` excerpt shown with the result before relying on it.
- **Storage settings refused** — the bucket reports that versioning is off. Turn it on at the provider, or
  protect the bucket another way and say so explicitly. If the provider cannot report versioning at all, the
  acknowledgment is the only option; if it reports access denied, allow `s3:GetBucketVersioning`.
