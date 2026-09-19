# API

All application endpoints use JSON under `/api/v1`. Responses containing authentication or health state use `Cache-Control: no-store`. Errors have `{ "error": { "code", "message", "request_id" } }` and never expose database errors or credentials.

| Method/path | Request | Response / authorization |
| --- | --- | --- |
| `GET /api/v1/setup` | — | `{available: boolean}`; no token is returned |
| `POST /api/v1/setup` | `{token, email, password}` | 201, session cookie, `{email, csrf_token}` |
| `POST /api/v1/auth/login` | `{email, password}` | 200, session cookie, `{email, csrf_token}` |
| `POST /api/v1/auth/logout` | `{}` plus `X-CSRF-Token` | 204, session revoked, cookie cleared |
| `GET /api/v1/auth/session` | Session cookie | `{email, expires_at, csrf_token, client_ip}` |
| `GET /api/v1/system/status` | Session cookie | `ready`, SQLite/PostgreSQL states and versions, app/tool versions, backup state, `database_access` |
| `GET /api/v1/settings` | Session cookie | Read-only installation identity, hostname/origin/mode, release and recorded host versions |
| `GET /api/v1/projects` | Session cookie | `{projects: [...], database_access}`; each project carries stage, `failed`, `stage_error`, `size_bytes` (or `size_error`) |
| `POST /api/v1/projects` | `{name, idempotency_key?}` + CSRF | 202 with the new project (200 when the key repeats); provisioning continues in the background |
| `GET /api/v1/projects/{id}` | Session cookie | `{project, database_access}`; the project includes `policy` and `connections_now` (live sessions of its role) |
| `POST /api/v1/projects/{id}/retry` | `{}` + CSRF | 204; resumes a failed project from its recorded stage |
| `GET /api/v1/projects/{id}/credentials` | Session cookie | Host, port, database, user, password, `sslmode`, `url`, `psql`; 409 until ready; never logged |
| `PUT /api/v1/projects/{id}/access` | `{revision, addresses[]}` + CSRF | Replaces the allowlist under an optimistic revision (409 on conflict, 400 on invalid addresses) and applies it; returns the policy state |
| `POST /api/v1/projects/{id}/connection-checks` | `{}` + CSRF | 201 `{id, application_name, command, expires_at}` |
| `GET /api/v1/projects/{id}/connection-checks/{check}` | Session cookie | `pending`, `successful` (with `evidence: {client_addr, tls, observed_at}`) or `expired` |
| `GET /api/v1/settings/storage` | Session cookie | `{configured, settings}` with masked credentials |
| `PUT /api/v1/settings/storage` | `{endpoint, region, bucket, prefix, access_key, secret_key, session_token, path_style}` + CSRF | Validates and seals the settings; blank/masked credentials keep the stored ones |
| `POST /api/v1/settings/storage/check` | `{}` + CSRF | `{ok, steps}` for upload, list, download (with integrity), cleanup of a temporary object |
| `POST /api/v1/projects/{id}/backups` | `{}` + CSRF | 202 with the queued job; 409 `job_busy` or `storage_not_configured` |
| `GET /api/v1/projects/{id}/backups` | Session cookie | `{backups, jobs, storage_configured, next_scheduled_at}` |
| `GET /api/v1/jobs/{id}` | Session cookie | Job with `state`, `stage`, `error`, `result`, `elapsed_seconds` |
| `GET /api/v1/recovery/backups` | Session cookie | `{state, backups: [manifests], busy, restores}`; `state` is `ok`, `storage_not_configured` or `storage_error` |
| `POST /api/v1/recovery/restores` | `{manifest_key, name}` + CSRF | 202 `{project, job}`; always a new project |
| `GET /health/live` | — | 200 when the process is serving |
| `GET /health/ready` | — | 200 `ready` or 503 `not_ready`; no component details |

Email identifiers are trimmed and lowercased. Passwords require at least 15 characters and at most 256 UTF-8 bytes and are hashed using Argon2id (64 MiB, three iterations, parallelism one, random 16-byte salt). Setup tokens and sessions contain 32 random bytes, encoded as base64url; only SHA-256 hashes are persisted. Tokens expire after 30 minutes; sessions have a 12-hour absolute expiry.

Cookies are host-only, HttpOnly, SameSite=Strict, Path=/; HTTPS uses Secure and `__Host-pgfy_session`, tunnel mode uses `pgfy_tunnel_session`. Sessions are bound to installation identity, access mode, configured origin, and access-configuration generation.

Every API mutation requires the exact configured `Origin`, `application/json`, and a non-cross-site Fetch Metadata header when supplied. Authenticated mutations additionally require a session-bound HMAC CSRF token. The API rejects unexpected Host headers. Caddy overwrites forwarded client addresses; only the isolated proxy-network range is trusted by the app.

Authentication is bounded to two concurrent expensive hash operations, ten attempts per client per fifteen minutes, and thirty total attempts per minute. Counters are bounded in-memory and reset on process restart. Retries return 429 and a bounded retry hint. Request bodies are limited to 4 KiB; secrets are never logged.

AES-256-GCM recoverable-secret encryption uses a separately mounted 32-byte key, fresh nonces, a versioned envelope, and purpose/record context as authenticated data. Project passwords are sealed with it and revealed only to the authenticated administrator.

## Projects and access

Project names are 1–64 letters, digits, spaces, dots, dashes, or underscores. Database and role names are generated (`app_<12 hex>`), passwords are 32 random bytes. Provisioning is recorded stage by stage (`identity_persisted → role_created → database_created → ready`) and resumes after interruption; a role or database that already exists with different attributes marks the project failed with the reason.

The management connection uses the non-superuser `pgfy_mgmt` role (CREATEDB/CREATEROLE only). Project roles are `NOSUPERUSER NOCREATEDB NOCREATEROLE` with a connection limit; every project database revokes `PUBLIC` privileges so other projects cannot connect.

`policy.state` is `applied` only after PostgreSQL parsed the rule file without errors and reported a newer `pg_conf_load_time()`. Addresses default to `0.0.0.0/0` and `::/0` (any address over TLS with the password). Remote rules are `hostssl` only, so non-TLS remote connections are rejected. SSH-tunnel connections arrive from the Docker gateway and are admitted per project regardless of the allowlist.

Connection checks are observed, not simulated: the dashboard issues an `application_name`; when a session with that name appears in `pg_stat_activity` for the project role, the client address and TLS state are recorded once as evidence.

## Backups and recovery

One worker goroutine runs heavy jobs one at a time. Jobs and their stages are persisted in SQLite; a job that was `running` when the process stopped is marked `interrupted` at the next start and is never reported as success. Backups run `pg_dump --format=custom --snapshot=…` as `pgfy_mgmt` with a protected pgpass file (never arguments or environment), capture per-table row counts in the same exported snapshot, checksum the archive, upload the archive, and publish `manifest.json` last; discovery ignores directories without a manifest. Archives above 2 GiB are refused in this release. The workspace is `data/work` on disk (the container's `/tmp` is a small tmpfs) and is emptied at startup.

Storage settings are sealed with the installation key. Any S3-compatible endpoint is configured through the same form (endpoint, region, bucket, prefix, path-style, optional session token); no provider APIs are used.

Once storage is configured, every ready project is backed up daily (a check runs every five minutes and enqueues one overdue project at a time). Retention is not automated.

Restores always create a new project: download, SHA-256 verification, PostgreSQL major-version check, provisioning, `pg_restore --no-owner --no-privileges --role=<new role>`, then verification (row counts recorded at backup time, ownership, connect privilege). The job reports `verified` only when all checks pass; the original database is never modified.

There are no APIs for domain changes, cutover confirmation, Caddy administration, public account recovery, or signup after initial setup.
