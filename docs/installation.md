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
```

`sync-db-cert` copies the certificate Caddy obtained for the dashboard hostname into PostgreSQL (validated, key permissions fixed, previous pair kept), reloads, and confirms a new TLS handshake presents it. The installer runs it once and installs a daily `pgfy-cert.timer` for renewals. Until it has succeeded, PostgreSQL serves a self-signed placeholder and the dashboard says so.

Token replacement works only before administrator creation and after the previous token expires. Losing an unexpired token requires waiting for expiry. Installer reruns never issue a replacement automatically. There is no public registration-reopening or password-reset endpoint.

Hostname changes validate the hostname, DNS, Caddy configuration, routing, and HTTPS. They save the previous access configuration and restore it on failure. An interruption can be recovered with `rollback-hostname`. Changes briefly restart the application and Caddy and require signing in again. A rollback restores configuration; it cannot repair DNS or an expired certificate for the former hostname.

Host access recovery verifies application liveness and routing independently of PostgreSQL/SQLite readiness, so a database outage does not prevent restoring dashboard access.

If the domain is unusable, explicitly run `pgfyctl tunnel` from SSH, then connect through the tunnel. Return to HTTPS with `pgfyctl hostname`. These commands never grant the dashboard access to Caddy's administration API.

## Persistence and reruns

Retain the exact release bundle. Running its installation command again preserves release selection, installation identity, volume names, configuration, and secrets. A different release is rejected; updates are a separate future procedure.

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
| `releases/` | Verified installed bundle and host command implementation |

Secret files use fixed container identities: bootstrap password owner/group `999:999`, mode `0400`; health and management passwords `10001:999`, mode `0440`; encryption key `10001:10001`, mode `0400`. The SQLite directory belongs to `10001:10001`, mode `0700`. Reruns reject unexpected secret ownership/permissions instead of rewriting them.

The app creates no fresh SQLite database during normal startup. Missing metadata fails closed. Do not replace missing SQLite/key files with empty ones or regenerate secrets beside existing data. A metadata/key backup must preserve their association. Phase 1 does not supply a disaster-recovery solution.

## Failure handling

- **Interrupted image pull:** correct connectivity and rerun the same release; existing state remains intact.
- **Occupied ports or incompatible prerequisites:** correct the reported condition; nothing is automatically removed or replaced.
- **Partial PostgreSQL initialization:** stop and inspect host logs and the existing volume. An initialized data directory does not cause Docker initialization scripts to run again. Pgfy checks a final marker and authenticated database identity; it never deletes or blindly reinitializes a partial directory.
- **SQLite migration failure/missing storage:** liveness and static dashboard assets remain available; readiness and protected operations fail. Restore the correct metadata or repair the failed migration after preserving a copy.
- **PostgreSQL outage:** dashboard login and SQLite-backed settings remain available; readiness and PostgreSQL status show failure.
- **Hostname-change interruption:** use host rollback; explicitly enable tunnel mode if necessary.

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

If the DNS record for the hostname is proxied through a CDN (for example Cloudflare's orange cloud), PostgreSQL connections will not pass through it: use a DNS-only record for the database hostname.

## Network boundary

The application publishes no host ports. Caddy publishes 80/443 in HTTPS mode or loopback 8080 in tunnel mode; PostgreSQL publishes 5432 on all interfaces in HTTPS mode or loopback only in tunnel mode. The database network shared with the application is internal; PostgreSQL additionally joins a dedicated network used only for the published port. Caddy admin is disabled. Host administrators remain trusted and can inspect Docker networks and volumes.

Verify from a separate public client and private-network peer that 3000, 2019, 8080, and 8081 are inaccessible, that 5432 rejects non-TLS and unlisted sources, and that a project role cannot open another project's database. Test IPv6 when enabled. Docker port publishing can bypass assumptions about UFW; inspect and probe actual behavior rather than treating a firewall screenshot as proof.
