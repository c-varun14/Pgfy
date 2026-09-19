# Project brief: Self-hosted PostgreSQL with verified recovery

Hackathon: WeMakeDevs — AWS First Commit

## Product promise

> Run your small projects’ PostgreSQL databases on a server you control—with guided connections, restricted access, and recovery you can verify.

A self-hosted dashboard turns one VPS into an easy-to-manage PostgreSQL server for multiple small applications. Users own their server and data,
and pay their infrastructure provider directly.

The setup flow is: install on a compatible VPS, open Settings, enter an existing S3-compatible bucket's endpoint and credentials, test the
connection, and enable backups. The VPS and storage provider are independent choices. Use the same release and application paths for each;
switching providers requires configuration, not a custom build or another service.

The MVP is complete when three journeys work end to end:

1. Create a project database.
2. Connect an application safely.
3. Recover the database and reconnect the application after losing the server.

Affordability explains why someone tries it. Easy management and trustworthy recovery explain why they keep using it.

## Problem and intended users

As a student and agency owner, I build several SaaS applications and client projects. Separate managed database services can become expensive
for projects at this stage. Sharing a VPS can spread infrastructure costs, but access control, backups, patches, and recovery create work.

This product serves students, indie developers, and small agencies with several modest PostgreSQL workloads. It reduces that operational work
without pretending to remove it. Cost comparisons must state workload assumptions and include compute, storage, backups, and relevant network
charges; savings are not guaranteed for every workload.

The first production target is the operator's own modest applications under one trusted administrator. Build the full MVP, then pass the
production gate before moving real workloads. A hackathon submission is a separate milestone; its deadline does not reduce MVP acceptance
criteria or establish production readiness. A public self-service database service and hostile-tenant isolation are outside this design.

## Architecture and limits

Each installation runs one PostgreSQL instance with a separate database and restricted credentials per project, plus a management dashboard
for provisioning, access rules, and backup/restore jobs. SQLite stores management metadata on a persistent volume.

Use three long-running Compose services: the Go application, PostgreSQL, and Caddy. Embed React assets in Go and keep authentication,
projects, database operations, storage, jobs, and recovery as internal modules of that one application. Use a durable SQLite job table and
one in-process worker; no separate queue service, Redis, production Node server, or container orchestrator is required.

SQLite keeps authentication and management state independent of PostgreSQL availability. Distinguish process liveness from dependency
readiness. A PostgreSQL outage must appear as degraded health without making Caddy withdraw the dashboard or preventing diagnosis and
recovery. SQLite migration failures must prevent readiness; do not report success from container state alone.

The hackathon uses direct PostgreSQL connections. PgBouncer is post-demo work, not a submission dependency or an implemented-feature claim.
Direct connection budgets must still leave capacity for administration, backups, and recovery.

Projects share CPU, memory, storage, and the server’s failure risk. Credentials provide logical access separation, not dedicated compute or
hard resource isolation. Applications run elsewhere. The database does not intentionally sleep, but one VPS does not provide high availability.

The demo uses an AWS Lightsail Linux instance in US East — N. Virginia and AWS S3 for external backups. These are demonstration choices,
not application dependencies. Users provision the server and network access controls; the installer configures the software.

Provider independence is an MVP requirement. Initially support Ubuntu 24.04 LTS on x86-64, Docker Engine 28+ and Compose 2.30+, with persistent
local storage, administrative installation access, and documented network requirements. Install a supported, tested Docker patch release;
the minimum version is not the recommended patch version. Start validation at 2 vCPU, 2 GiB RAM, and 20 GiB free disk. These are provisional
test minimums, not production capacity claims; measure active application traffic together with backup and restore workloads on both test
hosts. The installer must work without cloud-specific metadata, APIs, instance identities, or bundled services.

Compute and backup storage are independent choices. A compatible Linux VPS must be able to use a separately selected S3-compatible endpoint.
Keep provider-specific firewall instructions in deployment guides, outside core application logic. Validate portability on a second compatible
Linux environment and a non-AWS S3-compatible endpoint before claiming it tested; document the tested configurations rather than promising
universal compatibility. Public product copy describes capabilities and requirements without naming alternative providers or comparing prices.

The initial second-host and second-storage validation remains part of the MVP, not post-production work. AWS is the first demo target.
See the [validation targets](phase1-validation.md#portability-validation-targets) for the small initial set. Broader provider coverage can wait.
Compatibility means meeting the documented OS, Docker, storage, networking, and S3-operation requirements; a provider label alone is not proof.

For Lightsail, document an operator-managed static IP and public-IP firewall. A static IP can be reassigned during replacement-server recovery,
but data restoration, TLS readiness, and application credential updates are still required. Static-IP reassignment is optional and must not
become a dependency of the generic recovery procedure.

## Core user experience

### Installation and setup

The operator prepares a compatible Ubuntu 24.04 LTS x86-64 VPS with root/sudo access, a dashboard DNS record and firewall rules, or explicit
SSH-tunnel access without a domain. Before enabling backups, they supply an existing private S3-compatible bucket and scoped credentials.
Pgfy handles software installation and backup management; creating the VPS, DNS records, firewall rules, bucket, and credentials stays with
the operator. Storage is configured later in Settings and is not required to install the dashboard.

- Run one copy-paste command that fetches a small bootstrap script with `curl` over HTTPS. The script downloads a selected versioned release
  and checksum, verifies the bundle before extraction/execution, and invokes the existing installer with the dashboard hostname or explicit
  tunnel mode. No manual download/extraction is required for this path. Reruns preserve data, configuration, secrets, and release selection.
  Keep downloaded-bundle installation available as an alternative; print the dashboard address and setup instructions on success.
- Obtain dashboard HTTPS before entering the setup token or admin credentials: install → open HTTPS → create admin.
- Without a domain, explicitly enable loopback-only setup through an SSH tunnel. Never expose public HTTP administration on IP:3000.
- Keep the hostname in host-managed installation configuration. Settings displays it; a host-side command handles domain changes and recovery.
  Use a fixed Caddyfile and persistent certificate storage. The application does not control Caddy's administration API or implement browser
  domain editing, HTTP-to-HTTPS cutover, or an HTTPS-confirmation endpoint.
- Complete admin setup with a single-use, 30-minute token entered in the form, never in a URL. Store only its hash and consume it atomically
  with creation of the sole admin. A host-only command can replace an expired token before setup; reruns never reopen registration.
- Use Argon2id password hashes, hashed server-side sessions with a 12-hour absolute expiry, logout invalidation, CSRF and origin checks, and
  setup/login rate limits. Use host-only, HttpOnly, SameSite cookies with Secure on HTTPS; any tunnel-only HTTP mode has its own session scope.
- Confirm service health before reporting installation success.
- Configure external backup storage once, or show a clear “Backups not configured” state until it is configured.
- Offer “Recover from existing backups” on a fresh installation.

Distribute a versioned, checksummed release bundle with pinned images; installation requires neither Go nor Node on the server. Default to
`/opt/firstcommit`, lock concurrent runs, preserve compatible Docker installations, and check prerequisites before changing services. Detect
partial PostgreSQL initialization explicitly: image initialization scripts do not resume automatically on a nonempty data directory. Stop
with actionable diagnostics without deleting volumes or regenerating existing credentials. Keep updates separate from installer reruns.

### Create and connect

Creating a project requires only its name. Generate the database name and strong credentials automatically. Show progress, errors, and a
recoverable result if provisioning is interrupted; retrying must not silently create duplicate projects.

Persist the intended project identity before database creation and reconcile actual PostgreSQL state on retry. SQLite and PostgreSQL cannot
share one transaction, and PostgreSQL `CREATE DATABASE` cannot run inside a transaction block.

The connection panel guides the user through:

1. Allow access from the application server’s outbound IP or use the documented SSH-tunnel path for local development.
2. Copy a direct connection URL, individual fields, or a ready-to-use `psql` example with certificate-verification instructions.
3. Run the supplied connection check from the actual application environment.

“Database ready” means provisioning succeeded. “Application connected” requires evidence from the application environment. A backend health
check does not prove external connectivity. Mask credentials by default, restrict reveal/copy actions to the authenticated admin, and keep
secrets out of logs and URLs used for dashboard navigation.

### Manage databases

Keep navigation to four areas:

| Area | Purpose |
| --- | --- |
| Projects | Create databases and see readiness, size, and backup status |
| Project details | Connection details, credentials, allowed addresses, and backups |
| Recovery | Discover backups, restore into a new database, and inspect verification |
| Settings | Backup storage, daily schedule, and basic server information |

A useful project summary is: “Database ready · Access restricted · Last backup 12 minutes ago · Restore not yet verified.”

Statuses must describe actual evidence. Do not display a blanket “Secure” badge because one access rule exists. Show configuration state
separately from connectivity checks. Provide clear empty, loading, failure, and completion states throughout.

## Database access and security

IP restrictions are part of the MVP. They complement authentication, TLS, and database permissions.

### Default policy

- Remote database access is disabled until explicitly configured.
- Allow specific application-server IPs or narrow CIDR ranges. No “allow everyone” shortcut in the normal setup flow.
- Require TLS for remote database connections, using a stable database hostname and a publicly trusted certificate managed by Caddy. Use
  standard HTTP/TLS-ALPN challenges when reachable; ordinary installation must not require DNS-provider API credentials. DNS-01 is optional
  and needs the appropriate module and scoped credentials. A small host-managed mechanism delivers only the database certificate/key with
  correct permissions to a directory mounted read-only by PostgreSQL, preserves valid files on failure, and reloads on replacement. Caddy
  renews certificates; delivery/reload must be tested separately. Dashboard HTTPS alone does not provide database TLS.
  Document hostname and trust-chain verification for each supported client (`verify-full` for libpq), including system CA configuration
  where supported. Encryption-only settings such as `sslmode=require` do not establish server identity.
- Use separate restricted project credentials; never expose the management account as an application connection string.
- Enforce database and schema permissions so one project cannot connect to or access another project’s data.
- Support an SSH tunnel for local development without requiring a public database port.
- Keep dashboard authentication and database access policies separate so changing database rules does not lock out dashboard administration.

Explain that a deployed application normally connects from its host’s outbound IP, not the developer’s laptop IP. If offered, label
“Allow my current IP” specifically for connections from that computer. Changing outbound addresses require a stable-egress or private-network
arrangement; automatic support for those environments is outside the MVP.

For the Cloudflare deployment guide, use a DNS-only database record for direct PostgreSQL connections. The ordinary HTTP proxy does not
proxy PostgreSQL. Keep this provider-specific instruction outside application logic.

Use separate health-check, management, and project credentials. The management role needs explicit provisioning privileges beyond health
checks; do not give the dashboard the bootstrap superuser credential. Define and test the privilege model before project provisioning.
Encrypt recoverable project and storage credentials with a separately protected installation key outside SQLite. Keep passwords, tokens,
and keys out of logs; hashing remains appropriate for admin passwords and session/setup tokens. Encryption does not protect secrets from
a compromised running application. Fresh-server restore generates new project credentials and must not require the lost installation key.

### Enforcement and responsibility

| Layer | Responsibility |
| --- | --- |
| Provider and/or Docker-aware host firewall | Operator restricts which sources can reach the database port; the demo guide uses the Lightsail firewall |
| PostgreSQL `pg_hba.conf` and permissions | Dashboard manages per-project database/user/address rules; remote rules require TLS |

Both layers must allow a connection. Adding an address in the dashboard does not automatically open the infrastructure firewall. The dashboard
must explain this distinction and must not claim to have verified rules it has not inspected. Lightsail's firewall filters public-IP traffic,
not traffic through the instance's private IP. Apply PostgreSQL access rules on both paths and document any additional private-network firewall
controls. Other deployment guides must identify their actual enforcement boundaries too.

Apply database rules through a narrowly scoped mechanism: validate addresses and project identifiers, generate rules without arbitrary config
injection, preserve required administrative access, and check reload results. Invalid changes must leave the previous working policy intact.
Limit writable configuration to managed access-rule files; do not expose the PostgreSQL data directory or arbitrary server configuration.
Define validation, apply, reload, rollback, and connection checks together. `pg_hba_file_rules` describes the file on disk, not necessarily
the active policy, so validation alone is not proof of enforcement. Do not give the web application Docker socket or host firewall access.
Document that removing an allowlist entry blocks new connections; existing sessions need separate handling and must not be described as
immediately revoked.

Verify real connections from both allowed and disallowed sources, including IPv6 where enabled. Docker-published ports can bypass ordinary
UFW filtering, so configuration inspection alone is not sufficient. Document and test the actual provider firewall and Docker network path.
Omit IPv6 probes only when IPv6 is disabled and its absence of exposure is verified.

## Backups and recovery

Configure S3-compatible storage once; use AWS S3 for the demo. Storage setup accepts an HTTPS endpoint, signing region, bucket, optional object
prefix, access key ID, secret access key, optional session token, and path-style or virtual-hosted addressing. Provide AWS defaults without
hardcoding AWS domains or region lists. Validate endpoint configuration and restrict it to intended storage destinations; it must not expose
cloud metadata or management services through arbitrary backend requests.

Expose these settings in the dashboard using one storage form and one S3 client. Keep addressing mode and optional session token under
advanced settings. The operator creates the private bucket and scoped credentials with their chosen provider; the app handles its own
backup uploads, discovery, restoration, and cleanup. No provider-specific image, Compose edit, or separate backup agent is required.

Keep the bucket private and access scoped to the required backup location and operations. For Lightsail, configure dedicated scoped storage
credentials explicitly; do not assume an EC2 instance role is available. Support supplied credentials as the portable baseline; workload
identity or a credential provider chain can be optional conveniences on supported deployments. Never require an AWS account for installation
or a non-AWS backup destination. Protect stored credentials, mask them in the UI, and redact them from logs and errors.

Use a small S3 API subset for upload, list, download, and cleanup of failed work; support multipart upload and abort where required by the
documented backup size limit. Do not depend on AWS-specific IAM APIs, object ACLs, tagging, KMS, event notifications, or bucket provisioning in core
backup logic. Use application manifests and portable integrity checks rather than assuming an object ETag is a file checksum. Document any
endpoint-specific limits and optional features separately.

Provide a storage connection check that exercises upload, discovery, download/integrity verification, and cleanup of a temporary object in
the configured prefix. Authentication success alone is insufficient. The same backup, scheduling, discovery, and restore paths must work
against AWS S3 and a separately configured non-AWS S3-compatible endpoint without code changes.

A replacement server must receive its own authorized storage access without depending on secrets held only on the lost server. Retain the
endpoint, region, bucket/prefix, addressing mode, and a way to obtain credentials outside the original server. Backup manifests must not
require the original provider's instance ID, IP address, account identity, or local SQLite metadata to be restored.

Once storage is configured, new projects inherit a simple daily backup schedule. Always provide “Back up now,” backup history, and visible
failures. Before retention is implemented, clearly document that backups accumulate and require operator-managed cleanup.

Use one custom-format `pg_dump` archive per database. Bundle matching `psql`, `pg_dump`, `pg_restore`, and their runtime dependencies in the
application image so Go can execute them locally against PostgreSQL without Docker access. Initially derive the application runtime from
the pinned PostgreSQL image, override its entrypoint to run Go as a non-root user, and run the database only in its separate service. Invoke
tools with fixed argument arrays and protected credential files, never shell-built commands containing user input or secrets.

Stage archives in a bounded workspace, check free space before starting, enforce documented size limits, and calculate SHA-256 integrity
checks. Upload the archive before publishing its completed manifest. Discovery lists completed manifests, not incomplete uploads; restore
must verify the downloaded archive against the manifest before executing it. Retain actionable interrupted states and clean up temporary
files and failed uploads when safe. Full orphan reconciliation and retention are required at the production gate.

Backups include a versioned manifest identifying the project, PostgreSQL version, backup timestamp, integrity information, and supported
ownership and permission mappings. The MVP supports databases created by this platform; arbitrary custom roles, extensions, and external
dependencies are not automatically recoverable. Record requirements and reject unsupported restore environments clearly.

Restoration:

- Discover backups from a fresh installation without the original SQLite file.
- Show the source project, backup timestamp, and compatibility information.
- Restore into a new database by default, preserving the original if it still exists.
- Recreate the required ownership and permissions with newly generated database credentials. Do not require recoverable application
  passwords in backup metadata or promise that old connection strings keep working.
- Run restoration with controlled target ownership and restricted privileges, not the bootstrap superuser; support platform-created backups
  rather than arbitrary untrusted archives.
- Keep remote access closed until the user reviews and reapplies allowed addresses and configures the replacement server’s firewall and TLS.
- Show real stages: downloading, restoring, verifying, ready; show elapsed time and errors instead of invented percentages.
- Present fresh connection details and guide the user through updating application configuration.

Backup/restore jobs persist their status and run independently of browser sessions. Run only one heavy job at a time. After process or host
interruption, reconcile actual results and mark unfinished work interrupted instead of reporting false success.
Build this SQLite-backed worker in Phase 3 alongside manual recovery. Phase 4 adds scheduling and UI around the same execution path.

### What “verified” means

“Backup completed” means a backup was created and uploaded. “Restore verified” applies to a particular backup and restore attempt whose
defined checks passed; it must not imply that later backups have also been tested.

Display individual results for successful restore, expected schema, recorded data checks, and ownership/access checks. Record check baselines
against the same exported snapshot used by `pg_dump --snapshot`, or explicitly identify a controlled quiescent demo dataset, so live writes
do not produce misleading comparisons. Do not compare a dump with independently gathered live counts. Verify recognizable
records and an application read/write through a separate application-side check; do not label the application reconnected until it succeeds.

These checks demonstrate restorability, not complete application correctness. Automated recurring restore verification is future work.

Keep the recovery runbook, storage access information, and any required recovery secrets outside the original server. Measure recovery from
the start of the documented recovery procedure through a successful application check, and disclose any preparation excluded from the timing.
Show backup age and explain that writes after the backup are not recovered.

## Technical stack

| Component | Choice |
| --- | --- |
| Backend | Go for API, provisioning, and background jobs |
| Frontend | React + TypeScript + Vite + Tailwind + shadcn/ui |
| Delivery | Static frontend embedded in Go; no production Node server |
| Management metadata | SQLite on a persistent volume |
| Database | PostgreSQL 18.6 (`postgres:18.6-bookworm`) pinned by immutable image digest |
| Backup/restore | Matching tools bundled in the application image; custom-format dumps and versioned manifests |
| External storage | S3-compatible storage; AWS S3 for the demo |
| Demo compute | AWS Lightsail Linux instance; generic Linux VPS requirements for installation |
| Installation | Docker Compose and versioned one-command installer; Ubuntu 24.04 LTS x86-64 |
| Dashboard HTTPS | Caddy with a host-managed fixed configuration; HTTPS before admin setup |
| Database TLS | Caddy-managed certificate with restricted host-managed delivery and PostgreSQL reload |
| Connection pooling | PgBouncer after the demo; direct connections for the MVP |

## Hackathon deliverable

Install → create two projects → allow an application source → connect directly with TLS → back up to S3 → lose the original server →
recover on a replacement → update application credentials → verify recovered data.

The recording should show the personal problem, simple database creation, an allowed connection and a rejected unapproved source, S3 backup,
replacement-server recovery, and recognizable recovered records. Show AWS use, backup timestamp, verification results, and measured recovery
time. Explain one concrete lesson learned and provide an architecture diagram and cost estimate with explicit assumptions.

Prepare the recognizable dataset and script alongside development and record a usable recovery demo as soon as the first AWS recovery works,
without waiting for the portability checks or dashboard refinement. Reserve roughly the final quarter of the available time for deployment
checks, fixes, and submission. The final recording must be under three minutes; disclose time compression.

## Scope boundaries and later work

Required for the demo: secure setup, project separation, guided direct connections, IP restrictions, manual and daily external backups,
persistent job status, fresh-server recovery, and explicit verification results.

Defer CPU charts, detailed connection analytics, downloadable exports, elaborate version notices, and visual extras if time tightens.
Exclude a SQL editor, multiple database engines, team permissions, and destructive database-management workflows from the demo.

PgBouncer, recurring restore verification, customer-triggered updates, and expansion of the tested compatibility matrix follow the submission.
Provider-neutral installation and configurable S3-compatible backups are already MVP requirements. Application hosting,
billing, automatic cloud provisioning/firewall editing, seamless resizing, high availability, automatic failover, point-in-time recovery, and
automated major-version upgrades remain outside the MVP.

Before running one low-stakes production application, test retention, backup integrity, external alerts, cleanup of incomplete work, crashes,
disk exhaustion, corrupt backups, expired storage access, incompatible restores, and a documented manual update procedure. Review setup-token
handling, credential storage, TLS, access rules, and actual external exposure. Exercise certificate renewal and credential rotation. Define
acceptable data loss and recovery time and measure them with the intended workload. This gate is mandatory even if the demo is complete.
Prioritize retention, alerts, abandoned-work cleanup, manual updates, and runbook-only recovery after the demo; all production checks remain
required. Phase 2 proves certificate issuance and replacement/delivery/reload; Phase 6 exercises full automatic-renewal failure and retry.

Build versioned metadata migrations, secret encryption, request/subprocess timeouts, bounded temporary storage, and container log rotation
into the MVP as those components are introduced. The production gate adds operational evidence and controls to the same architecture.

Maintenance ownership covers the app, PostgreSQL, container images, Docker, and the host OS. Start with tested manual updates; database updates
can interrupt connections. Future customer-triggered updates use a restricted host-side updater, approved pinned images, prerequisite checks,
readiness checks, and measured downtime. Keep PostgreSQL updates within the supported major version; major upgrades need a separate migration.

When adding PgBouncer, test project authentication, credential changes, source restrictions at the actual client-facing boundary, connection
budgets, and replacement-server recovery again. PostgreSQL behind a pooler may see the pooler’s address rather than the application’s address.
Keep direct access for session-dependent workloads and administrative jobs. Do not promise transparent transaction retries or zero downtime.

## References

- [PostgreSQL 18.6 release notes](https://www.postgresql.org/docs/release/18.6/)
- [PostgreSQL official image and storage layout](https://github.com/docker-library/docs/blob/master/postgres/README.md)
- [PostgreSQL client authentication rules](https://www.postgresql.org/docs/18/auth-pg-hba-conf.html)
- [PostgreSQL access-rule validation](https://www.postgresql.org/docs/18/view-pg-hba-file-rules.html)
- [PostgreSQL client TLS verification](https://www.postgresql.org/docs/18/libpq-ssl.html)
- [PostgreSQL server TLS configuration](https://www.postgresql.org/docs/18/ssl-tcp.html)
- [PostgreSQL dump and snapshot support](https://www.postgresql.org/docs/18/app-pgdump.html)
- [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https)
- [Docker port publishing and loopback binding](https://docs.docker.com/engine/network/port-publishing/)
- [Cloudflare proxy limitations](https://developers.cloudflare.com/dns/proxy-status/limitations/)
- [Lightsail firewall behavior](https://docs.aws.amazon.com/lightsail/latest/userguide/understanding-firewall-and-port-mappings-in-amazon-lightsail.html)
- [Lightsail networking and static IPs](https://docs.aws.amazon.com/lightsail/latest/userguide/amazon-lightsail-faq-networking.html)
- [Docker packet filtering and firewalls](https://docs.docker.com/engine/network/packet-filtering-firewalls/)
- [First Commit judging criteria](https://www.wemakedevs.org/aws/first-commit)
- [First Commit submission rules](https://www.wemakedevs.org/aws/first-commit/rules)
