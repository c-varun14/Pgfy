# Release notes

## Unreleased — second factor (`mvp-to-production`)

- HTTPS mode requires an authenticator-app code after the password; setup enrols the factor before the administrator
  exists, and an existing administrator enrols one at the next sign-in. Keys are typed, not scanned; no new
  dependency (RFC 6238 with the standard library).
- Codes are single-use, allow one step of clock skew, and wrong codes lock code entry with a growing delay that
  survives restarts; repeated wrong codes and every enrolment or reset are alerted.
- `pgfyctl reset-admin` issues a one-use, 30-minute token for "Reset access": a new password and factor, applied only
  when a code confirms, signing out every session.
- Setup, reset, enrolment and pending tokens share one table; the setup token table is migrated into it.

## Unreleased — alerts (`mvp-to-production`)

- One generic JSON webhook (Settings → Alerts), sealed, with a test button and optional HMAC signing. It covers failing
  and late backups, interrupted jobs, PostgreSQL unreachable for five minutes, low disk, a silent host report, clock
  drift, certificate expiry and delivery failures, connection pressure, manifests without archives, and password
  changes. Each condition fires at most once a day and sends a resolved message after staying clear for ten minutes;
  conditions that cannot be checked are never reported resolved.
- Slack incoming webhooks work without an adapter; Discord via its `/slack` URL.

## Unreleased — host status and upkeep (`mvp-to-production`)

- `pgfy-host-status.timer` records free disk (PostgreSQL volume, backup workspace, `/`) and clock synchronisation every
  five minutes; Settings shows them, and says when the report is missing or stale.
- The database certificate's expiry is read from the certificate PostgreSQL serves and shown in Settings, with a
  warning within 14 days; every delivery attempt's outcome is recorded and a failed one is shown.
- Switching to HTTPS with `pgfyctl hostname` now installs the certificate timer; switching to tunnel mode stops it.
- The installer enables unattended security updates unless explicitly disabled, and holds the Docker packages it
  installs.
- A [host runbook](host-runbook.md): maintenance ownership, patch and reboot cadence with validation, break-glass
  access, retiring a database, client acceptance of the backup target, and bucket protection per provider.

## Unreleased — access and capacity (`mvp-to-production`)

- Databases whose policy admits any address show "Open to the internet" on their card and page. The default is
  unchanged.
- Connection budget: ten reserved slots keep the dashboard and health checks from being locked out; Settings shows use
  against what PostgreSQL accepts, per-database limits, and warns at 80%.
- Per-database guardrails (statement 60 s, idle in transaction 5 min, lock wait 10 s, temporary files 1 GB,
  25 connections), editable per database and re-applied if changed on the role.
- Password rotation: sessions of the old password end before the new one becomes active; an unconfirmed change is
  finished automatically and never loses the new password.
- An append-only audit table records rotations, access, freeze, limits and settings changes.
- Updating installs the new PostgreSQL grants through the release's converge step.

## Unreleased — updater (`mvp-to-production`)

- `pgfyctl update <bundle>` moves an installation to a newer release: verify, pause jobs and changes (`--drain` waits
  for a running backup), snapshot SQLite and the rewritten files, switch, migrate, verify readiness, and roll back
  automatically on any failure or interruption. `pgfyctl rollback-update` finishes an interrupted one. A successful
  update restarts PostgreSQL and Caddy once; a rollback restarts them twice. Everyone is signed out.
- The dashboard shows when an update is in progress; writes are refused with `503 maintenance` until it finishes.
- `rollback-hostname` restores only access settings and regenerates the Caddyfile, so it cannot revert a release.
- Release bundles are checked for integrity by the installed release and for policy (PostgreSQL 18 on bookworm, any
  minor version) by the release itself.
- The CI integration run exercises a rollback after a test-only migration; tagged release builds do not.

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
