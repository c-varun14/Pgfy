# Phase 1 validation

Implementation and automated local checks do not establish the two-host deployment gate. Record actual results below; never mark unrun checks as passed.

## Portability validation targets

The same installer must work on compatible VPS hosts, and the same backup implementation must accept independently chosen S3-compatible
storage. The initial targets are Lightsail plus AWS S3 for the demo, and AlphaVPS plus Backblaze B2 for the operator's intended use.
These are planned validation targets, not claims of current support. Broader provider matrices are later work.

| Milestone | Required evidence | Status |
| --- | --- | --- |
| Phase 1 | Same release installs on Lightsail and an AlphaVPS VPS meeting the Ubuntu/Docker requirements below | Pending on both hosts |
| Phase 3 | Backup to AWS S3 and fresh-server recovery on Lightsail | Not implemented / unvalidated |
| Phase 3 | Back up from Lightsail to B2, then discover and restore on a fresh AlphaVPS installation using the same release and only configuration changes | Not implemented / unvalidated |
| Phase 4 | Configure storage in Settings and complete scheduled backup/recovery through the dashboard on Lightsail + S3 and AlphaVPS + B2 | Not implemented / unvalidated |

The cross-provider recovery run demonstrates that compute and storage can be selected independently; there is no need to rehearse every
host/storage permutation. Initial portability remains an MVP gate. After Lightsail's Phase 1 checks pass, Phase 2/3 implementation may proceed
while AlphaVPS evidence is pending; do not mark Phase 1 deployment acceptance or the MVP complete until the required evidence exists.

For B2, use its S3-compatible endpoint, signing region, application key ID and application key in the standard storage fields. Use the same
S3 client rather than a B2-native adapter. B2 documents HTTPS, Signature V4, both addressing modes, and the object/multipart operations needed
here, but actual backup and recovery must still be tested. Avoid dependencies on object ACLs or tagging, whose support differs from AWS.
See [B2 endpoint and operation documentation](https://www.backblaze.com/docs/cloud-storage-call-the-s3-compatible-api) and
[S3 API differences](https://www.backblaze.com/docs/cloud-storage-s3-compatible-api). Never record credential values in validation evidence.

## Automated checks

| Check | Command / artifact |
| --- | --- |
| Backend and race checks | `go test -race ./...` after frontend build |
| Backend static analysis | `go vet ./...` |
| Frontend type and production build | `pnpm --dir web build` |
| Real setup/login browser flow, responsive layout | `make browser-test` |
| Bootstrap, real release packaging, and installer tests | `python3 -m unittest discover -s deploy/tests -v` |
| Shell syntax | `bash -n deploy/bootstrap.sh deploy/install.sh deploy/pgfyctl deploy/postgres/*.sh` |
| Pinned runtime/container integration | `make integration` |
| Integration evidence | `.cache/integration-results.json` |
| Browser screenshots/traces | `web/test-results/` |

The integration fixture exercises real SCRAM queries, SQLite, Caddy routing, setup/session behavior, restricted roles, exact tool versions, restart/rerun persistence, PostgreSQL outage behavior, initialization markers, and log redaction. It publishes only loopback 8080 and cleans up its own generated resources.

## Local evidence — 2026-09-19

- Go static analysis and all backend tests passed, including the race detector, atomic concurrent setup, session expiry/revocation, origin/CSRF rejection, encryption tampering, migration checks, and missing-metadata startup behavior.
- Frontend TypeScript and Vite production build passed with Vite 7.3.5.
- Real browser setup, invalid token, invalid login, settings, logout/login, session persistence, and desktop/mobile layout checks passed. Chromium was supplied from the local browser cache; CI installs Playwright's matching Chromium.
- Installer unit tests and shell syntax checks passed, including failed hostname rollback and host recovery independent of database readiness.
- Release packaging and checksum verification passed; a deliberately corrupted test bundle was rejected. This packaging fixture uses an intentionally non-publishable image reference and is not an installable release.
- Docker integration passed authenticated PostgreSQL 18.6 queries and matching client-tool versions, non-root app execution, health-role restrictions, Caddy routing, loopback port bindings, missing SQLite rejection/restoration, restart/rerun persistence, volume identities, PostgreSQL outage behavior, partial initialization detection, and secret-redaction assertions.
- The official Ubuntu package repository contains the pinned Docker 29.8.0, Compose 5.5.1, and containerd 2.3.5 packages. Fresh package installation on the two deployment hosts remains pending.
- Dependency audit found no moderate/high/critical findings. One low finding concerns esbuild's Windows development server, which is neither used by the Linux runtime nor exposed by this application.

These are local development-host results, not evidence of fresh Ubuntu installation, public certificate issuance, host reboot, or independent public/private-network reachability. The latest disposable-run resource snapshot and elapsed time are in `.cache/integration-results.json`; record measurements from both target hosts before capacity claims.

## Bootstrap implementation validation — 2026-09-19

- All 31 Python bootstrap/installer tests passed. They cover real release packaging, latest/explicit version selection, installed-version and
  image-digest preservation, malformed state, download/integrity failures, unsafe archives, argument forwarding, installer exit propagation,
  temporary-work cleanup, and preservation of installation identity after an interrupted image pull.
- Frontend TypeScript/Vite build, Go vet, Go race tests, and the Go application build passed. Go 1.27.1 was used with the existing pinned modules.
- Shell syntax passed including `deploy/bootstrap.sh`. The release verifier's CLI and Python compilation passed; real published-asset checks
  remain pending because no release has been published.
- The application image built and the disposable Docker integration suite passed all nine checks listed in its evidence artifact.
- The browser setup/login/logout/settings/degraded-PostgreSQL flow passed using cached Chromium. CI installs Playwright's matching browser.
- Documentation local links and whitespace checks passed. These checks do not establish a working public install URL or VPS acceptance.

Initial deployment access on 2026-09-19 failed because the AWS session expired and SSH timed out. After the operator refreshed the profile,
the running instance and static IP were confirmed. Approved SSH allowlist additions preserved the original source and added the workstation's
observed egress addresses; allowing the direct-network address restored SSH. See the deployment handoff for details.

The subsequent read-only VPS preflight confirmed Ubuntu 24.04.4 LTS x86-64, 2 CPUs, 1906 MiB total RAM, 55 GiB available on ext4, completed
cloud-init, and the dashboard DNS record resolving to the static IP. No Docker executable or `/opt/firstcommit` installation was present.
Only SSH and local DNS TCP listeners were observed; 80/443 were unoccupied. The historical inbound 3000 firewall rule still needs removal.
No application deployment, service interruption, or reboot was performed. Publication and Lightsail acceptance remain pending; second-host
acceptance is deferred. Access restoration and preflight do not establish installation acceptance.

## Required deployment evidence — pending

The one-command `curl` bootstrap is implemented with local test coverage. Publication, anonymous production downloads, and actual VPS
installation remain pending. Local packaging/fixture results do not establish deployment acceptance. The current execution scope is Lightsail;
the second-host gate is explicitly deferred and must remain pending rather than being marked passed.

Run the **same release artifact** on Lightsail Ubuntu 24.04 x86-64 and an AlphaVPS VPS with Ubuntu 24.04 x86-64 meeting the installation requirements. Do not substitute two Compose projects on one host for this gate.

Record for each host: release checksum/digests, OS/kernel/architecture, CPU/RAM/free disk, Docker/Compose versions, installation duration, idle and authenticated-request memory/CPU, filesystem/volume identity, DNS/IPv6 configuration, and probe locations. The initial 2 GiB target is provisional. Backup/restore measurements with application traffic belong to later phases.

- [ ] Published one-command bootstrap downloads/verifies the selected release and reaches setup without manual extraction on both hosts;
      hostname/tunnel modes and a custom installation directory work through the existing installer.
- [ ] Bootstrap download/checksum failures do not invoke the bundle; reruns preserve installed release and state; downloaded-bundle installation still works.
- [ ] Fresh HTTPS installation verifies a trusted certificate and routing before setup success.
- [ ] Explicit tunnel installation is accessible only through SSH/host loopback.
- [ ] Failed certificate issuance leaves public HTTP administration unavailable.
- [ ] Hostname change succeeds; failed change and interrupted change recover through rollback.
- [ ] Fresh Docker installation resolves the exact package versions recorded in the release; compatible Docker is preserved; incompatible Docker is rejected.
- [ ] Setup rejects expired/reused/concurrent tokens and never creates two admins.
- [ ] Login/session expiry/logout, CSRF/origin checks, rate limits, and redaction pass.
- [ ] Recognizable SQL records, administrator, credentials, config, release, and volume identities survive container restart, host reboot, and installer reruns.
- [ ] Missing SQLite/key/initialized PostgreSQL storage fails closed and never creates replacement state.
- [ ] Interrupt image pull and PostgreSQL initialization; verify actionable errors and safe reruns without data deletion or credential regeneration.
- [ ] Missing prerequisites, occupied ports, unsupported filesystems, and insufficient resources stop safely.
- [ ] PostgreSQL outage leaves the dashboard and login available while readiness fails.
- [ ] SQLite failure and migration failure never report readiness or authorize requests.
- [ ] External **and private-network** IPv4 probes cannot reach 5432, 3000, 2019, 8080, or 8081; repeat over IPv6 where enabled.
- [ ] Dashboard has no Docker socket, bootstrap credential, Caddy storage/admin access, or host firewall control.

IPv6 probes may be omitted only when IPv6 is disabled and its absence of exposure has been verified and recorded. Keep the IPv4 public and
private-network checks. Preserve release checksums and the existing automated checks; they are already part of the implementation.

## Phase 1 demo script

Show the verified HTTPS address, enter a token without recording its value, create the administrator, and inspect Overview and Settings. Stop PostgreSQL from the host to demonstrate degraded readiness with a usable dashboard; restart it and refresh. Show host diagnostics with secrets redacted. Do not present project provisioning, backups, recovery, or production readiness as implemented.

## MVP deployment evidence — 2026-09-20 (release v0.2.3)

Recorded from the workstation while driving the live servers through the public API and SSH; no secrets are included.

- **One-line install, Lightsail (`firstcommit-phase1`, Ubuntu 24.04 x86-64, 2 vCPU / 2 GiB):** the published bootstrap installed Docker, pulled the pinned images, verified HTTPS at `https://firstcommit.webbywasp.com`, and `sync-db-cert` delivered the Let's Encrypt certificate to PostgreSQL in the same run (about 4 minutes wall clock). A second fresh install of the same host after a wipe behaved identically.
- **Firewall:** TCP 80/443/5432 public, 22 restricted to operator addresses, the stale 3000 rule removed.
- **Direct TLS connection from the internet:** `psql "…?sslmode=verify-full&sslrootcert=system"` connected with TLSv1.3 / `TLS_AES_256_GCM_SHA384` and a chain verified by the system trust store. Connecting by IP failed hostname verification; `sslmode=disable` was rejected by `pg_hba.conf` ("no encryption"); a foreign database name was rejected.
- **Demo application (`demo/`, Node `pg`):** wrote three recognizable "Order" rows over TLS.
- **Backup to AWS S3 (`pgfy-backups-…`, private, SSE-S3, bucket-scoped IAM user):** the storage check passed all four steps; a manual backup completed in 0.8 s (3086-byte custom-format archive, SHA-256 recorded, row count 3 captured in the dump snapshot); archive and manifest listed in the bucket; the next scheduled backup was reported for 24 hours later.
- **Recovery on a replacement server (`firstcommit-recovery`, fresh Lightsail instance, tunnel mode):** with only the bucket settings entered, discovery listed the backup (3 minutes old); the restore into a new project completed in 2.0 s and verified row counts (3/3), ownership, and permissions. The demo application read the three orders and wrote a fourth through the SSH tunnel. Wall clock from administrator setup to verified restore: under 2 minutes; install about 1 minute before that.
- **Original server:** left running and unchanged during the rehearsal (the static-IP move is an operator step, see the runbook).

Issues found and fixed during this run: the application image lacked a CA bundle (object-storage TLS failed) — fixed in v0.2.2; libpq clients need `sslrootcert=system` for `verify-full` — connection examples updated in v0.2.2; tunnel-mode URLs now use `sslmode=disable` and project names may contain parentheses — v0.2.4.

Not yet covered: a non-AWS host/storage pair (AlphaVPS + B2) — configuration-only by design, but unexercised; IPv6; certificate renewal failure handling beyond the daily timer.
