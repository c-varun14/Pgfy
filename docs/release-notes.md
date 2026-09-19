# Release notes

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
