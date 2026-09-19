# Implementation phases

Build a complete app around three journeys: create a database, connect safely, and recover after server loss.

Phases 1–4 implement the complete MVP. Phase 3 proves the highest-risk recovery path before scheduling and UI refinement. Phase 5 completes
the submission. Phase 6 is the mandatory gate before running the operator's own modest production applications under one trusted admin.
The submission deadline does not reduce MVP acceptance criteria or establish production readiness. PgBouncer is not a submission dependency.

The core setup is: install on a compatible VPS → configure an existing S3-compatible bucket in Settings → test storage → back up and recover.
Compute and storage are independent choices. Keep one installer and one configurable S3 client; no provider-specific application builds,
required AWS account, native storage adapters, or extra backup service. The initial second-host and second-storage checks remain MVP gates.
Use the small validation set in [Phase 1 validation](phase1-validation.md#portability-validation-targets); broader provider matrices can wait.

The operator supplies a VPS meeting the initial Ubuntu 24.04 LTS x86-64 requirements, root/sudo access, and DNS/firewall configuration
(or explicit SSH-tunnel access). Before enabling backups, they create a private S3-compatible bucket and scoped credentials with their chosen
provider. Pgfy installs and configures the software, then manages backups through Settings; it does not provision cloud infrastructure.

Build basic usability into every phase. Prepare the recognizable demo dataset and script immediately, record a usable version once recovery
works, and reserve roughly the final quarter of available time for deployment checks, fixes, and recording. A working recovery demo is an early fallback, not a claim
that unfinished scheduling or management features are complete.

## Phase 1 — Secure foundation

Goal: Install and access the base stack on a fresh Lightsail Linux instance using a provider-neutral installer.

Build:

- One Go backend using `net/http`, `pgx`, and `modernc.org/sqlite`, with embedded React/TypeScript/Vite/Tailwind/shadcn assets. Keep modules
  inside one application; no separate frontend server, queue service, or Redis.
- Versioned SQLite migrations, WAL mode, foreign keys, and a bounded busy timeout. Persist admin, hashed setup tokens/sessions, and management
  metadata. Migration failures prevent readiness. Establish encryption for recoverable secrets with a separately protected key outside SQLite.
- PostgreSQL 18.6 using `postgres:18.6-bookworm`, resolved to an immutable image digest in the release bundle. Mount `/var/lib/postgresql`,
  retaining `PGDATA=/var/lib/postgresql/18/docker`.
- Matching `psql`, `pg_dump`, and `pg_restore` bundled with runtime dependencies in the application image. Initially derive its runtime from
  the pinned PostgreSQL image, override the entrypoint to run Go as non-root, and validate tool versions. Backup execution starts in Phase 3.
- Three long-running Compose services: application, PostgreSQL, and Caddy; persistent PostgreSQL, SQLite, and Caddy storage. Separate the
  application/database network from Caddy's proxy network. No PostgreSQL host port mapping in Phase 1.
- A dedicated restricted PostgreSQL health-check role and SCRAM authentication. Keep bootstrap credentials in restricted secret files outside
  dashboard access. Add restart policies, bounded logs, request/subprocess timeouts, and health checks.
- Setup, login, status, and Settings screens with loading/failure states and actual component versions. Distinguish liveness from SQLite and
  PostgreSQL readiness. Keep the dashboard reachable when PostgreSQL is down; do not route Caddy based on database-dependent readiness.
- Single-admin setup with a random 30-minute token displayed in the installer terminal and entered in a form, never a URL. Store only its
  hash, and consume it with admin creation in one SQLite transaction. A host-only command replaces expired tokens only before setup.
- Argon2id passwords, hashed server-side sessions with 12-hour absolute expiry, logout invalidation, CSRF/origin checks, and setup/login rate
  limits. Use host-only, HttpOnly, SameSite cookies with Secure on HTTPS. Isolate any tunnel-only HTTP sessions from public HTTPS sessions.
- HTTPS-first installation: accept the dashboard hostname, write a fixed Caddyfile, obtain a certificate, then open HTTPS for admin setup.
  Without a domain, require explicitly enabled loopback-only access through an SSH tunnel. Never publish HTTP administration on IP:3000.
- Host-managed hostname configuration and recovery, displayed read-only in Settings. Domain changes use a host-side command with validation
  and a documented rollback path. No dashboard domain editor, public HTTP cutover workflow, HTTPS-confirmation API, or app access to Caddy admin.
- A versioned release bundle containing the installer, Compose configuration, image digests, and checksums. CI builds the app image; target
  servers need neither Go nor Node. Support installation from a downloaded bundle and default to `/opt/firstcommit`.
- A single copy-paste install command using a small bootstrap script downloaded with `curl` over HTTPS from the project's release channel.
  It downloads a selected versioned bundle and its checksum, verifies before extraction/execution, and invokes the existing installer.
  Support dashboard hostname or explicit SSH-tunnel mode and the installation directory option. Reuse existing installation logic and
  preserve release selection and data on reruns; never turn a rerun into an implicit update. Retain downloaded-bundle installation as an
  alternative. On success, print the dashboard address and setup instructions using the installer's existing token handling.
- Ubuntu 24.04 LTS on x86-64; Docker Engine 28+ and Compose 2.30+. Install a supported, tested Docker patch release from Docker's official
  repository for Ubuntu when absent, preserve compatible installations, and report incompatible ones without replacing them.
- Start validation with 2 vCPU, 2 GiB RAM, and 20 GiB free disk on both hosts. Publish measured results; these are provisional test minimums,
  not production capacity guarantees. Include active application traffic with backup/restore measurements in later phases.
- Preflight checks for OS/architecture, root/sudo access, occupied ports, persistent storage, resources, and required network/DNS connectivity.
  Lock concurrent installer runs; generate configuration and secrets only when absent. Preserve release selection and volume identities.
- Explicit detection of partial PostgreSQL initialization, including failed image initialization scripts on an already nonempty data directory.
  Stop with actionable diagnostics; never delete data, regenerate credentials, reopen setup, or perform implicit updates on reruns.
- No required cloud metadata, provider API calls, or instance identity in installation or core application logic.
- A Lightsail deployment guide with operator-managed firewall and static-IP setup, separate from generic installation instructions.

Expose only Caddy's public ports 80/443; any tunnel listener binds explicitly to loopback. Keep host recovery local and never automatically
reopen public HTTP administration. Do not give the dashboard Docker socket, host firewall, or Caddy administration access.

Use JSON under `/api/v1` for setup availability/creation, login/logout/session, authenticated system status, and read-only installation
settings. Keep `/health/live` and `/health/ready` minimal and unauthenticated, with redacted errors. There are no domain mutation or cutover
confirmation endpoints. Database provisioning, database TLS/access rules, and backup execution remain in their later phases.

Done when:

- The published one-command bootstrap reaches setup on both validation hosts without manual download/extraction. Download or checksum
  failures stop before invoking the bundle; reruns retain the installed release and state. Downloaded-bundle installation still works.
- Fresh installation passes authenticated PostgreSQL queries, SQLite checks, and Caddy-to-app routing before reporting success. Domain mode
  verifies HTTPS; tunnel mode reports its restricted access explicitly. Failed certificate issuance must not enable public HTTP setup.
- Admin setup rejects invalid, expired, reused, and concurrent tokens; exactly one admin is created. Test unauthorized access, session
  expiry/logout, CSRF/origin rejection, rate limits, and secret redaction.
- Container restarts, host reboot, and installer reruns preserve recognizable database records, credentials, configuration, and volume identities.
- The database is not externally reachable under the default configuration.
- External and private-network probes cannot reach PostgreSQL, Caddy admin, or loopback recovery listeners; test IPv6 where enabled.
- PostgreSQL failure causes degraded readiness while the dashboard remains accessible. SQLite failure never produces false readiness.
- The same release artifact installs on Lightsail and a second independent non-AWS Ubuntu environment without provider-specific code.
- Missing prerequisites, occupied ports, failed image pulls, and interrupted initialization produce actionable errors and safe reruns.
- Go tests, frontend type/build checks, setup/login browser tests, Compose validation, and exact database/tool-version checks pass.

After the Lightsail checks pass, Phase 2/3 implementation can proceed while second-host evidence remains pending. Both hosts must pass before
claiming Phase 1 deployment acceptance or a complete MVP. Keep probes on every enabled public/private network path; omit IPv6 probes only
when IPv6 is disabled and its absence of exposure is verified. Keep the existing release checksums and automated checks.

## Phase 2 — Create and connect safely

Goal: A developer creates a project with one name and connects a real application through the guided flow.

Build:

- Project creation/listing with generated database names and strong restricted credentials.
- Recoverable provisioning states: persist intended project identity before PostgreSQL operations and reconcile on retry. Do not assume
  SQLite/PostgreSQL share a transaction or that `CREATE DATABASE` can run in one. Test interruption between provisioning stages.
- A separate management role with explicit provisioning privileges; health checks remain narrowly privileged and the dashboard never receives
  bootstrap superuser credentials. Test project database/schema permissions and role memberships for cross-project access.
- Basic project readiness and database-size display.
- Protected direct connection URL, individual fields, and `psql` example.
- Per-project allowed IP/CIDR rules through a narrowly scoped mechanism limited to managed rule files, without PostgreSQL data-directory or
  arbitrary configuration access. Define validation, apply/reload, rollback, and real connection checks; `pg_hba_file_rules` alone only
  validates the file on disk and does not establish active enforcement.
- PostgreSQL TLS using a stable database hostname and publicly trusted certificate managed by Caddy. Prefer its standard HTTP/TLS-ALPN
  challenges when that hostname resolves to the server and Caddy is reachable on 80/443; do not require DNS-provider API credentials for
  ordinary installation. DNS-01 is an optional deployment path requiring the appropriate Caddy module and scoped DNS credentials.
- Use a small host-managed mechanism to deliver only the database certificate/key into a directory mounted read-only by PostgreSQL, with
  PostgreSQL-compatible ownership and permissions. Validate replacements, preserve the working pair, reload on change, and verify a new
  connection sees the replacement. Caddy manages issuance/renewal; the host mechanism handles delivery/reload. Do not expose Caddy's entire
  storage or administration to the app. No separate certificate-management service; dashboard HTTPS alone does not provide database TLS.
- Client examples that verify hostname and trust chain (`verify-full` for libpq), including CA trust configuration appropriate to the driver.
  Document a DNS-only database record for Cloudflare deployments; ordinary HTTP proxying does not support direct PostgreSQL connections.
- Documented operator-managed Lightsail firewall setup, generic provider/host firewall requirements, and an SSH-tunnel path for local development.
- A connection check the user runs from the application environment.

Explain the difference between the application host’s outbound IP and the developer’s current IP. Show “Database ready” separately from
“Application connected.” Explain that both the infrastructure firewall and PostgreSQL policy must allow access, and that rule removal does not
terminate already-established sessions. Lightsail's firewall covers public-IP traffic, not private-IP traffic; test PostgreSQL restrictions on
each enabled path and document any additional private-network controls.

Connect a small demonstration application that reads and writes recognizable records.

Done when:

- Two projects work with their own credentials, without cross-project connections or data access.
- A permitted application source connects with TLS and verified server identity.
- Certificate issuance, retry after issuance failure, and a valid replacement through the delivery/reload path work; invalid/expired
  certificates and hostname mismatches fail client verification. Preserve the working certificate until a valid replacement is ready.
  Full automatic-renewal failure/retry exercises remain in Phase 6.
- A disallowed source and a non-TLS remote connection are rejected; test IPv6 too if enabled.
- Invalid rule changes preserve the previous working policy and administrative access.
- The SSH-tunnel path works without a public database port.
- The actual network behavior matches the documented Lightsail and Docker configuration; generic instructions identify equivalent controls.
- Secrets do not appear in logs, and unauthenticated users cannot retrieve them.

## Phase 3 — Prove disaster recovery early

Goal: Recover one real database on a replacement server without the original server or SQLite file.

Build the smallest end-to-end recovery path:

- Configurable S3-compatible backup location and scoped access; use AWS S3 for the demo and configure replacement-server authorization independently.
- Storage settings for HTTPS endpoint, signing region, bucket/prefix, supplied credentials, optional session token, and addressing mode.
- One S3 client uses these settings for every store. Do not hardcode AWS endpoint/region lists or require IAM APIs, object ACLs, tagging, KMS,
  bucket provisioning, or provider-native APIs. The operator supplies an existing private bucket and scoped credentials.
- Explicit scoped storage credentials on Lightsail; no dependency on EC2 instance roles or AWS-specific storage-management APIs.
- A storage check exercising upload, list/discovery, download/integrity verification, and temporary-object cleanup in the configured prefix.
- Portable object operations, manifests, and integrity checks; define backup size limits and test multipart upload/abort where required.
- Manual backup using bundled tools invoked locally with fixed argument arrays and protected credential files; no Docker execution or shell
  interpolation. Use one custom-format archive per database and a bounded workspace with free-space checks and enforced size limits.
- A versioned manifest containing project identity, PostgreSQL version, timestamp, SHA-256 integrity information, and supported ownership
  mappings. Upload the archive first and publish the completed manifest last; discovery excludes incomplete work.
- Durable SQLite job records and one worker in the Go process, independent of browser sessions. Persist real stages, elapsed time, and errors.
  Reconcile actual work after restart and mark unfinished jobs interrupted; never report false success or start duplicate heavy work.
- Backup discovery from a fresh installation.
- Restore into a new database, with explicit compatibility checks.
- Verify archive integrity before restoration. Recreate required ownership/permissions with new project credentials and restricted target
  privileges, not the bootstrap superuser. No dependency on the lost server's installation encryption key.
- Defined schema, data, and permission checks with recorded results. Capture automated baselines using the same exported snapshot passed to
  `pg_dump --snapshot`; a controlled quiescent demo dataset is an explicitly documented alternative. Do not compare against unrelated live counts.
- A written recovery runbook and separately retained endpoint, region, bucket/prefix, addressing mode, and credential-recovery information.

A minimal operator-driven flow is acceptable for this milestone. Do not wait for scheduling, charts, or polished recovery screens. Only one
heavy backup/restore operation runs at a time. Do not store recoverable application passwords in backup metadata.

Rehearse:

1. Write recognizable application records and capture checks against a consistent backup dataset.
2. Complete an external backup.
3. Make the original server unavailable.
4. Install on a replacement server without copying the original SQLite file.
5. Authorize storage access, discover the backup, and restore into a new database.
6. Review/reapply allowed addresses and configure the new server’s firewall and TLS. Optionally reassign the Lightsail static IP; generic recovery
   must also work with a new address and updated application configuration.
7. Update application connection details and verify recovered records plus a new read/write.
8. Record backup age, verification results, and recovery time, stating any excluded preparation.

Done when: The entire recovery succeeds using only external backups, separately retained recovery information, and the runbook. The original
database remains unchanged if still available. Unsupported restore requirements fail clearly. Changes after the backup are not presented as
recoverable.

Also test browser closure and process interruption during dump, upload, and restore; incomplete manifests/objects; insufficient workspace;
and safe retry without overwriting the original database. Bound subprocess execution and clean temporary work when safe. The durable worker
is part of this milestone, not deferred to dashboard polish.

Record a usable demo immediately after the first successful AWS recovery; do not wait for portability checks or dashboard refinement.

Portability gate: Repeat backup creation, discovery, and restoration against a non-AWS S3-compatible endpoint using configuration changes only.
Restore to the second compatible Linux environment without requiring the original host's provider, identity, address, or metadata. Check wrong
credentials, an invalid endpoint, missing object permissions, and integrity failures. Record tested configurations and limitations without
naming alternative providers or making price comparisons in public product copy. Cloud independence is part of this phase, not future work.

This is the core technical proof; Phase 4 turns it into the complete dashboard experience.

## Phase 4 — Complete the management experience

Goal: Make creation, connection, backup, and recovery coherent dashboard journeys.

Build:

- Four navigation areas: Projects, Project details, Recovery, and Settings.
- Provider-neutral storage setup with AWS defaults, custom endpoint/region/addressing controls, masked credentials, and the storage connection check.
  All storage configuration is available in Settings; choosing a compatible store requires no code, image, or Compose changes. Use one
  storage form and the same worker for every endpoint, with addressing mode and optional session token under advanced settings.
- A simple daily backup schedule inherited by new projects once storage is configured, enqueueing the same durable jobs used by manual backup.
- “Back up now,” backup history, and clear missing-storage or backup-failure states.
- Dashboard integration with the Phase 3 persistent worker; keep one heavy job at a time and browser-independent execution.
- Real stages, elapsed time, and errors; no fabricated progress percentages.
- Visible interrupted-job results and safe retry using the Phase 3 reconciliation behavior.
- “Recover from existing backups” on fresh installations, with backup selection and compatibility information.
- Restore into a new database, credential handoff, and application-reconnection guidance.
- Separate “Backup completed” and “Restore verified” statuses tied to specific backups/attempts.
- Individual verification results for restore, expected schema, recorded data checks, and permissions; application-side evidence for reconnection.
- Clear empty, loading, failure, and completion states in every required journey.

Do not use a blanket “Secure” status. Distinguish configured restrictions from observed connectivity. State that retention is not automated yet
and provide operator cleanup guidance; the demo must not imply indefinite backup storage is free.

Done when:

- The Phase 3 recovery works through the dashboard, without undocumented operator steps.
- A scheduled backup reaches the configured store and can be discovered and restored on a fresh installation; exercise AWS S3 and the second
  S3-compatible endpoint through the same application path.
- Closing the browser does not stop jobs; restarting the backend does not produce false success or duplicate heavy jobs.
- Failed backups/restores are visible and can be retried safely without overwriting the original database.
- Verification results name the checks actually performed and do not imply complete application correctness.
- The demonstration application reconnects using the newly generated credentials.

## Phase 5 — Demo and submission

Goal: Submit a reproducible working product with a recording under three minutes.

Prepare the script and architecture diagram alongside earlier phases. Freeze feature scope early enough to reserve roughly the final quarter
of the available time for verification, fixes, and recording.

The video should show:

1. The personal problem and intended users.
2. Two projects, simple creation, and guided connection details.
3. A successful allowed application connection and rejection from an unapproved source.
4. Recognizable application data backed up to S3.
5. The original server unavailable and recovery onto a replacement server.
6. New application connection details, recovered records, backup age, verification results, and measured recovery time.

Show AWS use explicitly. If recovery footage is compressed, label the elapsed time and any preparation omitted from the timing.

Complete:

- A public repository, installation/recovery instructions, and implemented-versus-planned feature list.
- An AWS Lightsail + S3 demo architecture diagram and actual cost estimate with workload assumptions, storage/backups/network costs, and
  maintenance tradeoffs. Explain the generic Linux VPS and S3-compatible storage interfaces without naming alternative providers or comparing prices.
- A short writeup explaining the problem, AWS use, and one concrete lesson learned.
- AI-tool disclosure and third-party credits as required by the event rules.
- A YouTube recording under three minutes, public or unlisted, with signed-out access checked.
- The event submission before its published deadline.

Done when: A complete submission exists, all demonstrated behavior was tested, and claims match what the video and implementation show.

If time tightens, cut CPU charts, detailed connection analytics, downloadable exports, elaborate version notices, and visual extras first.
Do not cut access restrictions or truthful recovery checks. Do not add PgBouncer, a SQL editor, team permissions, or one-click updates before
submission. Clearly identify any remaining required feature as unfinished rather than calling a partial app complete.

## Phase 6 — Post-demo hardening and operation

Goal: Run one low-stakes application with tested recovery and explicit maintenance responsibilities.

### Production gate

Start post-demo work with retention, external alerts, abandoned-work cleanup, the manual update procedure, and runbook-only recovery.
Every requirement below remains mandatory before real workloads; this ordering does not make the other checks optional.

- Review setup-token recovery, credentials at rest and in logs, TLS/certificate renewal, permissions, and actual network exposure.
- Exercise automatic database certificate renewal, failure/retry, and credential rotation without losing administrative access. Verify the
  installation key is protected separately from SQLite and fresh-server recovery does not depend on it.
- Test crashes, full disks, corrupt backups, expired storage authorization, and incompatible restore environments.
- Clean up abandoned temporary files and incomplete uploads; implement and test backup retention.
- Add external alerts for downtime, stale/failed backups, and low disk space.
- Test a documented manual update procedure and failed-update recovery.
- Assign maintenance ownership for the app, PostgreSQL, container images, Docker, and host OS.
- Repeat fresh-server recovery using only the runbook and independently retained recovery information.
- Define acceptable data loss and recovery time for the first application and verify the results meet them.
- Measure resource use with the intended application workload during backup and restore, including connection budgets, disk workspace, and
  log growth. Do not treat the Phase 1 2 GiB validation host as an established production capacity.

A demo does not establish production readiness. Move one low-stakes application only after the complete MVP and this gate pass; expand after
observing backups, recovery, and maintenance in actual operation. Keep the same application architecture; secret protection, bounded logs
and temporary storage, timeouts, migrations, and durable jobs are already MVP foundations rather than a production rewrite.

### PgBouncer follow-up

Introduce pooling as a separate, tested change after submission:

- Pin a separate PgBouncer container and offer pooled alongside direct connection details.
- Integrate project provisioning and credential changes with pooler authentication/configuration.
- Enforce and test source restrictions at the actual client-facing boundary; PostgreSQL may see the pooler’s IP rather than the client’s.
- Set connection budgets that reserve administrative and recovery capacity.
- Keep backup/restore jobs and session-dependent workloads on protected direct connections.
- Verify both projects’ authentication, isolation, credential changes, connection limits, and recovery on a replacement server.

Pooling must not weaken the existing access policy or recovery flow. Do not promise transparent transaction retries or zero-downtime restarts.

### Further improvements

Prioritize according to observed usage:

- Automated recurring restore verification.
- Richer monitoring and capacity guidance.
- Expansion of the tested OS/provider/storage compatibility matrix beyond the provider-neutral MVP's initial validation.
- Customer-triggered updates via a restricted host-side updater independent of the dashboard, with approved pinned images, prerequisite and
  readiness checks, durable results, and measured downtime.
- A formal patch-management policy and PgBouncer-assisted maintenance coordination.

Keep database updates within the supported major version. Automated major upgrades, high availability, and point-in-time recovery require
separate designs and remain outside this plan’s MVP.

Working rule: Prioritize demo-visible behavior and security/data-correctness checks; defer visual extras and broader compatibility matrices.
Track pending evidence explicitly, using the Phase 1 sequencing exception to unblock recovery work. Complete the initial portability checks
before calling the MVP complete. Prove recovery early, complete the user journeys, and protect time for a clear submission.
