#!/usr/bin/env python3
"""Disposable Compose integration tests; never points at /opt/firstcommit.

Only resources bearing a fresh pgfy_test_* identity are created and removed.
Run after scripts/build-image.py. Public ports are never opened by this fixture.
"""
import http.cookiejar
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("installer", ROOT / "deploy/installer.py")
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)

def run(args, **kwargs):
    return subprocess.run([str(x) for x in args], text=True, capture_output=True, check=True, timeout=240, **kwargs)

def main():
    started = time.monotonic()
    images = json.loads((ROOT / "deploy/images.lock.json").read_text())
    application_image = os.environ.get("PGFY_TEST_IMAGE", "pgfy:dev")
    identifier = uuid.uuid4().hex
    project = "pgfy_test_" + identifier[:12]
    host.available_ports("tunnel")
    evidence = {"checks": [], "images": images, "host": "local Docker integration; not the two-host acceptance gate"}
    def passed(name):
        evidence["checks"].append(name)
        print("PASS:", name, flush=True)
    with tempfile.TemporaryDirectory(prefix="pgfy-integration-") as temporary:
        directory = Path(temporary)
        directory.chmod(0o755)
        for name in ("config/caddy", "secrets", "data/sqlite"):
            (directory / name).mkdir(parents=True)
        database_subnet, proxy_subnet = host.select_subnets()
        cfg = {"id": identifier, "mode": "tunnel", "hostname": "", "origin": "http://127.0.0.1:8080", "generation": "1", "release": "test", "caddy_version": "test", "docker_version": "test", "compose_version": "test"}
        (directory / "config/install.json").write_text(json.dumps(cfg))
        (directory / "config/installation-id").write_text(identifier)
        (directory / "config/pg_hba.conf").write_text(host.pg_hba(database_subnet))
        (directory / "config/caddy/Caddyfile").write_text(host.caddyfile(cfg))
        (directory / "state.json").write_text(json.dumps({"release": "test", "volume_prefix": project, "images": images}))
        installation = host.Installation(directory)
        secret_values = {"bootstrap_password": secrets.token_hex(32).encode(), "health_password": secrets.token_hex(32).encode(), "encryption_key": secrets.token_bytes(32)}
        for name, value in secret_values.items():
            (directory / "secrets" / name).write_bytes(value)
        env = {"INSTALL_DIR": str(directory), "APP_IMAGE": application_image, "POSTGRES_IMAGE": images["postgres"], "CADDY_IMAGE": images["caddy"], "VOLUME_PREFIX": project, "DATABASE_SUBNET": database_subnet, "PROXY_SUBNET": proxy_subnet}
        (directory / "compose.env").write_text("\n".join(f"{k}={v}" for k, v in env.items()))
        base = ["docker", "compose", "--project-name", project, "--env-file", directory / "compose.env", "-f", ROOT / "deploy/compose.yaml"]
        def compose(*args, **kwargs):
            return run([*base, "-f", ROOT / "deploy/compose.tunnel.yaml", *args], **kwargs)
        helper = ["docker", "run", "--rm", "--network", "none", "--user", "0:0", "-v", f"{directory}:/fixture", "--entrypoint", "sh", application_image, "-c"]
        run([*helper, "chown 10001:10001 /fixture/data/sqlite /fixture/secrets/encryption_key; chmod 700 /fixture/data/sqlite; chmod 400 /fixture/secrets/encryption_key; chown 999:999 /fixture/secrets/bootstrap_password; chmod 400 /fixture/secrets/bootstrap_password; chown 10001:999 /fixture/secrets/health_password; chmod 440 /fixture/secrets/health_password"])
        jar = http.cookiejar.CookieJar()
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
        def request(path, body=None, csrf=None):
            headers = {"Content-Type": "application/json", "Origin": cfg["origin"]}
            if csrf:
                headers["X-CSRF-Token"] = csrf
            req = urllib.request.Request(cfg["origin"] + path, data=json.dumps(body).encode() if body is not None else None, headers=headers)
            try:
                with client.open(req, timeout=8) as response:
                    content = response.read()
                    return response.status, json.loads(content) if content and response.headers.get_content_type() == "application/json" else content.decode()
            except urllib.error.HTTPError as error:
                return error.code, error.read().decode()
        def wait_ready():
            deadline = time.monotonic() + 150
            while time.monotonic() < deadline:
                try:
                    if request("/health/ready")[0] == 200:
                        return
                except OSError:
                    pass
                time.sleep(2)
            # Logs are retained only in a local test artifact; assertions below check secret redaction.
            logs = compose("logs", "--no-color").stdout
            (ROOT / ".cache/integration-failure.log").write_text(logs)
            raise AssertionError("Compose never became ready; inspect .cache/integration-failure.log")
        def sql(statement, health=False):
            secret = "health_password" if health else "bootstrap_password"
            user = "pgfy_health" if health else "pgfy_bootstrap"
            network = "-h 127.0.0.1" if health else ""
            command = f'export PGPASSWORD="$(cat /run/secrets/{secret})"; exec psql {network} -U {user} -d pgfy_system -XAt --set ON_ERROR_STOP=1'
            return compose("exec", "-T", "postgres", "sh", "-c", command, input=statement).stdout.strip()
        try:
            (ROOT / ".cache").mkdir(exist_ok=True)
            compose("config", "--quiet")
            run([*base, "-f", ROOT / "deploy/compose.https.yaml", "config", "--quiet"])
            compose("pull", "postgres", "caddy")
            compose("run", "--rm", "--no-deps", "application", "initialize-store")
            compose("up", "-d")
            wait_ready()
            passed("three-service startup, authenticated PostgreSQL readiness, SQLite, Caddy routing")
            assert "<div id=\"root\">" in request("/")[1]
            token = compose("exec", "-T", "application", "pgfy", "setup-token").stdout.strip()
            password = "integration passphrase that is long"
            assert request("/api/v1/settings")[0] == 401
            assert request("/api/v1/setup", {"email": "admin@example.com", "password": password, "token": token})[0] == 201
            code, session = request("/api/v1/auth/session")
            assert code == 200
            assert request("/api/v1/settings")[0] == 200
            passed("single-admin setup, cookie session, authenticated settings")
            assert sql("SHOW server_version;").startswith("18.6")
            for tool in ("psql", "pg_dump", "pg_restore"):
                assert "18.6" in compose("exec", "-T", "application", tool, "--version").stdout
            assert compose("exec", "-T", "application", "id", "-u").stdout.strip() == "10001"
            assert sql("SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication FROM pg_roles WHERE rolname=current_user;", health=True) == "f"
            passed("PostgreSQL/client versions match; application non-root; health role restricted")
            postgres_id = compose("ps", "-q", "postgres").stdout.strip()
            app_id = compose("ps", "-q", "application").stdout.strip()
            caddy_id = compose("ps", "-q", "caddy").stdout.strip()
            inspected = json.loads(run(["docker", "inspect", postgres_id, app_id, caddy_id]).stdout)
            assert not inspected[0]["HostConfig"]["PortBindings"]
            assert not inspected[1]["HostConfig"]["PortBindings"]
            assert all(binding["HostIp"] == "127.0.0.1" for bindings in inspected[2]["HostConfig"]["PortBindings"].values() for binding in bindings)
            assert not any(mount["Destination"] in ("/var/run/docker.sock", "/run/secrets/bootstrap_password", "/etc/caddy", "/data/caddy") for mount in inspected[1]["Mounts"])
            forbidden = compose("exec", "-T", "caddy", "sh", "-c", "if wget -q -T 2 -O /dev/null http://127.0.0.1:2019/config/; then exit 1; fi")
            assert forbidden.returncode == 0
            passed("no app/database host ports, loopback-only proxy, no dashboard bootstrap/Docker access, Caddy admin disabled")
            sql("CREATE TABLE pgfy_internal.recognizable (value text); INSERT INTO pgfy_internal.recognizable VALUES ('phase-one-record');")
            volumes_before = sorted(m["Name"] for item in inspected for m in item["Mounts"] if m["Type"] == "volume")
            compose("restart")
            wait_ready()
            assert sql("SELECT value FROM pgfy_internal.recognizable;") == "phase-one-record"
            assert request("/api/v1/auth/session")[0] == 200
            compose("up", "-d")
            wait_ready()
            assert request("/api/v1/setup")[1]["available"] is False
            current_ids = compose("ps", "-q").stdout.split()
            current = json.loads(run(["docker", "inspect", *current_ids]).stdout)
            assert volumes_before == sorted(m["Name"] for item in current for m in item["Mounts"] if m["Type"] == "volume")
            passed("container restart and Compose rerun preserve records, admin, and sessions")
            compose("stop", "postgres")
            assert request("/health/live")[0] == 200
            assert request("/health/ready")[0] == 503
            assert request("/api/v1/system/status")[0] == 200
            assert request("/api/v1/auth/logout", {}, session["csrf_token"])[0] == 204
            assert request("/api/v1/auth/login", {"email": "admin@example.com", "password": password})[0] == 200
            assert request("/")[0] == 200
            passed("PostgreSQL outage preserves dashboard and login while readiness fails")
            compose("start", "postgres")
            wait_ready()
            compose("stop", "application")
            run([*helper, "mv /fixture/data/sqlite/pgfy.db /fixture/data/sqlite/pgfy.db.saved"])
            compose("start", "application")
            for _ in range(30):
                try:
                    if request("/health/live")[0] == 200:
                        break
                except OSError:
                    pass
                time.sleep(1)
            assert request("/health/ready")[0] == 503
            assert request("/api/v1/setup")[0] == 503
            assert request("/")[0] == 200
            assert not (directory / "data/sqlite/pgfy.db").exists()
            compose("stop", "application")
            run([*helper, "mv /fixture/data/sqlite/pgfy.db.saved /fixture/data/sqlite/pgfy.db"])
            compose("start", "application")
            wait_ready()
            assert request("/api/v1/setup")[1]["available"] is False
            passed("missing SQLite fails closed without reopening setup; restoring metadata recovers access")
            # Deliberately remove only the disposable fixture's final init marker, then restore it.
            assert installation.inspect_data() == identifier
            compose("exec", "-T", "postgres", "sh", "-c", 'mv "$PGDATA/.pgfy-initialized" "$PGDATA/.pgfy-initialized.saved"')
            assert installation.inspect_data() == "partial"
            failed = subprocess.run([*base, "-f", ROOT / "deploy/compose.tunnel.yaml", "exec", "-T", "postgres", "bash", "/usr/local/bin/pgfy-postgres-health"], capture_output=True)
            assert failed.returncode != 0
            compose("exec", "-T", "postgres", "sh", "-c", 'mv "$PGDATA/.pgfy-initialized.saved" "$PGDATA/.pgfy-initialized"')
            passed("partial initialization marker failure detected despite a running PostgreSQL server")
            logs = compose("logs", "--no-color").stdout
            sensitive_values = [token, password, secret_values["bootstrap_password"].decode(), secret_values["health_password"].decode(), *[cookie.value for cookie in jar]]
            for value in sensitive_values:
                assert value not in logs
            passed("setup token, password, and database credentials absent from container logs")
            evidence["container_stats"] = compose("stats", "--no-stream", "--format", "json").stdout
            evidence["seconds"] = round(time.monotonic() - started, 2)
            evidence["volume_identities"] = volumes_before
            (ROOT / ".cache/integration-results.json").write_text(json.dumps(evidence, indent=2) + "\n")
        finally:
            # These resources are generated test artifacts, not installation data.
            subprocess.run([*base, "-f", ROOT / "deploy/compose.tunnel.yaml", "down", "--volumes", "--remove-orphans"], capture_output=True, timeout=90)
            run([*helper, f"chown -R {os.getuid()}:{os.getgid()} /fixture; chmod -R u+rwX /fixture"])

if __name__ == "__main__":
    main()
