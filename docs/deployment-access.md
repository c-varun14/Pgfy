# Test server access for developers and AI agents

This is the operator handoff for the existing Lightsail test server. Read this alongside [Lightsail deployment](lightsail.md), [installation and recovery](installation.md), and the [Phase 1 acceptance checklist](phase1-validation.md).

## Server identity

These infrastructure details were verified on **2026-09-18**. Recheck live state before deploying or recording acceptance results.

| Item | Value |
| --- | --- |
| Instance | `firstcommit-phase1` |
| AWS region / availability zone | `us-east-1` / `us-east-1a` |
| OS / architecture | Ubuntu 24.04 LTS / x86-64 |
| Size | 2 vCPU, 2 GiB RAM, 60 GB disk |
| Bundle | `small_3_0` ($12/month before credits) |
| Static IP | `100.57.106.219` |
| Static IP resource | `firstcommit-phase1-ip` |
| Dashboard hostname | `firstcommit.webbywasp.com` |
| DNS provider | Cloudflare; DNS-only A record pointing at the static IP |
| SSH user | `ubuntu` |
| Lightsail key-pair name | `firstcommit-lightsail` |
| Local private key | `/home/varun/.ssh/firstcommit-lightsail-rsa` |
| AWS CLI profile | `firstcommit` (verify its account before using it) |

The private key was confirmed present on this workstation on 2026-09-19. It is **not in the repository**. Agents running as this local user can use it subject to their execution permissions; a clone on another machine does not grant access. Ask the operator to arrange SSH access for a different machine. Never commit or print private keys, AWS credentials, setup tokens, or database secrets.

The original account had an enabled $100 AWS credit covering Lightsail, expiring September 5, 2027. This is historical evidence, not a current balance or a spending limit. The instance continues accruing charges until deleted, including while stopped.

## Connect over SSH

AWS CLI authentication is not required for SSH. Use the dedicated RSA key explicitly:

```sh
ssh -i /home/varun/.ssh/firstcommit-lightsail-rsa \
  -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o ConnectTimeout=15 \
  ubuntu@100.57.106.219
```

The server's host key was recorded in this workstation's `~/.ssh/known_hosts` during provisioning. If it is missing or changed, verify the server identity with the operator before updating that record; do not disable host-key checking.

For a read-only connection check:

```sh
ssh -i /home/varun/.ssh/firstcommit-lightsail-rsa \
  -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o ConnectTimeout=15 \
  ubuntu@100.57.106.219 \
  'hostname; cat /etc/os-release; uname -m; cloud-init status; free -m; df -h /; test -x /opt/firstcommit/pgfyctl && echo pgfyctl-present'
```

The final check can return nonzero if Pgfy is not installed. Server provisioning and successful SSH do not establish application deployment or Phase 1 acceptance. Check the live host and record actual results in [phase1-validation.md](phase1-validation.md).

## Firewall and connection troubleshooting

At provisioning, TCP 22 and 3000 were restricted to the workstation's then-current public IPv4 address, `27.63.254.203/32`; TCP 80/443 were public. The instance was IPv4-only, and PostgreSQL 5432 was closed.

The current deployment guide requires public port 3000 to remain closed and uses HTTPS or an explicit loopback SSH tunnel. The historical 3000 rule is a known difference to reconcile when deploying; do not treat it as a requirement to expose HTTP administration.

On **2026-09-19**, the operator refreshed the expired `firstcommit` session. Approved SSH-rule updates added `103.218.111.98/32` and
`103.107.26.154/32`, preserving `27.63.254.203/32`. The first HTTPS IP lookup returned the former address; a lookup with proxies disabled
returned the latter, and SSH worked after that direct-network address was allowed. Check the current direct egress IP rather than reusing
these addresses blindly. The inbound 3000 rule was unchanged and still requires removal.

Read-only SSH preflight then confirmed Ubuntu 24.04.4 x86-64, 2 CPUs, 1906 MiB RAM, 55 GiB available on ext4, completed cloud-init, correct
dashboard DNS, and no TCP listeners on 80/443. Neither a Docker executable nor `/opt/firstcommit` was present. Installation, host reboot,
application acceptance, and second-host validation remain pending.

- **Timeout:** the workstation's public IP may have changed. Check the current firewall and arrange a narrow SSH allowlist update in the owning AWS account. Do not open SSH to everyone as a workaround.
- **Permission denied:** check username, key path, and key-file permissions. The RSA key above is the registered key; an unused Ed25519 key may also exist locally.
- **Host key changed:** stop and verify whether the server was replaced.
- **Dashboard unavailable:** check whether a release was actually installed, then DNS, services, and HTTPS. Do not assume the app is deployed just because DNS resolves.

## AWS account and DNS access

The operator may use a different AWS account in other profiles. Never rely on the default profile, and never create replacement resources merely because this instance is absent from the currently selected account.

```sh
aws sts get-caller-identity --profile firstcommit --region us-east-1
aws lightsail get-instance --instance-name firstcommit-phase1 \
  --profile firstcommit --region us-east-1 \
  --query 'instance.{name:name,state:state.name,ip:publicIpAddress,static:isStaticIp}' \
  --output json --no-cli-pager
aws lightsail get-instance-port-states --instance-name firstcommit-phase1 \
  --profile firstcommit --region us-east-1 --no-cli-pager
```

If the original profile has expired, the operator can sign in with `aws login --profile firstcommit --region us-east-1`. Confirm the expected instance and static IP before any mutation. Logging in to another account does not transfer ownership or billing. AWS CLI access is needed for infrastructure changes, not normal SSH or application operation.

Cloudflare owns the authoritative DNS zone. AWS CLI cannot modify that record; DNS changes require the operator or separately authorized Cloudflare access. Keep the record DNS-only during direct-origin HTTPS validation.

## Testing and deployment boundaries

- Follow repository and parent `AGENTS.md` instructions and the user's current authorization. This document supplies connection information, not standing permission to spend money, reboot the server, change firewall rules, or destroy data.
- Use read-only inspection first. Do not dump environment files, secrets, raw container inspection output, or unreviewed logs into shared transcripts.
- Install using the checksummed release workflow in [installation.md](installation.md). The expected installation directory is `/opt/firstcommit`; verify it exists before using its commands. Never assume local `dist/` fixtures are publishable releases.
- For an installed deployment, `sudo /opt/firstcommit/pgfyctl diagnostics` is the documented diagnostic command; use elevated access only within the authorized task.
- Destructive tests, reboot/persistence checks, and service interruption need an authorized test window. Preserve existing data; never use `docker compose down -v` or reinitialize storage to repair a failed check.
- An SSH tunnel uses `ssh -i /home/varun/.ssh/firstcommit-lightsail-rsa -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -N -L 8080:127.0.0.1:8080 ubuntu@100.57.106.219`. It works only after the host is explicitly configured for tunnel mode as documented; enabling that mode changes access and invalidates sessions.
- Record test dates, release identifiers, and actual evidence. A second independent non-AWS Ubuntu environment is still required by the acceptance checklist; this server alone cannot satisfy that gate.
