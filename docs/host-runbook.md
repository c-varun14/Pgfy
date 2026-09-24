# Host runbook

Pgfy runs on one Ubuntu 24.04 host you operate. This page is the routine upkeep and the break-glass steps. The
[installation guide](installation.md) covers installing and updating; the [recovery runbook](recovery-runbook.md) covers
losing the server.

## Who maintains what

| Component | How it is updated | Who decides when |
| --- | --- | --- |
| Pgfy application, PostgreSQL 18 minor versions, Caddy | A new Pgfy release, applied with `sudo pgfyctl update <bundle>` (images are pinned by digest in each release) | The operator, after reading the release notes |
| Docker Engine and Compose | Installed by the installer at pinned versions and held with `apt-mark hold`; upgraded deliberately (below) | The operator, in a maintenance window |
| Ubuntu security updates | `unattended-upgrades`, daily, security pocket only | Automatic |
| Other Ubuntu packages, kernel reboots | Monthly maintenance window | The operator |

Write down the named person responsible for each row before running client databases.

## Patch and reboot cadence

- **Daily, automatic:** security updates. The installer enables `unattended-upgrades` unless it was explicitly turned
  off on the host (it then warns). Automatic reboots stay off (`Unattended-Upgrade::Automatic-Reboot` defaults to false).
  `sudo pgfyctl diagnostics` shows the effective setting.
- **Monthly window:** `sudo apt update && sudo apt upgrade`. Docker packages the installer installed are held and are not
  upgraded by this; on hosts where Docker was already present, exclude `docker-ce`, `docker-ce-cli`, `containerd.io`
  and the Compose plugin yourself, because upgrading them restarts every container.
- **Docker upgrades:** in a window, `sudo apt-mark unhold <packages>`, upgrade, `sudo apt-mark hold <packages>`, then
  validate as below. Expect PostgreSQL connections to drop while containers restart.
- **Reboots:** when `/var/run/reboot-required` exists. Reboot in a window, then validate.

### Validating after a reboot or upgrade

1. `sudo pgfyctl diagnostics` — all three services running, dependencies ready, clock synchronised, disks not low.
2. `curl -fsS https://<dashboard-host>/health/ready` returns `ready`.
3. Sign in and open Settings: Host shows "Healthy", the certificate expiry is in the future.
4. Connect with a database's connection URL (`psql "<url>" -c 'select 1'`).
5. The next scheduled backup succeeds (Backups page), or run "Back up now".

## Host status and what the warnings mean

A systemd timer (`pgfy-host-status.timer`, every five minutes) runs `pgfyctl host-status`, which writes
`config/host-status.json`: free space on the PostgreSQL volume, the backup workspace and `/`, and whether the clock is
NTP-synchronised. Each certificate delivery (`pgfy-cert.timer`, daily, HTTPS mode) records its outcome in
`config/cert-sync.json`. The dashboard reads both; alerts use the same data.

- **Low disk (under 15% free):** free space or grow the disk before PostgreSQL or a backup runs out. Backups need room
  in the workspace for one archive.
- **Clock not synchronised:** fix `systemd-timesyncd` (or chrony). Sign-in codes and certificate checks depend on the
  time.
- **Report stale / no report:** the timer is not running. `systemctl status pgfy-host-status.timer`; after rolling back
  to a release older than the host status feature the timer fails harmlessly until the next update.
- **Certificate expiring within 14 days, or the last delivery failed:** the certificate serves both the dashboard and
  PostgreSQL, so expiry stops every client that verifies it. Check DNS and that ports 80/443 reach Caddy, then run
  `sudo pgfyctl sync-db-cert`. With shorter certificate lifetimes, a single missed daily sync can cross the 14-day line;
  treat a failed delivery as the earlier warning.

Alerts for these conditions go to the webhook set in Settings → Alerts (see [Alerts](installation.md#alerts)). Also
point an external uptime monitor at `https://<dashboard-host>/health/ready` (HTTPS mode): it is the only thing that
notices when the whole host is down.

## Break-glass access

- Keep the provider console login and an SSH key that is not stored on the server somewhere safe; they are the way in
  when the dashboard is unreachable.
- Dashboard certificate or hostname broken: from SSH, `sudo pgfyctl tunnel`, then `ssh -L 8080:127.0.0.1:8080 …` and
  open `http://127.0.0.1:8080`. Return with `sudo pgfyctl hostname <name>`.
- Setup not finished and the token expired: `sudo pgfyctl setup-token`.
- Lost the authenticator app or the password: `sudo pgfyctl reset-admin`, then "Reset access" on the sign-in page. It
  replaces both the password and the second factor and signs out every session. This is also how a tunnel-mode
  installation gets a second factor.
- A first enrolment (setup, or an existing administrator's first HTTPS sign-in) completed while an update was running
  is undone if that update rolls back; remove the entry from the authenticator app and enrol again at the next sign-in.
- An update was interrupted: `sudo pgfyctl rollback-update`. Backups left paused: `sudo pgfyctl maintenance off`.

## Retiring a database (until deletion exists)

Pgfy cannot delete a database yet. To stop a client using one while keeping its data safe:

1. On its page, **Freeze writes**, then **Back up now** and confirm the backup completed.
2. Stop the role logging in and end its sessions, as the bootstrap role:
   ```sh
   cd /opt/firstcommit   # or the directory you passed to the installer with --dir
   sudo ./pgfyctl diagnostics   # confirm the installation is healthy first
   prefix=$(sudo python3 -c 'import json;print(json.load(open("state.json"))["volume_prefix"])')
   release=$(sudo python3 -c 'import json;print(json.load(open("state.json"))["release"])')
   mode=$(sudo python3 -c 'import json;print(json.load(open("config/install.json"))["mode"])')
   sudo docker compose --project-name "$prefix" --env-file compose.env \
     -f "releases/$release/compose.yaml" -f "releases/$release/compose.$mode.yaml" \
     exec -T postgres sh -c 'PGPASSWORD="$(cat /run/secrets/bootstrap_password)" psql -X -v ON_ERROR_STOP=1 -U pgfy_bootstrap -d pgfy_system' <<'SQL'
   ALTER ROLE app_xxxxxxxxxxxx NOLOGIN;
   SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'app_xxxxxxxxxxxx';
   SQL
   ```
   Replace `app_xxxxxxxxxxxx` with the database's user from its Connect tab. The dashboard does not know about this
   change: it still lists the database as ready. Do not revoke `CONNECT` or drop anything: backups run as the management role through the database's owner role and
   would start failing.
3. Backups of the retired database keep running and keep being pruned by retention, which is intended while its data is
   kept. Its connection check in the dashboard now fails.

Deletion as a supported job is planned; until then, retire rather than drop.

## Backup target interval, agreed with each client

Before moving a client's database, get written acceptance of the backup target, for example:

> Your database is backed up to off-site storage about every **24 hours**; in a total server loss, changes made since
> the newest completed backup can be lost.

## Bucket protection per provider

Pgfy refuses storage whose protection it cannot establish (see
[backup storage credentials](installation.md#backup-storage-credentials)).

- **S3 and S3-compatible stores with versioning:** enable bucket versioning; use a credential that may put, get, list
  and delete objects and read the versioning setting, and that is denied `s3:DeleteObjectVersion` and bucket
  configuration changes. A deleted or overwritten backup then remains recoverable as a previous version.
- **Stores that cannot report versioning (object lock or lifecycle based):** configure the provider's own protection
  (object lock / retention rules, or a credential that cannot delete), then acknowledge it in Settings. Pgfy trusts the
  acknowledgment; check it yourself.
- In both cases keep provider-side encryption at rest on, and store the credential and bucket details outside the
  server, as the recovery runbook requires.
