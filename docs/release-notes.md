# Release notes

## Unreleased — Tier 0 A: backup correctness (`mvp-to-production`)

Post-hackathon work from `docs/phases.md` Phase 6. This is the last release that ships as a manual reinstall;
the update procedure comes next.

- Scheduling considers every overdue database each pass, least recently attempted first, and backs a failing
  one off (15 minutes, an hour, four hours, then the target interval). One failing database can no longer
  block the backups behind it.
- A configurable backup target interval (24, 12, 6 or 1 hour) and retention (14 daily, 8 weekly) in Settings,
  with a per-database "newest recoverable backup" age taken from the bucket rather than from local history.
- Discovery pages each database separately from a reconciled view of the bucket, so a store with more than 200
  manifests no longer hides whole databases. Incomplete and unreadable backups are counted and never offered.
- Retention deletes the manifest before the archive, finishes its own half-deletes, never touches another
  installation's or unreadable backups, and only runs when the bucket is protected.
- Storage settings require bucket versioning, or an explicit acknowledgment where the provider cannot report
  it; plain-HTTP endpoints are limited to private addresses.
- Restores report verified, partly verified or not verified, with the `pg_restore` error count and excerpt.
  Verification also compares sequences, views, functions, indexes and constraints; the table capture limit is
  now 5000 and truncation is reported.
- `GET /api/v1/system/status` reports the real backup state instead of a hardcoded one.

## v0.2.0 — Complete MVP (create, connect, back up, recover)

- Projects: one name creates a database with a restricted role and strong password; provisioning resumes after interruption and reports real failure reasons.
- Connection details with `sslmode=verify-full`, driver snippets, SSH-tunnel path, and an observed connection check.
- PostgreSQL TLS using the dashboard certificate (`pgfyctl sync-db-cert`, daily timer); TLS-only remote access with per-project allowed addresses applied through PostgreSQL's own parser and reload.
- Backups to any S3-compatible bucket: storage check, manual and daily backups, manifests published last, durable one-at-a-time jobs, restarts reported as interrupted.
- Recovery from the bucket alone on a fresh install, into a new project, with checksum, version and named verification checks.
- Installer changes: `config/pg/` layout, `management_password` secret, `data/work` workspace, PostgreSQL port publication (public in HTTPS mode, loopback in tunnel mode). Pre-release Phase 1 installations must be reinstalled fresh.

Not implemented: automated retention, external alerts, PgBouncer, one-click updates. This is a hackathon MVP; run the hardening gate before real workloads.

## v0.1.0 — Phase 1 foundation

Self-hosted PostgreSQL foundation with an embedded dashboard, single-admin setup/login, component status, read-only settings, host-managed
installation/recovery commands, a standalone bootstrap, and a checksummed bundle with digest-pinned images. Ubuntu 24.04 LTS x86-64 only.
