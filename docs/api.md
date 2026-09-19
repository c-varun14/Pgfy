# Phase 1 API

All application endpoints use JSON under `/api/v1`. Responses containing authentication or health state use `Cache-Control: no-store`. Errors have `{ "error": { "code", "message", "request_id" } }` and never expose database errors or credentials.

| Method/path | Request | Response / authorization |
| --- | --- | --- |
| `GET /api/v1/setup` | — | `{available: boolean}`; no token is returned |
| `POST /api/v1/setup` | `{token, email, password}` | 201, session cookie, `{email, csrf_token}` |
| `POST /api/v1/auth/login` | `{email, password}` | 200, session cookie, `{email, csrf_token}` |
| `POST /api/v1/auth/logout` | `{}` plus `X-CSRF-Token` | 204, session revoked, cookie cleared |
| `GET /api/v1/auth/session` | Session cookie | `{email, expires_at, csrf_token}` |
| `GET /api/v1/system/status` | Session cookie | `ready`, SQLite/PostgreSQL states and versions, app/tool versions, backup state |
| `GET /api/v1/settings` | Session cookie | Read-only installation identity, hostname/origin/mode, release and recorded host versions |
| `GET /health/live` | — | 200 when the process is serving |
| `GET /health/ready` | — | 200 `ready` or 503 `not_ready`; no component details |

Email identifiers are trimmed and lowercased. Passwords require at least 15 characters and at most 256 UTF-8 bytes and are hashed using Argon2id (64 MiB, three iterations, parallelism one, random 16-byte salt). Setup tokens and sessions contain 32 random bytes, encoded as base64url; only SHA-256 hashes are persisted. Tokens expire after 30 minutes; sessions have a 12-hour absolute expiry.

Cookies are host-only, HttpOnly, SameSite=Strict, Path=/; HTTPS uses Secure and `__Host-pgfy_session`, tunnel mode uses `pgfy_tunnel_session`. Sessions are bound to installation identity, access mode, configured origin, and access-configuration generation.

Every API mutation requires the exact configured `Origin`, `application/json`, and a non-cross-site Fetch Metadata header when supplied. Authenticated mutations additionally require a session-bound HMAC CSRF token. The API rejects unexpected Host headers. Caddy overwrites forwarded client addresses; only the isolated proxy-network range is trusted by the app.

Authentication is bounded to two concurrent expensive hash operations, ten attempts per client per fifteen minutes, and thirty total attempts per minute. Counters are bounded in-memory and reset on process restart. Retries return 429 and a bounded retry hint. Request bodies are limited to 4 KiB; secrets are never logged.

AES-256-GCM recoverable-secret encryption uses a separately mounted 32-byte key, fresh nonces, a versioned envelope, and purpose/record context as authenticated data. Phase 1 establishes and tests this primitive; project/storage credentials are introduced in later phases.

There are no APIs for domain changes, cutover confirmation, Caddy administration, project provisioning, backup execution, public account recovery, or signup after initial setup.
