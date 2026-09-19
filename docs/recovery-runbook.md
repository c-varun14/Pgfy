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
3. In **Settings → Backup storage**, enter the retained storage values and press **Test storage**.
4. Open **Recovery**. Every completed backup in the bucket is listed with its project name, time, size and PostgreSQL version.
5. Select the backup, name the new project, and press **Restore into a new project**. The restore downloads the archive,
   verifies its SHA-256 against the manifest, creates a new database and role, restores as that role, and re-counts the
   tables recorded at backup time. Watch the stage and elapsed time; you may close the browser.
6. When it reports **Restore verified**, open the recovered project, copy the new connection URL into your application,
   and run a read and a write from the application.
7. Review the project's allowed addresses (they default to open over TLS) and restrict them if the original was restricted.
8. Record: backup age (backup time vs. now), the verification results shown, and the wall-clock recovery time.

## What the verification covers

Row counts per table as captured in the backup snapshot, table ownership by the new project role, and the new role's
ability to connect. It does not verify application-level correctness or data written after the backup time.

## If something fails

- **Storage test fails** — check endpoint/region/bucket/keys; the step that failed is named.
- **Checksum mismatch** — the archive in the bucket is damaged or incomplete; choose another backup. Nothing was restored.
- **Version mismatch** — the backup comes from a different PostgreSQL major version; restore on a matching release.
- **Interrupted** — the application restarted mid-restore. Start the restore again; it creates another fresh project.
- **Verification found differences** — the restore ran but counts or ownership differ; inspect the listed checks before using it.
