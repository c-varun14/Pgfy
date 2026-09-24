#!/usr/bin/env python3
"""Host-only administration. No cloud APIs; never imported by the dashboard."""
import argparse
import contextlib
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error
import uuid

DEFAULT_ROOT = "/opt/firstcommit"
HOSTNAME = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\Z")
DIGEST = re.compile(r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}\Z")

class InstallError(Exception):
    pass

def run(args, *, timeout=60, capture=True, check=True, input=None, pass_fds=()):
    try:
        result = subprocess.run([str(a) for a in args], input=input, text=True, capture_output=capture, timeout=timeout, check=False, pass_fds=pass_fds)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise InstallError(f"{args[0]} failed or timed out; check prerequisites and connectivity.") from error
    if check and result.returncode:
        # Do not echo arbitrary subprocess output: it can contain SQL, credentials, or configuration.
        raise InstallError(f"{args[0]} failed (exit {result.returncode}). Run pgfyctl diagnostics; inspect restricted host logs if needed.")
    return result

def read_json(path):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError) as error:
        raise InstallError(f"Cannot read valid JSON from {path}.") from error

def atomic(path, data, mode=0o600, uid=None, gid=None):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if not isinstance(data, bytes):
        data = data.encode()
    fd, temporary = tempfile.mkstemp(prefix=".pgfy-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as output:
            os.fchmod(output.fileno(), mode)
            if uid is not None:
                os.fchown(output.fileno(), uid, gid if gid is not None else uid)
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)

def json_write(path, value, mode=0o600):
    atomic(path, json.dumps(value, indent=2) + "\n", mode)

def lock_path(root):
    return "/run/lock/pgfy-" + hashlib.sha256(str(root).encode()).hexdigest()[:16] + ".lock"

@contextlib.contextmanager
def locked(root):
    # The lock lives outside install state, so even two first-time runs serialize.
    with open(lock_path(root), "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise InstallError("Another installer or host command is running.") from error
        yield lock.fileno()

def holds_lock(root, fd):
    """True only when fd is an open description of this installation's lock that already holds it."""
    try:
        opened, expected = os.fstat(fd), os.stat(lock_path(root))
        if (opened.st_dev, opened.st_ino) != (expected.st_dev, expected.st_ino):
            return False
        # Succeeds without blocking only for the description that holds the lock.
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return True
    except OSError:
        return False

def valid_hostname(hostname):
    if not hostname or len(hostname) > 253 or not HOSTNAME.fullmatch(hostname):
        raise InstallError("Use a lowercase DNS hostname, without scheme, port, path, or wildcard.")
    try:
        ipaddress.ip_address(hostname)
    except ValueError:
        return hostname
    raise InstallError("Use a DNS hostname, not an IP address. Otherwise explicitly select --tunnel.")

def caddyfile(config):
    common = """{
    admin off
}
http://127.0.0.1:8081 {
    bind 127.0.0.1
    respond /health/live \"live\" 200
}
"""
    address = valid_hostname(config["hostname"]) if config["mode"] == "https" else "http://:8080"
    # Override forwarded headers; the app trusts only the isolated proxy network.
    return common + f"""{address} {{
    request_body {{
        max_size 8KB
    }}
    reverse_proxy application:3000 {{
        header_up X-Forwarded-For {{remote_host}}
        transport http {{
            dial_timeout 3s
            response_header_timeout 75s
        }}
    }}
}}
"""

def pg_hba(subnet):
    ipaddress.ip_network(subnet)
    # First match wins: system roles are pinned (and rejected elsewhere) before the
    # dashboard-managed include, so that file can only ever admit project roles.
    return f"""# Host-managed. Do not edit; project rules live in managed/projects.conf.
local all pgfy_bootstrap scram-sha-256
host pgfy_system pgfy_health 127.0.0.1/32 scram-sha-256
host pgfy_system pgfy_health {subnet} scram-sha-256
host all pgfy_mgmt {subnet} scram-sha-256
host all pgfy_bootstrap all reject
host all pgfy_health all reject
host all pgfy_mgmt all reject
include_if_exists managed/projects.conf
local all all reject
host all all 0.0.0.0/0 reject
host all all ::/0 reject
"""

def placeholder_certificate(directory):
    """Self-signed placeholder so PostgreSQL always starts with TLS; sync-db-cert replaces it."""
    directory = Path(directory)
    if (directory / "server.crt").exists() and (directory / "server.key").exists():
        return
    with tempfile.TemporaryDirectory(dir=directory.parent) as work:
        work = Path(work)
        run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "3650", "-subj", "/CN=pgfy-placeholder", "-keyout", work / "server.key", "-out", work / "server.crt"], timeout=30)
        atomic(directory / "server.key", (work / "server.key").read_bytes(), 0o600, 999, 999)
        atomic(directory / "server.crt", (work / "server.crt").read_bytes(), 0o644, 999, 999)
    json_write(directory / "state.json", {"state": "placeholder", "source": "self-signed", "issuer": "", "not_after": "", "fingerprint": ""}, 0o644)

def compose_env(root, state, cfg):
    # Loopback-published connections (the SSH-tunnel path) arrive from the Docker gateway only.
    tunnel_source = str(ipaddress.ip_network(state["public_subnet"])[1]) + "/32"
    values = {"INSTALL_DIR": str(root), "APP_IMAGE": state["images"]["application"], "POSTGRES_IMAGE": state["images"]["postgres"], "CADDY_IMAGE": state["images"]["caddy"], "VOLUME_PREFIX": state["volume_prefix"], "DATABASE_SUBNET": state["database_subnet"], "PROXY_SUBNET": state["proxy_subnet"], "PUBLIC_SUBNET": state["public_subnet"], "TUNNEL_SOURCE": tunnel_source, "PG_BIND": "0.0.0.0" if cfg["mode"] == "https" else "127.0.0.1",
              "SCHEDULE_INTERVAL": os.environ.get("PGFY_SCHEDULE_INTERVAL", "5m")}
    return "\n".join(f"{k}={v}" for k, v in values.items()) + "\n"

POSTGRES_POLICY = re.compile(r"postgres:18\.[0-9]+-bookworm@sha256:[a-f0-9]{64}\Z")
VERSION = re.compile(r"v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([a-zA-Z0-9.-]+))?\Z")

def verify_bundle(bundle):
    release = verify_bundle_integrity(bundle)
    release_policy(release)
    return release

def release_policy(release):
    # Minor PostgreSQL releases may change; the major version and base OS (collation) may not.
    if not POSTGRES_POLICY.fullmatch(release["images"]["postgres"]):
        raise InstallError("This release requires PostgreSQL 18 on the bookworm base image.")

def verify_bundle_integrity(bundle):
    """Checksums, pinned digests and a version: everything an older installer can judge about a newer bundle."""
    manifest = bundle / "SHA256SUMS"
    if not manifest.is_file():
        raise InstallError("Bundle has no SHA256SUMS. Download a versioned release bundle.")
    verified = set()
    for line in manifest.read_text().splitlines():
        expected, name = line.split("  ", 1)
        path = bundle / name
        if not path.resolve().is_relative_to(bundle.resolve()) or path.is_symlink() or not path.is_file():
            raise InstallError("Bundle checksum manifest contains an unsafe or missing file.")
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise InstallError(f"Checksum mismatch for {name}; download the release again.")
        verified.add(name)
    required = {"release.json", "installer.py", "install.sh", "pgfyctl", "compose.yaml", "compose.https.yaml", "compose.tunnel.yaml", "postgres/init.sh", "postgres/health.sh"}
    if not required.issubset(verified):
        raise InstallError("Bundle checksum manifest is incomplete.")
    release = read_json(bundle / "release.json")
    for key in ("application", "postgres", "caddy"):
        if not DIGEST.fullmatch(release["images"][key]):
            raise InstallError(f"The {key} image is not pinned to a digest.")
    if not VERSION.fullmatch(release["version"]):
        raise InstallError("Invalid release version.")
    return release

def parse_version(version):
    match = VERSION.fullmatch(version)
    if not match:
        raise InstallError(f"Invalid release version {version}.")
    return tuple(int(part) for part in match.groups()[:3]), match.group(4)

def prerelease_key(pre):
    # Semantic-versioning precedence: numeric identifiers sort numerically and before alphanumeric ones.
    return [(0, int(part), "") if part.isdigit() else (1, 0, part) for part in pre.split(".")]

def update_allowed(installed, candidate):
    """Stable moves only to a newer stable; a pre-release moves to a later pre-release of the same version or to
    its stable release or a newer one. Nothing ever moves backwards or to a pre-release from a stable release."""
    old_core, old_pre = parse_version(installed)
    new_core, new_pre = parse_version(candidate)
    if new_pre is not None:
        return old_pre is not None and new_core == old_core and prerelease_key(new_pre) > prerelease_key(old_pre)
    if old_pre is not None:
        return new_core >= old_core
    return new_core > old_core

def version_at_least(actual, minimum):
    match = re.search(r"(\d+)\.(\d+)(?:\.(\d+))?", actual)
    return bool(match) and tuple(int(v or 0) for v in match.groups()) >= minimum

def docker_versions():
    engine = run(["docker", "version", "--format", "{{.Server.Version}}"], check=False)
    compose = run(["docker", "compose", "version", "--short"], check=False)
    if engine.returncode or compose.returncode or not version_at_least(engine.stdout, (28, 0, 0)) or not version_at_least(compose.stdout, (2, 30, 0)):
        raise InstallError("Docker Engine 28+ and Compose 2.30+ are required. Existing incompatible installations will not be replaced.")
    return engine.stdout.strip(), compose.stdout.strip()

def install_docker(release):
    for package in ("docker.io", "docker-ce", "containerd", "podman-docker"):
        installed = run(["dpkg-query", "-W", "-f=${Status}", package], check=False)
        if "install ok installed" in installed.stdout:
            raise InstallError("Existing container packages found without a compatible Docker CLI. Repair them manually; installer will not replace them.")
    run(["apt-get", "update"], timeout=300)
    run(["apt-get", "install", "-y", "ca-certificates", "curl"], timeout=300)
    Path("/etc/apt/keyrings").mkdir(mode=0o755, exist_ok=True)
    with urllib.request.urlopen("https://download.docker.com/linux/ubuntu/gpg", timeout=30) as response:
        atomic("/etc/apt/keyrings/docker.asc", response.read(), 0o644)
    atomic("/etc/apt/sources.list.d/pgfy-docker.sources", "Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: noble\nComponents: stable\nArchitectures: amd64\nSigned-By: /etc/apt/keyrings/docker.asc\n", 0o644)
    run(["apt-get", "update"], timeout=300)
    packages = release["docker_packages"]
    run(["apt-get", "install", "-y", *[f"{name}={version}" for name, version in packages.items()]], timeout=600)
    run(["systemctl", "enable", "--now", "docker"], timeout=120)

def network_preflight(hostname):
    if hostname:
        try:
            records = socket.getaddrinfo(hostname, 443, type=socket.SOCK_STREAM)
            if not records:
                raise OSError()
        except OSError as error:
            raise InstallError("Dashboard hostname does not resolve. Configure DNS before installation.") from error
    for address in ("https://registry-1.docker.io/v2/", "https://ghcr.io/v2/"):
        try:
            with urllib.request.urlopen(address, timeout=15):
                pass
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise InstallError("Container registry connectivity check failed.") from error
        except OSError as error:
            raise InstallError("Cannot reach container registries. Check DNS and outbound HTTPS.") from error
    if hostname:
        try:
            with urllib.request.urlopen("https://acme-v02.api.letsencrypt.org/directory", timeout=15):
                pass
        except OSError as error:
            raise InstallError("Cannot reach the certificate authority over HTTPS.") from error

def available_ports(mode):
    ports = [80, 443] if mode == "https" else [8080]
    for port in ports:
        for family, host in ((socket.AF_INET, "0.0.0.0"), (socket.AF_INET6, "::")):
            if family == socket.AF_INET6 and not ipv6_enabled():
                continue
            with socket.socket(family, socket.SOCK_STREAM) as listener:
                # Lingering TIME-WAIT sockets from an earlier run are not an occupied port.
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                try:
                    if family == socket.AF_INET6:
                        listener.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                    listener.bind((host, port))
                except OSError as error:
                    raise InstallError(f"Port {port} is occupied. Release it before installing.") from error

def ipv6_enabled():
    flag = Path("/proc/sys/net/ipv6/conf/all/disable_ipv6")
    if not socket.has_ipv6 or (flag.exists() and flag.read_text().strip() != "0"):
        return False
    # A kernel booted with ipv6.disable=1 has no sysctl and refuses the socket family.
    try:
        socket.socket(socket.AF_INET6, socket.SOCK_STREAM).close()
    except OSError:
        return False
    return True

def select_subnets():
    occupied = []
    networks = run(["docker", "network", "ls", "-q"]).stdout.split()
    if networks:
        for item in json.loads(run(["docker", "network", "inspect", *networks]).stdout):
            occupied.extend(ipaddress.ip_network(c["Subnet"]) for c in (item.get("IPAM", {}).get("Config") or []) if c.get("Subnet"))
    for route in json.loads(run(["ip", "-j", "-4", "route"]).stdout):
        if route.get("dst") and route["dst"] != "default":
            occupied.append(ipaddress.ip_network(route["dst"], strict=False))
    candidates = [ipaddress.ip_network(f"172.{second}.{third}.0/24") for second in range(20, 32) for third in range(240, 254)]
    free = [n for n in candidates if not any(n.version == used.version and n.overlaps(used) for used in occupied)]
    if len(free) < 3:
        raise InstallError("Cannot find three unused private Docker subnets. Review host routing.")
    return str(free[0]), str(free[1]), str(free[2])

def preflight(root, release, hostname, mode, existing):
    info = platform.freedesktop_os_release()
    if info.get("ID") != "ubuntu" or info.get("VERSION_ID") != "24.04" or platform.machine() != "x86_64":
        raise InstallError("Supported target: Ubuntu 24.04 LTS on x86-64.")
    for command in ("ip", "curl", "findmnt", "openssl"):
        if not shutil.which(command):
            raise InstallError(f"Missing prerequisite: {command}.")
    ancestor = root
    while not ancestor.exists():
        ancestor = ancestor.parent
    filesystem = run(["findmnt", "-n", "-o", "FSTYPE", "-T", str(ancestor)]).stdout.strip()
    if filesystem not in ("ext4", "xfs", "btrfs"):
        raise InstallError("Installation requires persistent local ext4, XFS, or Btrfs storage.")
    mem_kib = int(next(line.split()[1] for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemTotal:")))
    if (os.cpu_count() or 0) < 2 or mem_kib < 1900000 or shutil.disk_usage(ancestor).free < 20 * 1024**3:
        raise InstallError("Validation minimum: 2 vCPU, 2 GiB RAM, and 20 GiB free installation disk.")
    network_preflight(hostname)
    if not shutil.which("docker"):
        install_docker(release)
    versions = docker_versions()
    docker_root = run(["docker", "info", "--format", "{{.DockerRootDir}}"]).stdout.strip()
    if not docker_root or not Path(docker_root).is_dir():
        raise InstallError("Docker must use local persistent storage on this host.")
    docker_filesystem = run(["findmnt", "-n", "-o", "FSTYPE", "-T", docker_root]).stdout.strip()
    if docker_filesystem not in ("ext4", "xfs", "btrfs") or shutil.disk_usage(docker_root).free < 20 * 1024**3:
        raise InstallError("Docker data storage requires a persistent local filesystem and 20 GiB free disk.")
    if not existing:
        available_ports(mode)
    return versions

class Installation:
    def __init__(self, root):
        self.root = Path(root)
        self.state = read_json(self.root / "state.json")
        self.bundle = self.root / "releases" / self.state["release"]

    def config(self):
        return read_json(self.root / "config/install.json")

    def compose(self, *args, mode=None, **kwargs):
        mode = mode or self.config()["mode"]
        return run(["docker", "compose", "--project-name", self.state["volume_prefix"], "--env-file", self.root / "compose.env", "-f", self.bundle / "compose.yaml", "-f", self.bundle / f"compose.{mode}.yaml", *args], **kwargs)

    def container(self, service):
        return self.compose("ps", "-aq", service).stdout.strip()

    def inspect_data(self):
        name = self.state["volume_prefix"] + "_postgres"
        if run(["docker", "volume", "inspect", name], check=False).returncode:
            return "empty"
        image = self.state["images"]["postgres"]
        script = "if [ -z \"$(ls -A /volume)\" ]; then echo empty; elif [ -f /volume/18/docker/.pgfy-initialized ] && [ -f /volume/18/docker/PG_VERSION ]; then cat /volume/18/docker/.pgfy-initialized; else echo partial; fi"
        return run(["docker", "run", "--rm", "--network", "none", "--read-only", "--mount", f"type=volume,src={name},dst=/volume,readonly", "--entrypoint", "sh", image, "-c", script]).stdout.strip()

    def wait_postgres(self):
        deadline = time.monotonic() + 150
        while time.monotonic() < deadline:
            identifier = self.container("postgres")
            if identifier:
                health = run(["docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{end}}", identifier]).stdout.strip()
                if health == "healthy":
                    return
            time.sleep(2)
        raise InstallError("PostgreSQL failed authenticated initialization checks. Do not delete its volume or regenerate credentials. Run pgfyctl diagnostics and inspect PostgreSQL logs on the host.")

    def verify(self, require_dependencies=True):
        if require_dependencies:
            self.wait_postgres()
        cfg = self.config()
        # HTTPS verifies hostname/trust locally against Caddy, independently of NAT hairpin support.
        command = ["curl", "--fail", "--silent", "--show-error", "--noproxy", "*", "--connect-timeout", "3", "--max-time", "8"]
        if cfg["mode"] == "https":
            command.extend(["--resolve", f"{cfg['hostname']}:443:127.0.0.1"])
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            health_path = "/health/ready" if require_dependencies else "/health/live"
            response = run([*command, cfg["origin"] + health_path], check=False)
            if response.returncode == 0:
                page = run([*command, cfg["origin"] + "/"], check=False)
                if page.returncode == 0 and '<div id="root">' in page.stdout:
                    if require_dependencies:
                        self.compose("exec", "-T", "application", "pgfy", "health", "ready")
                        self.compose("exec", "-T", "postgres", "bash", "/usr/local/bin/pgfy-postgres-health")
                    return
            time.sleep(3)
        raise InstallError("Caddy routing or HTTPS verification failed. Check DNS and inbound 80/443. Public HTTP setup stays disabled; use explicit host-side tunnel recovery if needed.")

    def write_access(self, cfg):
        json_write(self.root / "config/install.json", cfg, 0o644)
        atomic(self.root / "config/caddy/Caddyfile", caddyfile(cfg), 0o644)

    def validate_caddy(self):
        self.compose("run", "--rm", "--no-deps", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")

def stage_bundle(bundle, target, verify):
    """Copy a verified bundle into releases/ through a staging directory, so a partial copy is never used."""
    staging = target.parent / (target.name + ".staging")
    if staging.exists():
        shutil.rmtree(staging)
    staging.mkdir(parents=True)
    for path in bundle.rglob("*"):
        if path.is_file() and not path.is_symlink():
            destination = staging / path.relative_to(bundle)
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, destination)
            os.chmod(destination, 0o644)
    verify(staging)
    staging.rename(target)

def write_pgfyctl(root, bundle):
    atomic(root / "pgfyctl", f'#!/usr/bin/env bash\nexec python3 "{bundle}/installer.py" --dir "{root}" "$@"\n', 0o700)

def pull_images(images):
    for image in images.values():
        result = run(["docker", "pull", "--platform", "linux/amd64", image], timeout=600, check=False)
        if result.returncode:
            raise InstallError("Pinned image pull failed. Check registry access and retry this same bundle; existing state is preserved.")

def converge_steps(installation):
    """Release-specific host configuration, shared by install and update. Every step must be idempotent and
    compatible with the previous release: an update that rolls back restores files and metadata, never these
    effects on PostgreSQL or the host."""
    cfg = installation.config()
    atomic(installation.root / "config/caddy/Caddyfile", caddyfile(cfg), 0o644)
    atomic(installation.root / "config/pg/pg_hba.conf", pg_hba(installation.state["database_subnet"]), 0o644)
    installation.validate_caddy()

# Grants added after the first release; init.sh carries them for fresh clusters. Additive and harmless to
# earlier releases, as every converge step must be.
POSTGRES_CONVERGE_SQL = """GRANT pg_use_reserved_connections TO pgfy_mgmt, pgfy_health;
GRANT SET ON PARAMETER temp_file_limit TO pgfy_mgmt;
"""

def converge_postgres(installation):
    """Bring an existing cluster's system roles up to this release, as the bootstrap role over the local socket."""
    installation.wait_postgres()
    command = 'PGPASSWORD="$(cat /run/secrets/bootstrap_password)" exec psql -U pgfy_bootstrap -d pgfy_system -XAtq -v ON_ERROR_STOP=1'
    installation.compose("exec", "-T", "postgres", "sh", "-c", command, input=POSTGRES_CONVERGE_SQL, timeout=60)

def install(args):
    bundle = Path(args.bundle).resolve()
    release = verify_bundle(bundle)
    root = Path(args.dir).resolve()
    if not re.fullmatch(r"/[a-zA-Z0-9_./-]+", str(root)) or root == Path("/"):
        raise InstallError("Installation path must be an absolute path without whitespace or shell characters.")
    existing = (root / "state.json").exists()
    if existing:
        previous = read_json(root / "state.json")
        if release["version"] != previous["release"] or release["images"] != previous["images"]:
            raise InstallError("Reruns must use the installed release and image digests. Updates require a separate procedure: pgfyctl update <extracted bundle directory>.")
        cfg = read_json(root / "config/install.json")
        if (args.hostname and args.hostname != cfg["hostname"]) or (args.tunnel and cfg["mode"] != "tunnel"):
            raise InstallError("Use pgfyctl hostname or pgfyctl tunnel to change host access configuration.")
        mode, hostname = cfg["mode"], cfg["hostname"]
    else:
        if not args.hostname and not args.tunnel:
            raise InstallError("Supply --hostname dashboard.example.com or explicitly enable --tunnel.")
        mode = "tunnel" if args.tunnel else "https"
        hostname = "" if args.tunnel else valid_hostname(args.hostname)
        if root.exists() and any(root.iterdir()):
            raise InstallError("Installation directory is nonempty without installation state. Inspect it manually; nothing was overwritten.")
    engine, compose_version = preflight(root, release, hostname, mode, existing)
    if not existing:
        database_subnet, proxy_subnet, public_subnet = select_subnets()
        root.mkdir(parents=True, mode=0o711, exist_ok=True)
        installation_id = uuid.uuid4().hex
        state = {"release": release["version"], "images": release["images"], "id": installation_id, "volume_prefix": "pgfy_" + installation_id[:12], "database_subnet": database_subnet, "proxy_subnet": proxy_subnet, "public_subnet": public_subnet, "stage": "preparing"}
        # Stage configuration/state together before any data or secrets exist.
        cfg = {"id": installation_id, "mode": mode, "hostname": hostname, "origin": "https://" + hostname if hostname else "http://127.0.0.1:8080", "generation": uuid.uuid4().hex, "release": release["version"], "caddy_version": "pending", "docker_version": engine, "compose_version": compose_version}
        (root / "config/caddy").mkdir(parents=True, mode=0o755)
        os.chmod(root / "config", 0o755)
        json_write(root / "config/install.json", cfg, 0o644)
        json_write(root / "state.json", state)
    installation = Installation(root)
    state = installation.state
    if not installation.bundle.exists():
        stage_bundle(bundle, installation.bundle, verify_bundle)
    verify_bundle(installation.bundle)
    write_pgfyctl(root, installation.bundle)
    print("Release verified. Preparing persistent storage and pinned images…", flush=True)
    (root / "secrets").mkdir(mode=0o711, exist_ok=True)
    (root / "data/sqlite").mkdir(parents=True, mode=0o700, exist_ok=True)
    os.chown(root / "data/sqlite", 10001, 10001)
    # Disk-backed workspace for dump/restore archives; /tmp inside the container is a small tmpfs.
    (root / "data/work").mkdir(parents=True, mode=0o700, exist_ok=True)
    os.chown(root / "data/work", 10001, 10001)
    if (root / "config/pg_hba.conf").exists():
        raise InstallError("This installation uses the pre-release Phase 1 layout, which cannot be upgraded in place. Back up any data, remove the installation, and install fresh.")
    (root / "config/pg").mkdir(parents=True, mode=0o755, exist_ok=True)
    # The dashboard owns only this directory: it may admit project roles, nothing else.
    (root / "config/pg/managed").mkdir(mode=0o750, exist_ok=True)
    os.chown(root / "config/pg/managed", 10001, 999)
    (root / "config/postgres-tls").mkdir(parents=True, mode=0o755, exist_ok=True)
    pull_images(state["images"])
    data_state = installation.inspect_data()
    if data_state not in ("empty", state["id"]):
        raise InstallError("Partial or foreign PostgreSQL initialization detected. Stop and inspect the existing volume; do not regenerate credentials or rerun initialization scripts blindly.")
    if data_state == "empty" and state["stage"] != "preparing":
        raise InstallError("Previously initialized PostgreSQL storage is missing. Restore the original volume; the installer will not silently replace it.")
    secret_specs = {"bootstrap_password": (secrets.token_hex(32).encode(), 999, 999, 0o400), "health_password": (secrets.token_hex(32).encode(), 10001, 999, 0o440), "management_password": (secrets.token_hex(32).encode(), 10001, 999, 0o440), "encryption_key": (secrets.token_bytes(32), 10001, 10001, 0o400)}
    for name, (value, uid, gid, permissions) in secret_specs.items():
        path = root / "secrets" / name
        if not path.exists():
            if data_state != "empty" or state["stage"] != "preparing" or (root / "data/sqlite/pgfy.db").exists():
                raise InstallError(f"Required secret {name} is missing alongside existing state. Restore it; credentials will not be regenerated.")
            atomic(path, value, permissions, uid, gid)
        else:
            info = path.lstat()
            if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != permissions or info.st_uid != uid or info.st_gid != gid:
                raise InstallError(f"Secret {name} has unexpected ownership or permissions; restore the documented restricted host permissions.")
    generated = {
        root / "config/installation-id": state["id"] + "\n",
        root / "config/pg/pg_hba.conf": pg_hba(state["database_subnet"]),
        root / "config/caddy/Caddyfile": caddyfile(cfg),
        root / "compose.env": compose_env(root, state, cfg),
    }
    for path, content in generated.items():
        if not path.exists():
            if state["stage"] != "preparing":
                raise InstallError(f"Installation configuration {path.name} is missing; restore it from your host backup.")
            atomic(path, content, 0o600 if path.name == "compose.env" else 0o644)
        elif path.name in ("compose.env", "installation-id") and path.read_text() != content:
            raise InstallError(f"{path.name} no longer matches persisted installation/release/volume identity. Restore the original configuration; no services were changed.")
    placeholder_certificate(root / "config/postgres-tls")
    installation.compose("config", "--quiet")
    converge_steps(installation)
    sqlite_path = root / "data/sqlite/pgfy.db"
    if not sqlite_path.exists():
        if data_state != "empty" or state["stage"] != "preparing" or any((root / "data/sqlite").iterdir()):
            raise InstallError("Existing management storage is missing. Restore SQLite; setup will not be reopened.")
        installation.compose("run", "--rm", "--no-deps", "application", "initialize-store")
    installation.compose("up", "-d", "postgres", "application", "caddy", timeout=180)
    converge_postgres(installation)
    print("Checking authenticated PostgreSQL, SQLite, and dashboard routing…", flush=True)
    installation.verify()
    cfg["caddy_version"] = installation.compose("exec", "-T", "caddy", "caddy", "version").stdout.strip()
    cfg["docker_version"], cfg["compose_version"] = engine, compose_version
    json_write(root / "config/install.json", cfg, 0o644)
    # Refresh read-only version metadata; the session scope remains unchanged.
    installation.compose("restart", "application", timeout=60)
    installation.verify()
    state["stage"] = "installed"
    json_write(root / "state.json", state)
    print(f"Installation verified: {cfg['origin']}")
    if mode == "tunnel":
        print("Restricted access: loopback only. From your computer: ssh -L 8080:127.0.0.1:8080 user@server")
        print("PostgreSQL listens on 127.0.0.1:5432 only. Use ssh -L 5432:127.0.0.1:5432 user@server.")
    else:
        sync_db_cert(installation, fatal=False)
        install_cert_timer(root)
        print(f"Direct database access: {hostname}:5432 over TLS. Open TCP 5432 in your provider firewall to allow application connections.")
    if not existing:
        # Token command emits plaintext only to this terminal, never container logs.
        token = installation.compose("exec", "-T", "application", "pgfy", "setup-token", check=False)
        if token.returncode == 0:
            print("Setup token (expires in 30 minutes; enter in the form, never in a URL):")
            print(token.stdout.strip())
        else:
            print(f"Setup token unavailable. Use sudo {root}/pgfyctl setup-token after readiness succeeds.")
    else:
        print("Existing state preserved. If setup is unfinished and its token expired, use pgfyctl setup-token.")

def change_access(installation, mode, hostname="", rollback=False):
    root = installation.root
    old = installation.config()
    if rollback:
        saved = read_json(root / "access-rollback.json")
        # Only access fields come back: the record may predate an update, and must not revert the release.
        new = dict(old, **{key: saved["config"][key] for key in ("mode", "hostname", "origin")})
    else:
        if mode == "https":
            valid_hostname(hostname)
            network_preflight(hostname)
        new = dict(old, mode=mode, hostname=hostname, origin="https://" + hostname if mode == "https" else "http://127.0.0.1:8080")
        json_write(root / "access-rollback.json", {"config": old, "caddyfile": (root / "config/caddy/Caddyfile").read_text()})
    new["generation"] = uuid.uuid4().hex
    # Persist the previous configuration before mutation; rollback also works after process loss.
    saved_caddy = (root / "config/caddy/Caddyfile").read_text()
    try:
        installation.write_access(new)
        installation.validate_caddy()
        # PG_BIND follows the access mode: public 5432 only alongside public HTTPS.
        atomic(root / "compose.env", compose_env(root, installation.state, new), 0o600)
        installation.compose("up", "-d", "--force-recreate", "postgres", "application", "caddy", timeout=120)
        # Host recovery must remain usable during a PostgreSQL or SQLite outage.
        installation.verify(require_dependencies=False)
    except (InstallError, KeyboardInterrupt):
        old["generation"] = uuid.uuid4().hex
        json_write(root / "config/install.json", old, 0o644)
        atomic(root / "config/caddy/Caddyfile", saved_caddy, 0o644)
        atomic(root / "compose.env", compose_env(root, installation.state, old), 0o600)
        installation.compose("up", "-d", "--force-recreate", "postgres", "application", "caddy", timeout=120, check=False)
        raise InstallError("Access change failed; previous configuration restored. Sign in again. If the host was interrupted, run pgfyctl rollback-hostname.")
    print(f"Access verified: {new['origin']}. Previous sessions are invalid; sign in again.")
    if new["mode"] == "tunnel":
        print("Loopback access only. Use ssh -L 8080:127.0.0.1:8080 user@server.")
        print("PostgreSQL listens on 127.0.0.1:5432 only. Use ssh -L 5432:127.0.0.1:5432 user@server.")
    else:
        print("PostgreSQL accepts TLS connections on port 5432. Open TCP 5432 in your provider firewall.")
        sync_db_cert(installation, fatal=False)

def certificate_fingerprint(path):
    out = run(["openssl", "x509", "-in", path, "-noout", "-fingerprint", "-sha256"]).stdout
    return out.strip().split("=", 1)[1]

def sync_db_cert(installation, fatal=True):
    """Deliver Caddy's certificate for the dashboard hostname to PostgreSQL. Caddy issues and
    renews; this copies, validates, reloads, and confirms a new connection sees the change."""
    root = installation.root
    cfg = installation.config()
    tls = root / "config/postgres-tls"
    try:
        if cfg["mode"] != "https":
            raise InstallError("Direct database TLS uses the dashboard hostname; the installation is in tunnel mode.")
        host = cfg["hostname"]
        listing = installation.compose("exec", "-T", "caddy", "sh", "-c", f"ls /data/caddy/certificates/*/{host}/{host}.crt 2>/dev/null | head -n 1", check=False)
        crt_path = listing.stdout.strip()
        if listing.returncode or not crt_path:
            raise InstallError("Caddy has not obtained a certificate for the dashboard hostname yet. Check DNS and inbound 80/443, then rerun pgfyctl sync-db-cert.")
        certificate = installation.compose("exec", "-T", "caddy", "cat", crt_path).stdout
        key = installation.compose("exec", "-T", "caddy", "cat", crt_path[:-4] + ".key").stdout
        with tempfile.TemporaryDirectory(dir=tls) as work:
            work = Path(work)
            atomic(work / "server.crt", certificate, 0o600)
            atomic(work / "server.key", key, 0o600)
            run(["openssl", "x509", "-in", work / "server.crt", "-noout", "-checkhost", host, "-checkend", "86400"])
            run(["openssl", "verify", "-untrusted", work / "server.crt", work / "server.crt"])
            public_from_cert = run(["openssl", "x509", "-in", work / "server.crt", "-noout", "-pubkey"]).stdout
            public_from_key = run(["openssl", "pkey", "-in", work / "server.key", "-pubout"]).stdout
            if public_from_cert.strip() != public_from_key.strip():
                raise InstallError("Certificate and key from Caddy do not match; nothing was changed.")
            fingerprint = certificate_fingerprint(work / "server.crt")
            details = run(["openssl", "x509", "-in", work / "server.crt", "-noout", "-issuer", "-enddate", "-nameopt", "RFC2253"]).stdout.splitlines()
            issuer = next((line.split("=", 1)[1] for line in details if line.startswith("issuer=")), "")
            not_after = next((line.split("=", 1)[1] for line in details if line.startswith("notAfter=")), "")
            previous = {name: (tls / name).read_bytes() for name in ("server.crt", "server.key") if (tls / name).exists()}
            if previous and certificate_fingerprint(tls / "server.crt") == fingerprint:
                print("PostgreSQL already serves the current certificate.")
                return
            for name, content in previous.items():
                atomic(tls / (name + ".previous"), content, 0o600, 999, 999)
            atomic(tls / "server.key", key, 0o600, 999, 999)
            atomic(tls / "server.crt", certificate, 0o644, 999, 999)
        def reload_and_observe():
            installation.compose("exec", "-T", "-u", "postgres", "postgres", "pg_ctl", "reload")
            time.sleep(1)
            observed = run(["sh", "-c", "openssl s_client -starttls postgres -connect 127.0.0.1:5432 -servername " + host + " </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256"], check=False).stdout
            return observed.strip().split("=", 1)[-1]
        if reload_and_observe() != fingerprint:
            for name, content in previous.items():
                atomic(tls / name, content, 0o600 if name.endswith(".key") else 0o644, 999, 999)
            reload_and_observe()
            raise InstallError("PostgreSQL did not present the new certificate after reload; the previous certificate was restored.")
        json_write(tls / "state.json", {"state": "trusted", "source": "caddy", "hostname": host, "issuer": issuer, "not_after": not_after, "fingerprint": fingerprint, "synced_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}, 0o644)
        print(f"PostgreSQL now serves the certificate for {host} (expires {not_after}).")
    except InstallError as error:
        if fatal:
            raise
        print(f"Database certificate not synced yet: {error} The self-signed placeholder remains; clients cannot verify it until sync succeeds.")

def install_cert_timer(root):
    units = {
        "/etc/systemd/system/pgfy-cert.service": f"[Unit]\nDescription=Deliver the renewed Pgfy database certificate to PostgreSQL\n\n[Service]\nType=oneshot\nExecStart={root}/pgfyctl sync-db-cert\n",
        "/etc/systemd/system/pgfy-cert.timer": "[Unit]\nDescription=Daily Pgfy database certificate sync\n\n[Timer]\nOnCalendar=daily\nRandomizedDelaySec=1h\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n",
    }
    for path, content in units.items():
        atomic(path, content, 0o644)
    run(["systemctl", "daemon-reload"], check=False)
    run(["systemctl", "enable", "--now", "pgfy-cert.timer"], check=False)

UPDATE_FILES = ("state.json", "compose.env", "config/install.json", "config/caddy/Caddyfile", "config/pg/pg_hba.conf", "pgfyctl")
SNAPSHOT_DB = ".update-rollback.db"  # inside data/sqlite, owned by the application user
CONVERGE_CONTRACT = "1"

class Interrupted(KeyboardInterrupt):
    pass

_shield = {"on": False}

def _interrupt(signum, frame):
    # Once a rollback has begun, a second signal must not abort it half-way.
    if not _shield["on"]:
        raise Interrupted(f"signal {signum}")

@contextlib.contextmanager
def signals(handler):
    """SIGTERM and SIGHUP (a dropped SSH session) interrupt like Ctrl-C; SIG_IGN shields a rollback."""
    names = (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)
    previous = {name: signal.getsignal(name) for name in names}
    for name in names:
        if handler is not None or name != signal.SIGINT:
            signal.signal(name, handler if handler is not None else _interrupt)
    try:
        yield
    finally:
        for name, value in previous.items():
            signal.signal(name, value)

def fsync_dir(path):
    directory = os.open(path, os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)

def app_status(installation):
    identifier = installation.container("application")
    if not identifier:
        return "absent"
    return run(["docker", "inspect", "--format", "{{.State.Status}}", identifier]).stdout.strip()

def stop_application(installation, failure="The application did not stop; nothing was changed."):
    installation.compose("stop", "application", timeout=120)
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if app_status(installation) not in ("running", "restarting"):
            return
        time.sleep(1)
    raise InstallError(failure)

def app_command(installation, *args, running=True, check=True):
    """Run a pgfy host command in the running application, or in a one-off container of the configured release."""
    if running:
        return installation.compose("exec", "-T", "application", "pgfy", *args, timeout=60, check=check)
    return installation.compose("run", "--rm", "--no-deps", "application", *args, timeout=120, check=check)

def store_file(root, image, *args):
    """Snapshot or restore SQLite with a specific application image, independent of compose configuration."""
    return run(["docker", "run", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp", "--user", "10001:10001", "-v", f"{root}/data/sqlite:/data", image, *args], timeout=600)

def run_converge(installation, bundle, lock_fd):
    """The new release applies its own host configuration while this process keeps the lock. Its messages go
    straight to the operator's terminal; it prints no secrets."""
    run([sys.executable, bundle / "installer.py", "--dir", installation.root, "converge", "--contract", CONVERGE_CONTRACT, "--lock-fd", lock_fd], timeout=600, capture=False, pass_fds=(lock_fd,))

def discard_snapshot(root, image):
    """The snapshot lives in the application user's private directory; remove it as that user."""
    run(["docker", "run", "--rm", "--network", "none", "--read-only", "--user", "10001:10001", "-v", f"{root}/data/sqlite:/data", "--entrypoint", "rm", image, "-f", "/data/" + SNAPSHOT_DB], timeout=120)

def converge(root, contract, lock_fd):
    if contract != CONVERGE_CONTRACT:
        raise InstallError(f"Unsupported converge contract {contract}.")
    if lock_fd is None or not holds_lock(root, lock_fd):
        raise InstallError("converge runs only from pgfyctl update, which holds the installation lock.")
    converge_installation(Installation(root))

def converge_installation(installation):
    """Everything this release changes on an installed host during an update."""
    converge_steps(installation)
    # PostgreSQL keeps running through an update; the later force-recreate applies new server flags.
    installation.compose("up", "-d", "--no-recreate", "postgres", timeout=180)
    converge_postgres(installation)

def update(root, bundle, drain=False, lock_fd=None):
    installation = Installation(root)
    state = installation.state
    rollback_dir = root / "update-rollback"
    if state.get("stage") != "installed":
        raise InstallError("Finish the installation before updating it.")
    if rollback_dir.exists():
        raise InstallError("An earlier update did not finish. Run pgfyctl rollback-update first.")
    bundle = Path(bundle).resolve()
    if not bundle.is_dir():
        raise InstallError("Extract the release bundle and pass its directory.")
    release = verify_bundle_integrity(bundle)
    release_policy(release)
    if not update_allowed(state["release"], release["version"]):
        raise InstallError(f"Cannot update {state['release']} to {release['version']}: updates move only to a newer release, stable releases only to stable ones.")
    target = root / "releases" / release["version"]
    if target.exists():
        if (target / "SHA256SUMS").read_bytes() != (bundle / "SHA256SUMS").read_bytes():
            raise InstallError(f"releases/{release['version']} exists with different contents; inspect it manually.")
        verify_bundle_integrity(target)
    else:
        stage_bundle(bundle, target, verify_bundle_integrity)
    print(f"Release {release['version']} verified. Pulling pinned images…", flush=True)
    pull_images(release["images"])
    cfg = installation.config()
    new_state = dict(state, release=release["version"], images=release["images"])
    with tempfile.NamedTemporaryFile("w", dir=root, prefix=".pgfy-env-") as candidate:
        candidate.write(compose_env(root, new_state, cfg))
        candidate.flush()
        run(["docker", "compose", "--project-name", state["volume_prefix"], "--env-file", candidate.name, "-f", target / "compose.yaml", "-f", target / f"compose.{cfg['mode']}.yaml", "config", "--quiet"])
    status = app_status(installation)
    was_running = status == "running"
    snapshot_ready = committed = False
    step = "pausing the installation"
    with signals(None):
        try:
            if was_running:
                app_command(installation, "maintenance", "on")
                running = int(app_command(installation, "jobs", "running").stdout.strip() or "0")
                if running and not drain:
                    raise InstallError("A backup or restore is running. Retry when it finishes, or pass --drain to wait for it.")
                deadline = time.monotonic() + 2 * 3600
                while running:
                    if time.monotonic() > deadline:
                        raise InstallError("A backup or restore is still running after two hours; nothing was changed.")
                    print("Waiting for the running backup or restore to finish…", flush=True)
                    time.sleep(5)
                    running = int(app_command(installation, "jobs", "running").stdout.strip() or "0")
                stop_application(installation)
            else:
                # A crash-looping application is stopped before anything reads its storage.
                stop_application(installation)
                app_command(installation, "maintenance", "on", running=False)
            print("Application stopped. Taking a snapshot of management storage…", flush=True)
            step = "taking the snapshot"
            rollback_dir.mkdir(mode=0o700)
            saved = {}
            for name in UPDATE_FILES:
                source = root / name
                info = source.stat()
                destination = rollback_dir / "files" / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, destination)
                saved[name] = [stat.S_IMODE(info.st_mode), info.st_uid, info.st_gid]
            store_file(root, state["images"]["application"], "store-snapshot", "/data/" + SNAPSHOT_DB)
            meta = {"format": 1, "from": state["release"], "to": release["version"], "from_images": state["images"], "files": saved, "app_was_running": was_running, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
            json_write(rollback_dir / "meta.json", meta)
            fsync_dir(rollback_dir)
            snapshot_ready = True
            # Apply. Sessions are signed out: the new generation changes every session scope.
            step = "switching to the new release"
            caddy_version = run(["docker", "run", "--rm", "--network", "none", release["images"]["caddy"], "caddy", "version"], timeout=120).stdout.strip()
            json_write(root / "state.json", new_state)
            atomic(root / "compose.env", compose_env(root, new_state, cfg), 0o600)
            json_write(root / "config/install.json", dict(cfg, release=release["version"], generation=uuid.uuid4().hex, caddy_version=caddy_version), 0o644)
            write_pgfyctl(root, target)
            installation = Installation(root)
            step = "applying the new release's host configuration"
            run_converge(installation, target, lock_fd)
            print("Starting the new release…", flush=True)
            step = "starting the new release"
            installation.compose("up", "-d", "--force-recreate", "postgres", "application", "caddy", timeout=180)
            step = "verifying readiness"
            installation.verify()
            # Commit point: from here on the new release stays, whatever happens during cleanup.
            (rollback_dir / "meta.json").unlink()
            committed = True
            fsync_dir(rollback_dir)
        except BaseException as error:
            _shield["on"] = True
            try:
                with signals(signal.SIG_IGN):
                    if not committed:
                        reason = f"{step}: {error or type(error).__name__}"
                        if snapshot_ready:
                            raise InstallError(rollback_update(root, reason=reason)) from error
                        shutil.rmtree(rollback_dir, ignore_errors=True)
                        try:
                            discard_snapshot(root, state["images"]["application"])
                        except InstallError:
                            pass
                        unquiesce(installation, was_running)
                        if isinstance(error, InstallError):
                            raise
                        raise InstallError(f"Update stopped while {reason}; nothing was changed and the application was restarted.") from error
            finally:
                _shield["on"] = False
    finish = [("turn maintenance off", lambda: app_command(installation, "maintenance", "off")),
              ("remove the storage snapshot", lambda: discard_snapshot(root, release["images"]["application"])),
              ("remove the hostname rollback record", lambda: (root / "access-rollback.json").unlink(missing_ok=True)),
              ("remove update-rollback/", lambda: shutil.rmtree(rollback_dir))]
    for step, action in finish:
        try:
            action()
        except (InstallError, OSError) as error:
            print(f"Warning: updated, but could not {step}: {error}. Run pgfyctl maintenance off and remove leftovers by hand.")
    print(f"Updated to {release['version']} and verified: {cfg['origin']}. Sign in again.")

def reexec_rollback(root):
    """A rollback is performed by the release being restored, whichever pgfyctl the operator ran."""
    meta_path = root / "update-rollback/meta.json"
    if not meta_path.exists():
        return
    previous = read_json(meta_path).get("from", "")
    installer = root / "releases" / previous / "installer.py"
    if VERSION.fullmatch(previous) and installer.is_file() and Path(__file__).resolve() != installer.resolve():
        os.execv(sys.executable, [sys.executable, str(installer), "--dir", str(root), "rollback-update"])

def unquiesce(installation, was_running):
    """Undo quiescing when nothing was changed yet: start the application and resume jobs."""
    installation.compose("up", "-d", "application", timeout=180, check=False)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline and app_status(installation) != "running":
        time.sleep(2)
    if app_command(installation, "maintenance", "off", check=False).returncode:
        print("Warning: backups are still paused. Run pgfyctl maintenance off once the application is running.")
    if not was_running:
        print("Note: the application was not running before the update; it has been started.")

def rollback_update(root, reason=None):
    """Restore the release, configuration and management storage captured before an update."""
    rollback_dir = root / "update-rollback"
    meta_path = rollback_dir / "meta.json"
    if not meta_path.exists():
        # The update stopped before its snapshot was complete: nothing else changed.
        installation = Installation(root)
        unquiesce(installation, True)
        shutil.rmtree(rollback_dir, ignore_errors=True)
        return "No update was applied; the application was restarted."
    meta = read_json(meta_path)
    if meta.get("format") != 1:
        raise InstallError("update-rollback/ was written by an unknown installer version; restore it by hand.")
    old_image = meta["from_images"]["application"]
    try:
        store_file(root, old_image, "store-restore", "--check", "/data/" + SNAPSHOT_DB)
    except InstallError as error:
        raise InstallError(f"The storage snapshot is missing or damaged, so nothing was rolled back. Keep update-rollback/ and data/sqlite/{SNAPSHOT_DB}; run pgfyctl diagnostics.") from error
    installation = Installation(root)
    installation.compose("stop", "application", timeout=120, check=False)
    for name, (mode, uid, gid) in meta["files"].items():
        atomic(root / name, (rollback_dir / "files" / name).read_bytes(), mode, uid, gid)
    installation = Installation(root)
    stop_application(installation, f"The application did not stop during the rollback. update-rollback/ is kept; run pgfyctl rollback-update to retry.")
    cfg = installation.config()
    json_write(root / "config/install.json", dict(cfg, generation=uuid.uuid4().hex), 0o644)
    store_file(root, old_image, "store-restore", "/data/" + SNAPSHOT_DB)
    installation.compose("up", "-d", "--force-recreate", "postgres", "application", "caddy", timeout=180)
    try:
        installation.verify()
    except InstallError as error:
        raise InstallError(f"Rollback to {meta['from']} did not reach readiness. update-rollback/ is kept; run pgfyctl diagnostics, then pgfyctl rollback-update to retry.") from error
    if app_command(installation, "maintenance", "off", check=False).returncode:
        print("Warning: backups are still paused. Run pgfyctl maintenance off.")
    discard_snapshot(root, old_image)
    shutil.rmtree(rollback_dir)
    if reason:
        return f"Update to {meta['to']} failed while {reason}. Restored {meta['from']}; it is ready. Sign in again."
    return f"Rolled back the interrupted update to {meta['to']}. Restored {meta['from']}; it is ready. Sign in again."

def diagnostics(installation):
    cfg = installation.config()
    print(f"Release: {installation.state['release']}\nAccess: {cfg['mode']} {cfg['origin']}\nVolume prefix: {installation.state['volume_prefix']}")
    if not (installation.root / "compose.env").exists():
        print("Configuration is incomplete. Correct the installation error and rerun the same release bundle. Existing state is preserved.")
        return
    print("PostgreSQL initialization:", installation.inspect_data())
    for service in ("application", "postgres", "caddy"):
        identifier = installation.container(service)
        if not identifier:
            print(service + ": absent")
            continue
        result = run(["docker", "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", identifier], check=False)
        print(service + ": " + result.stdout.strip())
    result = installation.compose("exec", "-T", "application", "pgfy", "health", "ready", check=False)
    print("Authenticated dependency readiness:", "ready" if result.returncode == 0 else "not ready")
    result = installation.compose("exec", "-T", "application", "pgfy", "maintenance", "status", check=False)
    maintenance = result.stdout.strip() if result.returncode == 0 else "unknown"
    print("Maintenance (backups and changes paused):", maintenance + (" — run pgfyctl maintenance off if no update is running" if maintenance == "on" else ""))
    if (installation.root / "update-rollback").exists():
        print("An unfinished update left update-rollback/. Run pgfyctl rollback-update.")
    print("No secrets or raw logs are included. Bootstrap credentials stay in restricted host files.")

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", default=DEFAULT_ROOT)
    sub = parser.add_subparsers(dest="command", required=True)
    install_parser = sub.add_parser("install")
    install_parser.add_argument("--bundle", required=True)
    install_parser.add_argument("--dir", default=argparse.SUPPRESS)
    modes = install_parser.add_mutually_exclusive_group()
    modes.add_argument("--hostname")
    modes.add_argument("--tunnel", action="store_true")
    sub.add_parser("diagnostics")
    sub.add_parser("setup-token")
    hostname_parser = sub.add_parser("hostname")
    hostname_parser.add_argument("hostname")
    sub.add_parser("tunnel")
    sub.add_parser("rollback-hostname")
    sub.add_parser("sync-db-cert")
    update_parser = sub.add_parser("update", help="update to an extracted, newer release bundle")
    update_parser.add_argument("bundle")
    update_parser.add_argument("--drain", action="store_true", help="wait for a running backup or restore instead of refusing")
    sub.add_parser("rollback-update", help="finish rolling back an interrupted update")
    maintenance_parser = sub.add_parser("maintenance", help="show or clear the pause an update puts on backups and changes")
    maintenance_parser.add_argument("action", choices=("status", "off"))
    converge_parser = sub.add_parser("converge", help=argparse.SUPPRESS)
    converge_parser.add_argument("--contract", required=True)
    converge_parser.add_argument("--lock-fd", type=int)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.exit(1, "Host administration requires root; run this command with sudo.\n")
    root = Path(args.dir).resolve()
    try:
        if args.command == "converge":
            # Runs under the lock held by the updating installer; taking it again would deadlock.
            converge(root, args.contract, args.lock_fd)
            return
        if args.command == "rollback-update":
            reexec_rollback(root)
        with locked(root) as lock_fd:
            if args.command == "install":
                install(args)
            elif args.command == "update":
                update(root, args.bundle, args.drain, lock_fd)
            elif args.command == "rollback-update":
                with signals(signal.SIG_IGN):
                    print(rollback_update(root))
            else:
                installation = Installation(Path(args.dir).resolve())
                if args.command == "diagnostics":
                    diagnostics(installation)
                elif args.command == "setup-token":
                    result = installation.compose("exec", "-T", "application", "pgfy", "setup-token", check=False)
                    if result.returncode:
                        raise InstallError("Token replacement rejected: setup may already be complete, the existing token may still be valid, or SQLite may be unavailable. Replacement is allowed only before setup and after token expiry.")
                    print("Setup token (expires in 30 minutes):\n" + result.stdout.strip())
                elif args.command == "hostname":
                    change_access(installation, "https", args.hostname)
                elif args.command == "tunnel":
                    change_access(installation, "tunnel")
                elif args.command == "sync-db-cert":
                    sync_db_cert(installation)
                elif args.command == "maintenance":
                    running = app_status(installation) == "running"
                    result = app_command(installation, "maintenance", args.action, running=running)
                    print(result.stdout.strip() or "Backups and changes resumed.")
                else:
                    change_access(installation, "", rollback=True)
    except (InstallError, OSError, ValueError, KeyError) as error:
        parser.exit(1, f"Pgfy: {error}\n")

if __name__ == "__main__":
    main()
