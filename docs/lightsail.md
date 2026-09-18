# Lightsail Phase 1 deployment

This guide supplements the provider-neutral [installation instructions](installation.md). Provisioning and firewall changes are operator actions, not application features.

1. Create an Ubuntu 24.04 LTS **x86-64** Lightsail instance. Start validation with at least 2 vCPU, 2 GiB RAM, and 20 GiB free disk after OS installation. The demo location is US East — N. Virginia; core installation has no region dependency.
2. Allocate and attach a static IPv4 address before configuring the dashboard DNS A record. Configure an AAAA record only when IPv6 reaches this instance correctly.
3. In Lightsail's IPv4 firewall, allow TCP 80/443 for the HTTPS dashboard and restrict TCP 22 to operator source addresses. Configure IPv6 rules separately if enabled. Leave PostgreSQL 5432, application 3000, Caddy admin 2019, and tunnel/internal health 8080/8081 closed.
4. SSH to the host, transfer/download the versioned release bundle, verify its checksum, and run the generic installation command with the dashboard hostname.
5. Confirm HTTPS before entering the terminal setup token. Create the administrator and inspect PostgreSQL and SQLite health.
6. Run the [acceptance checklist](phase1-validation.md), including reboot/rerun persistence and public/private-network exposure probes. Keep secrets and tokens out of recordings.

Lightsail's firewall controls public-address traffic; it does not replace private-network access controls. Phase 1 additionally avoids any database host-port mapping. Probe from a private-network peer as well as an external client. If host firewall rules are used, account for Docker's actual forwarding behavior.

For a deployment without DNS, use the explicit SSH tunnel mode from the generic guide and keep public 8080 closed. Never expose HTTP administration at `IP:3000`.

Retain the static-IP association, SSH access, installation identity, release version, and separately protected configuration/secrets in the operator's records. A static IP is not a data backup. Database provisioning, application connectivity, and server-loss recovery are later phases.
