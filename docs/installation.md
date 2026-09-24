# Installation and host recovery

## Requirements

- Ubuntu 24.04 LTS, x86-64, administrative access, Python 3, `curl`, `ip`, and `findmnt`.
- Start validation with 2 vCPU, 2 GiB RAM, and 20 GiB free disk on persistent local ext4, XFS, or Btrfs storage. Both the installation directory and Docker data storage are checked. These are provisional validation minimums, not production capacity claims.
- Docker Engine 28+ and Compose 2.30+. Compatible installations are preserved. An absent Docker installation is installed from Docker's official Ubuntu repository using exact package versions in `release.json`. Incompatible installations stop the installer instead of being replaced.
- Outbound DNS and HTTPS to container registries, and to the certificate authority for domain mode. First-time Docker installation also needs Ubuntu/Docker package repositories.
- Domain mode: a DNS hostname resolving to this host and inbound TCP 80/443. Set both A and any AAAA records correctly. Keep SSH restricted to operator addresses. No PostgreSQL port is required.

The generic installer never calls provider APIs or metadata services. The operator configures networking/firewalls; see the separate Lightsail guide for that environment.

The operator provisions the VPS, has root/sudo access, and prepares DNS/firewall rules (or uses the explicit SSH tunnel below). Pgfy installs
the software on that server. For the planned backup feature, the operator also creates a private S3-compatible bucket and scoped credentials,
then enters them in Settings. Storage is not required for initial dashboard installation. Provider independence applies to hosts meeting the
requirements above; it does not imply support for every operating system or architecture.

## One-command installation

The bootstrap is implemented and tested locally. **Release publication and VPS acceptance are still pending.** The command below becomes
usable after the first stable GitHub release publishes its bootstrap, bundle, and publicly pullable application image.

SSH into the prepared VPS, replace the hostname, and paste this single command. It downloads the complete bootstrap before running it:

```sh
(
  set -eu
  pgfy_bootstrap_dir=$(mktemp -d)
  trap 'rm -rf "$pgfy_bootstrap_dir"' EXIT
  curl --disable --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 120 \
    https://github.com/c-varun14/Pgfy/releases/latest/download/bootstrap.sh \
    -o "$pgfy_bootstrap_dir/bootstrap.sh"
  sudo bash "$pgfy_bootstrap_dir/bootstrap.sh" --hostname db-admin.example.com
)
```

The bootstrap selects the latest stable release on a fresh VPS, verifies the downloaded archive and internal file checksums, and runs the
bundled installer. Docker is installed if absent; the installer configures PostgreSQL, the dashboard, and Caddy, then prints the HTTPS
address and initial setup instructions. Neither GitHub authentication nor an AWS account is required.

- To choose a release, append `--version v0.1.0` to the `sudo bash` line. Explicit prerelease tags are allowed; automatic selection excludes them.
- To use another installation directory, append `--dir /opt/pgfy` and use that same directory on reruns.
- Without a domain, replace `--hostname db-admin.example.com` with `--tunnel`, then connect using the SSH instructions below.
- Reruns use the release recorded in the installation directory, even if a newer stable release exists. Requesting a different version fails;
  updates are a separate procedure. Corrupt or missing state beside existing files stops installation instead of replacing data.

Download failures, invalid checksums, and unsafe archives stop before the bundled installer executes. Rerun the command to retry. The
bootstrap and checksums are obtained from the project's trusted HTTPS release channel; checksums detect corruption, not an independent
publisher signature. Downloaded-bundle installation remains available as an alternative.

## Install a downloaded release

Download a specific `pgfy-vX.Y.Z.tar.gz` and its `.sha256` file from this repository's GitHub Releases. Verify the outer checksum before executing any bundled code:

```sh
sha256sum -c pgfy-vX.Y.Z.tar.gz.sha256
tar -xzf pgfy-vX.Y.Z.tar.gz
sudo bash pgfy-vX.Y.Z/install.sh --hostname db-admin.example.com
```

Replace the version and hostname with your actual values. The installer verifies its internal file checksums and digest-pinned images, uses `/opt/firstcommit`, waits for authenticated database/SQLite checks, and verifies Caddy routing and HTTPS before reporting success. Target servers do not build source.

Open the printed **HTTPS** address and enter the setup token from the terminal, an email address, and a password of at least 15 characters. The email is an identifier; Phase 1 has no email verification or email delivery. The random setup token expires after 30 minutes and is consumed atomically with administrator creation. Never put it in a URL, shell command argument, screenshot, or shared log.

Port 80 handles certificate challenges and HTTPS redirects only. Failed certificate issuance never enables public HTTP setup. Check DNS (including AAAA), certificate-authority connectivity, and inbound 80/443 before retrying.

The bundle checksum detects corruption; obtain the checksum and bundle from the trusted release channel. It is not an independent publisher signature.

## Explicit SSH tunnel mode

Without a domain:

```sh
# Alternative when using an already downloaded release bundle:
sudo bash pgfy-vX.Y.Z/install.sh --tunnel
ssh -L 8080:127.0.0.1:8080 user@server
```

Run `ssh` on your computer and open `http://127.0.0.1:8080`. Keep that exact address and port: origin checks and cookies are intentionally scoped to it. The only published dashboard listener binds to host loopback. Never add an inbound firewall rule for 8080 or 3000.

Tunnel cookies use a separate name and scope from HTTPS cookies. Access-mode and hostname changes invalidate existing sessions. The tunnel is an explicit alternative access mode, not an automatic certificate-failure fallback.

## Host commands

Use the installed wrapper, replacing the path if you supplied `--dir`:

```sh
sudo /opt/firstcommit/pgfyctl diagnostics
sudo /opt/firstcommit/pgfyctl setup-token
sudo /opt/firstcommit/pgfyctl hostname new-admin.example.com
sudo /opt/firstcommit/pgfyctl rollback-hostname
sudo /opt/firstcommit/pgfyctl tunnel
sudo /opt/firstcommit/pgfyctl sync-db-cert
sudo /opt/firstcommit/pgfyctl update /path/to/pgfy-vX.Y.Z
sudo /opt/firstcommit/pgfyctl rollback-update
sudo /opt/firstcommit/pgfyctl maintenance status|off
```

The installer also enables unattended security updates (unless explicitly disabled on the host), holds the Docker
packages it installed at their pinned versions, and installs `pgfy-host-status.timer`, which records disk space and
clock synchronisation for the dashboard every five minutes (`pgfyctl host-status`). Routine upkeep, reboots and
break-glass steps are in the [host runbook](host-runbook.md).

`sync-db-cert` copies the certificate Caddy obtained for the dashboard hostname into PostgreSQL (validated, key permissions fixed, previous pair kept), reloads, and confirms a new TLS handshake presents it. The installer runs it once and installs a daily `pgfy-cert.timer` for renewals (also when switching to HTTPS with `pgfyctl hostname`; switching to tunnel mode stops it). Every attempt's outcome is recorded in `config/cert-sync.json`, and Settings shows the certificate's expiry. Until it has succeeded, PostgreSQL serves a self-signed placeholder and the dashboard says so.

Token replacement works only before administrator creation and after the previous token expires. Losing an unexpired token requires waiting for expiry. Installer reruns never issue a replacement automatically. There is no public registration-reopening or password-reset endpoint.

Hostname changes validate the hostname, DNS, Caddy configuration, routing, and HTTPS. They save the previous access configuration and restore it on failure. An interruption can be recovered with `rollback-hostname`. Changes briefly restart the application and Caddy and require signing in again. A rollback restores configuration; it cannot repair DNS or an expired certificate for the former hostname.

Host access recovery verifies application liveness and routing independently of PostgreSQL/SQLite readiness, so a database outage does not prevent restoring dashboard access.

If the domain is unusable, explicitly run `pgfyctl tunnel` from SSH, then connect through the tunnel. Return to HTTPS with `pgfyctl hostname`. These commands never grant the dashboard access to Caddy's administration API.

## Persistence and reruns

Retain the exact release bundle. Running its installation command again preserves release selection, installation identity, volume names, configuration, and secrets. A different release is rejected; move to a newer release with `pgfyctl update` (below).

The installation contains:

| Location | Purpose |
| --- | --- |
| `state.json` | Installed release, image digests, persistent volume identities |
| `config/` | Host-managed dashboard origin, Caddy configuration, PostgreSQL access policy (`pg/pg_hba.conf`), database TLS material (`postgres-tls/`) |
| `config/pg/managed/` | Dashboard-written project access rules (`projects.conf`), included by the host policy; the only PostgreSQL configuration the app can write |
| `data/sqlite/` | Administrator, hashed sessions/setup tokens, projects, sealed credentials, jobs, sealed storage settings, backup policy and the reconciled view of the bucket |
| `data/work/` | Disk-backed workspace for backup/restore archives; emptied at application start |
| `secrets/encryption_key` | Recoverable-secret encryption key, separately protected from SQLite |
| `secrets/bootstrap_password` | PostgreSQL bootstrap credential, never mounted into the app |
| `secrets/health_password` | Restricted PostgreSQL health-query credential |
| `secrets/management_password` | Non-superuser provisioning credential (`pgfy_mgmt`) used by the dashboard |
| `releases/` | Verified installed bundles (current and earlier) and the host command implementation |
| `update-rollback/`, `data/sqlite/.update-rollback.db` | Present only while an update runs, or after one was interrupted: the files and storage snapshot a rollback restores |

Secret files use fixed container identities: bootstrap password owner/group `999:999`, mode `0400`; health and management passwords `10001:999`, mode `0440`; encryption key `10001:10001`, mode `0400`. The SQLite directory belongs to `10001:10001`, mode `0700`. Reruns reject unexpected secret ownership/permissions instead of rewriting them.

The app creates no fresh SQLite database during normal startup. Missing metadata fails closed. Do not replace missing SQLite/key files with empty ones or regenerate secrets beside existing data. A metadata/key backup must preserve their association. Phase 1 does not supply a disaster-recovery solution.

## Updating

Download the newer release bundle, verify it as for installation, extract it, and run the installed wrapper:

```sh
tar -xzf pgfy-vX.Y.Z.tar.gz
sudo /opt/firstcommit/pgfyctl update ./pgfy-vX.Y.Z
```

The installed release performs the update and, if needed, the rollback:

1. It verifies the new bundle's checksums, pinned digests and version, copies it into `releases/`, pulls its images and
   validates its Compose configuration. Nothing has changed yet; a failure here leaves the server as it was.
2. It pauses the installation: new backups, restores and dashboard changes are refused, and no job starts. If a backup
   or restore is running it refuses, or with `--drain` waits up to two hours for it to finish.
3. It stops the application and snapshots management storage (SQLite, with `VACUUM INTO` and an integrity check) and
   the files it is about to rewrite (`state.json`, `compose.env`, `config/install.json`, the Caddyfile, `pg_hba.conf`,
   `pgfyctl`).
4. It switches to the new release, applies the new release's host configuration, recreates the three services
   (PostgreSQL and Caddy restart once) and waits for full readiness. The new release migrates SQLite as it starts.
5. On success it resumes backups and removes the snapshot. Everyone is signed out.

If anything fails or the command is interrupted after the snapshot (including a dropped SSH session), the previous
release restores the snapshot and its files, starts, verifies readiness and reports what failed; PostgreSQL and Caddy
restart once more. Nothing is restored before the snapshot has been checked, and a damaged snapshot stops the
rollback with everything kept for inspection. If the host itself went down mid-update, run `pgfyctl rollback-update`.

What a rollback does not undo: releases only make PostgreSQL and host changes that the previous release can run with,
and while paused the new release does not provision databases or delete anything from the bucket.

Updates move forward only: a stable release to a newer stable release, and a pre-release to a later pre-release of the
same version or to a stable release. Pre-releases are for test hosts. Updates within PostgreSQL 18 are supported; a
different major version or base image is refused. An update is not a way to repair a broken installation — run
`pgfyctl diagnostics` first. If an update warns that backups are still paused, run `pgfyctl maintenance off`.

The first release that includes `update` is installed by the manual reinstall procedure; later ones use `update`.

## Alerts

Settings → Alerts takes one webhook URL. Pgfy checks every minute and posts JSON when:

| Kind | When |
| --- | --- |
| `backup_failed` | A database's scheduled backup failed twice in a row (the first failure is retried after 15 minutes) |
| `backup_stale` | A database's newest recoverable backup is older than 1.5 × the backup target interval |
| `job_interrupted` | A backup or restore was interrupted by an application restart (one message per job) |
| `postgres_unreachable` | The health check has failed continuously for five minutes |
| `disk_low` | Free space is under 15% on the PostgreSQL volume, the backup workspace or `/` (one alert per disk) |
| `host_report_stale` | The host stopped reporting disk and clock status, so those alerts cannot fire |
| `ntp_unsynchronised` | The host clock is not NTP-synchronised |
| `certificate_expiring`, `certificate_sync_failed` | HTTPS mode: the certificate expires within 14 days, or delivering it to PostgreSQL failed |
| `connections` | Project connections reach 80% of what PostgreSQL accepts for them, or a role reaches 80% of its limit |
| `manifest_only` | The bucket holds a manifest whose archive is missing |
| `credential_rotated` | A database password was changed (one message per change) |

Conditions are sent as `state: "firing"`, at most once every 24 hours per condition while they last, and
`state: "resolved"` once they have stayed clear for ten minutes; one-shot events are sent as `state: "event"`. A
condition that returns within 24 hours of its last firing message is not sent again until the 24 hours are up, so
the receiver may believe it resolved for that time — the dashboard always shows what is active now. A condition
Pgfy cannot check (for example connections while PostgreSQL is down) is never reported as resolved. Delivery is
at-least-once: a crash between delivery and recording it can repeat a message. Nothing is sent during an update, and
events older than 24 hours when delivery resumes are dropped.

The body:

```json
{"version": 1, "kind": "backup_failed", "key": "backup_failed:prj_…", "state": "firing",
 "summary": "Backups of shop are failing", "detail": "…", "installation_id": "…",
 "dashboard": "https://pgfy.example.com", "at": "2026-09-24T12:00:00Z", "text": "[Pgfy] Alert: …"}
```

`text` makes Slack incoming webhooks work as they are; Discord accepts the same body at its webhook URL with `/slack`
appended. With a signing secret, each request carries `X-Pgfy-Timestamp` (Unix seconds) and
`X-Pgfy-Signature: sha256=<hex>`, an HMAC-SHA256 of `timestamp + "." + body`. Verify it, and reject old timestamps:

```python
import hashlib, hmac, time
def verified(secret: bytes, timestamp: str, body: bytes, signature: str) -> bool:
    expected = "sha256=" + hmac.new(secret, timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, signature) and abs(time.time() - int(timestamp)) < 300
```

The URL must use HTTPS and reach a public address. A receiver on this machine's network (another container, a LAN
address, a Tailscale 100.64.0.0/10 address) needs "private endpoint", which also allows plain HTTP; link-local
addresses such as a cloud metadata service are always refused, and redirects are not followed. The URL and secret are
sealed like storage credentials; the dashboard and the audit trail only ever show the host.

Whole-host failures cannot alert from the host itself: point an external uptime monitor at
`https://<dashboard-host>/health/ready` (HTTPS mode).

## Failure handling

- **Interrupted image pull:** correct connectivity and rerun the same release; existing state remains intact.
- **Occupied ports or incompatible prerequisites:** correct the reported condition; nothing is automatically removed or replaced.
- **Partial PostgreSQL initialization:** stop and inspect host logs and the existing volume. An initialized data directory does not cause Docker initialization scripts to run again. Pgfy checks a final marker and authenticated database identity; it never deletes or blindly reinitializes a partial directory.
- **SQLite migration failure/missing storage:** liveness and static dashboard assets remain available; readiness and protected operations fail. Restore the correct metadata or repair the failed migration after preserving a copy.
- **PostgreSQL outage:** dashboard login and SQLite-backed settings remain available; readiness and PostgreSQL status show failure.
- **Hostname-change interruption:** use host rollback; explicitly enable tunnel mode if necessary. `rollback-hostname` restores only the access mode and hostname, never an earlier release.
- **Update interruption:** run `pgfyctl rollback-update`; `diagnostics` reports a leftover update and paused backups.

Diagnostics print safe state, not raw credentials or container logs. Operators can inspect restricted logs through Docker on the host. Do not share unreviewed logs or the installation's secret files.

## Backup storage credentials

The operator supplies an existing private bucket and scoped keys. Pgfy never creates buckets or changes their
configuration. Grant the key `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject` and `s3:ListBucket` on the
backup folder, plus `s3:GetBucketVersioning` so Pgfy can confirm the bucket keeps versions of deleted objects.
Deny `s3:DeleteObjectVersion` and every bucket-configuration action (`s3:PutBucketVersioning`,
`s3:PutBucketPolicy`, `s3:PutLifecycleConfiguration`, …): retention deletes objects, and versioning is what
makes that recoverable. Turn on the provider's encryption at rest for the bucket.

Storage settings will not save unless the bucket is protected. If the provider reports versioning is off,
enable it and save again. If the provider cannot report versioning at all (Cloudflare R2, for example), state
explicitly that the bucket is protected by the provider's own lock or that you accept that these keys can
delete backups; retention respects that decision. If the provider answers "access denied", grant
`s3:GetBucketVersioning` rather than working around it.

Endpoints must use `https://`. A plain `http://` endpoint is accepted only for a private endpoint on the same
machine or private network, and the address actually contacted is checked again when connecting.

## Database access

Applications connect to PostgreSQL at the dashboard hostname on port 5432 with TLS (`sslmode=verify-full`, using the same publicly trusted certificate as the dashboard). libpq-based clients such as `psql`, Python's psycopg and Ruby additionally need `sslrootcert=system` to use the operating system's trust store; Node.js, Go and JDBC drivers use it by default. In HTTPS mode the installer publishes 5432 on all interfaces; **open TCP 5432 in your provider firewall** to allow application traffic. Both layers must allow a connection: the provider firewall and the per-project allowlist in the dashboard. Remember that your application's server has its own outbound IP, which usually differs from the address you browse from.

In tunnel mode PostgreSQL is published on the host loopback only. Developers forward it from their computer and connect to `127.0.0.1:5432`:

```sh
ssh -N -L 5432:127.0.0.1:5432 user@server
```

Loopback-forwarded connections are admitted per project without a public port; the SSH tunnel encrypts the hop, so the dashboard shows a `sslmode=disable` URL there.

### Connections, limits and passwords

PostgreSQL accepts 150 connections. Three are kept for maintenance and ten are reserved for the dashboard's management
role and health checks (`reserved_connections`, with `pg_use_reserved_connections` granted to them), so project
databases can never lock the dashboard out; the remaining 137 are shared by all databases. Settings → Connections shows
what is in use, each database's limit and the sum of the limits, and warns when database connections reach 80% of the
137 or a database reaches 80% of its own limit. Limits may add up to more than 137: that is fine while not every application is busy at once.

Each database's user carries guardrails, set when it is created and editable on its Access tab: statement timeout 60 s,
idle-in-transaction timeout 5 minutes, lock wait 10 s, temporary files 1 GB per session and 25 connections. The
connection limit and the temporary-file limit are enforced; the three timeouts are defaults an application may override
with `SET` for its own session (Pgfy restores them if the role's defaults, or per-database defaults for its own
database, are changed). Changes apply to new
connections. Restores run under the management role and are not bound by these limits.

"Change password" on the Connect tab issues a new password, sets it on the database user, closes every session of that
user and only then makes it the password the dashboard shows; the new connection URL is displayed once in the dialog
and remains available through "Reveal" afterwards. If PostgreSQL does not confirm the change, the dashboard keeps the
old password active and finishes the change automatically in the background. Every rotation is recorded in the audit
table.

Existing installations receive the reserved-slot and parameter grants when updated with `pgfyctl update` (or on an
installer rerun); new installations get them at initialization.

If the DNS record for the hostname is proxied through a CDN (for example Cloudflare's orange cloud), PostgreSQL connections will not pass through it: use a DNS-only record for the database hostname.

## Network boundary

The application publishes no host ports. Caddy publishes 80/443 in HTTPS mode or loopback 8080 in tunnel mode; PostgreSQL publishes 5432 on all interfaces in HTTPS mode or loopback only in tunnel mode. The database network shared with the application is internal; PostgreSQL additionally joins a dedicated network used only for the published port. Caddy admin is disabled. Host administrators remain trusted and can inspect Docker networks and volumes.

Verify from a separate public client and private-network peer that 3000, 2019, 8080, and 8081 are inaccessible, that 5432 rejects non-TLS and unlisted sources, and that a project role cannot open another project's database. Test IPv6 when enabled. Docker port publishing can bypass assumptions about UFW; inspect and probe actual behavior rather than treating a firewall screenshot as proof.
