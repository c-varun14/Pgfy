# API

All application endpoints use JSON under `/api/v1`. Responses containing authentication or health state use `Cache-Control: no-store`. Errors have `{ "error": { "code", "message", "request_id" } }` and never expose database errors or credentials.

| Method/path | Request | Response / authorization |
| --- | --- | --- |
| `GET /api/v1/setup` | — | `{available: boolean}`; no token is returned |
| `POST /api/v1/setup` | `{token, email, password}` | HTTPS mode: 200 `{next: "enrol", key, account, issuer, algorithm, digits, period, server_time}` and an enrolment cookie; no administrator exists until confirmed. Tunnel mode: 201, session cookie, `{email, csrf_token}` |
| `POST /api/v1/auth/login` | `{email, password}` | With a factor: 200 `{next: "code", server_time}` and a pending cookie (5 minutes), no session. Without one in HTTPS mode: 200 `{next: "enrol", …}`. Without one in tunnel mode: 200, session cookie, `{email, csrf_token}` |
| `POST /api/v1/auth/code` | `{code}` | 200, session cookie, `{email, csrf_token}`; 401 `invalid_code`, `code_reused` or `code_expired`; 429 `code_locked` after repeated wrong codes. Its own rate limit; no password hashing |
| `GET /api/v1/auth/enrol` | Enrolment cookie | The key again while the enrolment (10 minutes, 5 attempts) is live |
| `POST /api/v1/auth/enrol/confirm` | `{code}` | 200, session cookie, `{email, csrf_token}`; completes setup, a reset or a first enrolment in one transaction |
| `POST /api/v1/auth/reset` | `{token, password}` | 200 `{next: "enrol", …}` with a token from `pgfyctl reset-admin`; nothing changes until confirmed; 503 during an update |
| `POST /api/v1/auth/logout` | `{}` plus `X-CSRF-Token` | 204, session revoked, cookie cleared |
| `GET /api/v1/auth/session` | Session cookie | `{email, expires_at, csrf_token, client_ip}` |
| `GET /api/v1/system/status` | Session cookie | `ready`, `maintenance`, `host` (`state` ok/stale/unknown, `written_at`, `disks[]` with `free_percent` and `low` under 15%, `ntp_synchronized`, `certificate` with `expires_at` from the served certificate, `expiring` within 14 days and `last_sync`), SQLite/PostgreSQL states and versions, app/tool versions, backup state, `database_access` |
| `GET /api/v1/settings` | Session cookie | Read-only installation identity, hostname/origin/mode, release and recorded host versions |
| `GET /api/v1/projects` | Session cookie | `{projects: [...], database_access}`; each project carries stage, `failed`, `stage_error`, `size_bytes` (or `size_error`), `open_to_internet` (its policy admits any address) and `rotation_pending` |
| `GET /api/v1/system/connections` | Session cookie | `{max_connections, superuser_reserved, reserved, available, projects_used, projects_limit, other_used, warning, overcommitted, roles[], system[]}`; each role `{role, project, limit, connections, warning}`; `warning` at 80% of `available` or of a role's limit; 503 when PostgreSQL cannot be read |
| `POST /api/v1/projects` | `{name, idempotency_key?}` + CSRF | 202 with the new project (200 when the key repeats); provisioning continues in the background |
| `GET /api/v1/projects/{id}` | Session cookie | `{project, database_access}`; the project includes `policy`, `limits` (with `revision` and `applied_revision`) and `connections_now` (live sessions of its role) |
| `PUT /api/v1/projects/{id}/limits` | `{statement_timeout_ms, idle_in_transaction_ms, temp_file_limit_kb, lock_timeout_ms, connection_limit, revision}` + CSRF | Timeouts 0 (off) or 1 s–24 h; temporary files −1 (unlimited) or 1 MB–1 TB, never 0; connections 1–100. 409 on a stale revision. Applied to the role at once for ready projects; returns the limits |
| `POST /api/v1/projects/{id}/credentials/rotate` | `{}` + CSRF | 200 `{credentials, rotated_at}` once PostgreSQL enforces the new password and every session of the role has ended; 502 `rotation_incomplete` when PostgreSQL did not confirm it (the change is finished automatically); 409 before ready |
| `POST /api/v1/projects/{id}/retry` | `{}` + CSRF | 204; resumes a failed project from its recorded stage |
| `GET /api/v1/projects/{id}/credentials` | Session cookie | Host, port, database, user, password, `sslmode`, `url`, `psql`, `rotation_pending`; 409 until ready; never logged. While a rotation is pending this is still the active password, which may stop working once the change completes |
| `PUT /api/v1/projects/{id}/access` | `{revision, addresses[]}` + CSRF | Replaces the allowlist under an optimistic revision (409 on conflict, 400 on invalid addresses) and applies it; returns the policy state |
| `PUT /api/v1/projects/{id}/writes` | `{frozen}` + CSRF | Freezes or resumes application writes for a ready project (409 before ready); returns the project with `frozen_at` |
| `POST /api/v1/projects/{id}/connection-checks` | `{}` + CSRF | 201 `{id, application_name, command, expires_at}` |
| `GET /api/v1/projects/{id}/connection-checks/{check}` | Session cookie | `pending`, `successful` (with `evidence: {client_addr, tls, observed_at}`) or `expired` |
| `GET /api/v1/settings/storage` | Session cookie | `{configured, settings}` with masked credentials |
| `PUT /api/v1/settings/storage` | `{endpoint, region, bucket, prefix, access_key, secret_key, session_token, path_style, private_endpoint, bucket_protection}` + CSRF | Validates the settings, asks the provider about bucket versioning, then seals them; blank/masked credentials keep the stored ones |
| `POST /api/v1/settings/storage/check` | `{}` + CSRF | `{ok, steps}` for upload, list, download (with integrity), cleanup of a temporary object, and the bucket's protection |
| `GET /api/v1/settings/backups` | Session cookie | `{target_interval_hours, retention_daily, retention_weekly, updated_at}` |
| `PUT /api/v1/settings/backups` | Same fields + CSRF | Interval must be 1, 6, 12 or 24 hours; daily retention 1–90, weekly 0–52 |
| `POST /api/v1/projects/{id}/backups` | `{}` + CSRF | 202 with the queued job; 409 `job_busy` or `storage_not_configured` |
| `GET /api/v1/projects/{id}/backups` | Session cookie | `{backups, jobs, storage_configured, next_scheduled_at, newest_backup_at, target_interval_hours, failures, last_attempt_at}`; `newest_backup_at` is what the bucket holds, not local history |
| `GET /api/v1/jobs/{id}` | Session cookie | Job with `state`, `stage`, `error`, `result`, `elapsed_seconds` |
| `GET /api/v1/recovery/backups` | Session cookie | `{state, databases, reconciled_at, installation_id, busy, restores}`; `state` is `ok`, `checking` (the bucket has not been read completely yet) or `storage_not_configured`. `?db=&before=&limit=` returns one database's next page |
| `POST /api/v1/recovery/restores` | `{manifest_key, name}` + CSRF | 202 `{project, job}`; always a new project |
| `GET /api/v1/settings/alerts` | Session cookie | `{configured, url, has_secret, private_endpoint}`; `url` is scheme and host only |
| `PUT /api/v1/settings/alerts` | `{url, secret, private_endpoint}` + CSRF | Empty `url` turns alerts off; blank `secret`, or the masked URL sent back, keeps the stored value; 400 `invalid_webhook` for plain HTTP to a public receiver |
| `POST /api/v1/settings/alerts/test` | `{}` + CSRF | `{ok, error?}` after delivering a test message |
| `GET /api/v1/alerts` | Session cookie | `{conditions[], delivery: {last_ok_at, last_error}}`; active conditions first |
| `GET /health/live` | — | 200 when the process is serving |
| `GET /health/ready` | — | 200 `ready` or 503 `not_ready`; no component details |

Email identifiers are trimmed and lowercased. Passwords require at least 15 characters and at most 256 UTF-8 bytes and are hashed using Argon2id (64 MiB, three iterations, parallelism one, random 16-byte salt). Setup tokens and sessions contain 32 random bytes, encoded as base64url; only SHA-256 hashes are persisted. Tokens expire after 30 minutes; sessions have a 12-hour absolute expiry.

Cookies are host-only, HttpOnly, SameSite=Strict, Path=/; HTTPS uses Secure and `__Host-pgfy_session`, tunnel mode uses `pgfy_tunnel_session`. Sessions are bound to installation identity, access mode, configured origin, and access-configuration generation.

Every API mutation requires the exact configured `Origin`, `application/json`, and a non-cross-site Fetch Metadata header when supplied. Authenticated mutations additionally require a session-bound HMAC CSRF token. The API rejects unexpected Host headers. Caddy overwrites forwarded client addresses; only the isolated proxy-network range is trusted by the app.

Authentication is bounded to two concurrent expensive hash operations, ten attempts per client per fifteen minutes, and thirty total attempts per minute. Counters are bounded in-memory and reset on process restart. Retries return 429 and a bounded retry hint. Request bodies are limited to 4 KiB; secrets are never logged.

AES-256-GCM recoverable-secret encryption uses a separately mounted 32-byte key, fresh nonces, a versioned envelope, and purpose/record context as authenticated data. Project passwords are sealed with it and revealed only to the authenticated administrator.

## Projects and access

Project names are 1–64 letters, digits, spaces, dots, dashes, underscores, or parentheses. Database and role names are generated (`app_<12 hex>`), passwords are 32 random bytes. Provisioning is recorded stage by stage (`identity_persisted → role_created → database_created → ready`) and resumes after interruption; a role or database that already exists with different attributes marks the project failed with the reason.

The management connection uses the non-superuser `pgfy_mgmt` role (CREATEDB/CREATEROLE only). Project roles are `NOSUPERUSER NOCREATEDB NOCREATEROLE` with a connection limit; every project database revokes `PUBLIC` privileges so other projects cannot connect.

`policy.state` is `applied` only after PostgreSQL parsed the rule file without errors and reported a newer `pg_conf_load_time()`. Addresses default to `0.0.0.0/0` and `::/0` (any address over TLS with the password). Remote rules are `hostssl` only, so non-TLS remote connections are rejected. SSH-tunnel connections arrive from the Docker gateway and are admitted per project regardless of the allowlist.

Freezing writes sets `default_transaction_read_only = on` on the project role and terminates its sessions, so pooled applications reconnect read-only: reads continue, writes fail with PostgreSQL's read-only transaction error, and a backup taken while frozen captures everything (backups are snapshot-consistent either way). The change is recorded only after PostgreSQL applied it and is reverted if recording fails. The freeze is cooperative — a client can `SET transaction_read_only = off` — so it is a tool for the operator's own applications before a move or a maintenance window, not an access control; the allowlist is the access control.

Connection checks are observed, not simulated: the dashboard issues an `application_name`; when a session with that name appears in `pg_stat_activity` for the project role, the client address and TLS state are recorded once as evidence.

## Backups and recovery

One worker goroutine runs heavy jobs one at a time. Jobs and their stages are persisted in SQLite; a job that was `running` when the process stopped is marked `interrupted` at the next start and is never reported as success. Backups run `pg_dump --format=custom --snapshot=…` as `pgfy_mgmt` with a protected pgpass file (never arguments or environment), capture per-table row counts in the same exported snapshot, checksum the archive, upload the archive, and publish `manifest.json` last; discovery ignores directories without a manifest. Archives above 2 GiB are refused in this release. The workspace is `data/work` on disk (the container's `/tmp` is a small tmpfs) and is emptied at startup.

Storage settings are sealed with the installation key. Any S3-compatible endpoint is configured through the same form (endpoint, region, bucket, prefix, path-style, optional session token); no provider APIs are used.

Once storage is configured, every ready project is backed up towards the configured target interval (24, 12, 6 or 1 hour; a scheduling pass runs every five minutes, `PGFY_SCHEDULE_INTERVAL`). Each pass considers every overdue project, least recently attempted first, and enqueues the one it can run; one heavy job at a time is unchanged. A failed or interrupted scheduled backup is retried after 15 minutes, an hour, four hours, then the target interval, so a failing database can never block the ones behind it. A manual backup records the attempt, clears the backoff on success, and never extends it on failure.

Backups live at `<prefix>/backups/<database>/<YYYYMMDDTHHMMSSZ>/{archive.dump,manifest.json}`. The same layout rules apply to listing, reconciliation, retention and restore: a folder with an unexpected name or file, or a manifest that disagrees with where it sits, is damaged — hidden, never restored, never deleted. Discovery pages each database separately, newest first, from a reconciled view of the bucket held in SQLite, so no request performs a full scan and no number of backups can hide a database. A backup counts as recoverable only when its manifest and its archive are both present; a backup removed outside the application stops counting, and the project becomes due again.

Retention runs after a successful backup: the newest backup of each database is always kept, then a daily and a weekly series (14 and 8 by default). Deletion mirrors publication — manifest first, then archive, then the local row — with the intent recorded first so a half-delete is finished on a later pass. Nothing is deleted in a folder holding another installation's backups or anything unreadable, an abandoned archive is removed only when a local job claims that exact folder in that exact store, and no deletion happens at all unless the bucket is protected (versioning enabled, or acknowledged where the provider cannot report it). Job rows older than 90 days are pruned, except those whose provenance cleanup still needs. `GET /api/v1/system/status` reports `not_configured`, `checking`, `failing`, `stale` (older than 1.5× the interval) or `ok`.

Restores always create a new project: download, SHA-256 verification, PostgreSQL major-version check, provisioning, `pg_restore --no-owner --no-privileges --role=<new role>`, then verification (row counts recorded at backup time, counts of sequences, views, functions, indexes and constraints, ownership, connect privilege). `verification` is `verified`, `partial` (every check that this backup supports passed, but it carries no object baselines or its table list was truncated at 5000) or `failed`; `verified` remains true only for `verified`. A `pg_restore` that exits non-zero, or reports errors, is stored as "completed with N restore errors" with a bounded stderr excerpt: the job succeeds, the database is usable, and it is not called verified. A restore that never ran — the tool could not start, was signalled, timed out or was cancelled — fails the job. The original database is never modified.

There are no APIs for domain changes, cutover confirmation, Caddy administration, public account recovery, or signup after initial setup.

While `pgfyctl update` holds the installation paused (`maintenance: true` in the status), every non-GET API request except setup, sign-in (password and code steps, enrolment confirmation) and sign-out returns `503` with code `maintenance`, no job starts, provisioning waits, and reconciliation reads the bucket without deleting anything.

## Audit

Sign-in cookies (`session`, `pending`, `enrol`) are `__Host-` prefixed and Secure in HTTPS mode, host-only in tunnel mode, always HttpOnly and SameSite=Strict. Setup, reset, enrolment and pending tokens live in one `auth_tokens` table as hashes with an expiry and an attempt limit enforced inside the verifying transaction.

Administrative actions are recorded in an append-only `audit` table in SQLite (time, administrator, action, target, request ID and a short detail that never contains a secret): `credentials.rotate`, `access.update`, `writes.freeze`, `writes.resume`, `limits.update`, `settings.storage`, `settings.backups`, `settings.alerts`, `auth.second_factor` and `auth.reset`. The rotation row commits in the same transaction as the new password; the others are written after their change commits. Updates and deletes are refused by the database. There is no audit view yet.
