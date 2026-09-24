# Production gate checklist

The [production gate](phases.md#production-gate-unchanged-acceptance-list) must pass before `mvp-to-production` merges
to `main` and before real client databases move onto Pgfy. Tier 0 built what each item needs; this page records, per
item, what automated tests already show and what still has to be done on real servers or over time. "Pending" items are
not claims: they are the work left.

| Gate item | Covered by automated tests | Still to do (by the operator) |
| --- | --- | --- |
| Review setup-token recovery, credentials at rest and in logs, TLS/certificate renewal, permissions, network exposure | Integration: setup-token and reset flows, secrets absent from container logs, loopback-only ports, no Docker socket or bootstrap credential in the app, sealed credentials and webhook URL never returned | **Pending:** a review on the production host of file permissions, `ss -lntp` exposure (IPv4 and IPv6) and the provider firewall |
| Automatic database certificate renewal, failure/retry, credential rotation without losing access; installation key separate from SQLite; fresh-server recovery without it | Unit: every certificate delivery outcome recorded; expiry read from the served certificate; alerts for expiry and failed delivery. Integration: rotation ends old sessions; recovery from the bucket alone | **Pending:** observe one real renewal and one forced delivery failure (e.g. block port 80) on the HTTPS host, with its alert |
| Crashes, full disks, corrupt backups, expired storage authorization, incompatible restore environments | Integration: restart during upload, archive-only and manifest-only objects, integrity failures, a restore that cannot be verified. Unit: low-disk detection and alert | **Pending:** fill the workspace disk during a backup on a test host; revoke the storage credential and confirm the failing-backup alert |
| Clean up abandoned temporary files and incomplete uploads; retention | Tier 0 A: workspace cleared at start, abandoned uploads removed with provenance, retention tested end to end | Done |
| External alerts for downtime, stale/failed backups, low disk | Webhook alerts (unit and integration); host status | **Pending:** configure the webhook and an external uptime monitor on `/health/ready`; receive a test alert |
| Documented manual update procedure and failed-update recovery | Integration: an update that fails after migrating rolls back to the previous release, snapshot and configuration; a clean update commits | **Pending:** update a real host from one tagged release to the next, and rehearse `pgfyctl rollback-update` once |
| Maintenance ownership for the app, PostgreSQL, images, Docker and host OS | [Host runbook](host-runbook.md) table | **Pending:** write the named owner into each row |
| Fresh-server recovery using only the runbook and independently retained information | Integration covers the mechanism | **Pending:** repeat the full [recovery runbook](recovery-runbook.md) on a replacement server and record the time |
| Acceptable data loss and recovery time for the first application | Backup target interval and newest-recoverable age are visible and alerted | **Pending:** agree the numbers with the application owner and check the rehearsal meets them |
| Resource use under the intended workload during backup and restore | Connection budget and host status are reported | **Pending:** measure CPU, memory, disk workspace, log growth and connections during a backup and a restore of the real dataset |
| Run one low-stakes application for at least two weeks with alerts observed | — | **Pending** |

When every row is Done, remove the "hackathon MVP" wording from the README and merge the branch.
