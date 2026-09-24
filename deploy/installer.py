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

def run(args, *, timeout=60, capture=True, check=True, input=None):
    try:
        result = subprocess.run([str(a) for a in args], input=input, text=True, capture_output=capture, timeout=timeout, check=False)
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

@contextlib.contextmanager
def locked(root):
    # The lock lives outside install state, so even two first-time runs serialize.
    lock_path = "/run/lock/pgfy-" + hashlib.sha256(str(root).encode()).hexdigest()[:16] + ".lock"
    with open(lock_path, "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise InstallError("Another installer or host command is running.") from error
        yield

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

def verify_bundle(bundle):
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
    if not release["images"]["postgres"].startswith("postgres:18.6-bookworm@"):
        raise InstallError("This release requires PostgreSQL 18.6 bookworm.")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?", release["version"]):
        raise InstallError("Invalid release version.")
    return release

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
            raise InstallError("Reruns must use the installed release and image digests. Updates require a separate procedure.")
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
        staging = root / "releases" / (state["release"] + ".staging")
        staging.mkdir(parents=True, exist_ok=True)
        for path in bundle.rglob("*"):
            if path.is_file() and not path.is_symlink():
                target = staging / path.relative_to(bundle)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(path, target)
                os.chmod(target, 0o644)
        verify_bundle(staging)
        staging.rename(installation.bundle)
    else:
        verify_bundle(installation.bundle)
    atomic(root / "pgfyctl", f'#!/usr/bin/env bash\nexec python3 "{installation.bundle}/installer.py" --dir "{root}" "$@"\n', 0o700)
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
    images = state["images"]
    for image in images.values():
        result = run(["docker", "pull", "--platform", "linux/amd64", image], timeout=600, check=False)
        if result.returncode:
            raise InstallError("Pinned image pull failed. Check registry access and retry this same bundle; existing state is preserved.")
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
    installation.validate_caddy()
    sqlite_path = root / "data/sqlite/pgfy.db"
    if not sqlite_path.exists():
        if data_state != "empty" or state["stage"] != "preparing" or any((root / "data/sqlite").iterdir()):
            raise InstallError("Existing management storage is missing. Restore SQLite; setup will not be reopened.")
        installation.compose("run", "--rm", "--no-deps", "application", "initialize-store")
    installation.compose("up", "-d", "postgres", "application", "caddy", timeout=180)
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
        new = saved["config"]
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
        if rollback:
            atomic(root / "config/caddy/Caddyfile", saved["caddyfile"], 0o644)
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
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.exit(1, "Host administration requires root; run this command with sudo.\n")
    try:
        with locked(Path(args.dir).resolve()):
            if args.command == "install":
                install(args)
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
                else:
                    change_access(installation, "", rollback=True)
    except (InstallError, OSError, ValueError, KeyError) as error:
        parser.exit(1, f"Pgfy: {error}\n")

if __name__ == "__main__":
    main()
