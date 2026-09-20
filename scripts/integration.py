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

class patch_atomic_owner:
    """The installer chowns generated files to service users; the unprivileged fixture fixes ownership later."""
    def __enter__(self):
        self.original = host.atomic
        host.atomic = lambda path, data, mode=0o600, uid=None, gid=None: self.original(path, data, mode)
    def __exit__(self, *exc):
        host.atomic = self.original

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
        for name in ("config/caddy", "config/pg/managed", "config/postgres-tls", "secrets", "data/sqlite", "data/work", "minio"):
            (directory / name).mkdir(parents=True)
        database_subnet, proxy_subnet, public_subnet = host.select_subnets()
        cfg = {"id": identifier, "mode": "tunnel", "hostname": "", "origin": "http://127.0.0.1:8080", "generation": "1", "release": "test", "caddy_version": "test", "docker_version": "test", "compose_version": "test"}
        (directory / "config/install.json").write_text(json.dumps(cfg))
        (directory / "config/installation-id").write_text(identifier)
        (directory / "config/pg/pg_hba.conf").write_text(host.pg_hba(database_subnet))
        with patch_atomic_owner():
            host.placeholder_certificate(directory / "config/postgres-tls")
        (directory / "config/caddy/Caddyfile").write_text(host.caddyfile(cfg))
        (directory / "state.json").write_text(json.dumps({"release": "test", "volume_prefix": project, "images": images, "database_subnet": database_subnet, "proxy_subnet": proxy_subnet, "public_subnet": public_subnet}))
        installation = host.Installation(directory)
        secret_values = {"bootstrap_password": secrets.token_hex(32).encode(), "health_password": secrets.token_hex(32).encode(), "management_password": secrets.token_hex(32).encode(), "encryption_key": secrets.token_bytes(32)}
        for name, value in secret_values.items():
            (directory / "secrets" / name).write_bytes(value)
        (directory / "compose.env").write_text(host.compose_env(directory, dict(installation.state, images=dict(images, application=application_image)), cfg))
        base = ["docker", "compose", "--project-name", project, "--env-file", directory / "compose.env", "-f", ROOT / "deploy/compose.yaml"]
        def compose(*args, **kwargs):
            return run([*base, "-f", ROOT / "deploy/compose.tunnel.yaml", *args], **kwargs)
        helper = ["docker", "run", "--rm", "--network", "none", "--user", "0:0", "-v", f"{directory}:/fixture", "--entrypoint", "sh", application_image, "-c"]
        run([*helper, "chown 10001:10001 /fixture/data/sqlite /fixture/data/work /fixture/secrets/encryption_key; chown 10001:999 /fixture/config/pg/managed; chmod 700 /fixture/data/sqlite /fixture/data/work; chmod 750 /fixture/config/pg/managed; chmod 400 /fixture/secrets/encryption_key; chown 999:999 /fixture/secrets/bootstrap_password /fixture/config/postgres-tls/server.key /fixture/config/postgres-tls/server.crt; chmod 400 /fixture/secrets/bootstrap_password; chmod 600 /fixture/config/postgres-tls/server.key; chown 10001:999 /fixture/secrets/health_password /fixture/secrets/management_password; chmod 440 /fixture/secrets/health_password /fixture/secrets/management_password"])
        jar = http.cookiejar.CookieJar()
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
        def request(path, body=None, csrf=None, method=None):
            headers = {"Content-Type": "application/json", "Origin": cfg["origin"]}
            if csrf:
                headers["X-CSRF-Token"] = csrf
            req = urllib.request.Request(cfg["origin"] + path, data=json.dumps(body).encode() if body is not None else None, headers=headers, method=method)
            try:
                with client.open(req, timeout=70) as response:
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
            assert compose("exec", "-T", "application", "test", "-s", "/etc/ssl/certs/ca-certificates.crt").returncode == 0, "CA bundle missing: object-storage TLS cannot be verified"
            assert compose("exec", "-T", "application", "id", "-u").stdout.strip() == "10001"
            assert sql("SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication FROM pg_roles WHERE rolname=current_user;", health=True) == "f"
            passed("PostgreSQL/client versions match; application non-root; health role restricted")
            postgres_id = compose("ps", "-q", "postgres").stdout.strip()
            app_id = compose("ps", "-q", "application").stdout.strip()
            caddy_id = compose("ps", "-q", "caddy").stdout.strip()
            inspected = json.loads(run(["docker", "inspect", postgres_id, app_id, caddy_id]).stdout)
            assert {k: [b["HostIp"] for b in v] for k, v in inspected[0]["HostConfig"]["PortBindings"].items()} == {"5432/tcp": ["127.0.0.1"]}
            assert not inspected[1]["HostConfig"]["PortBindings"]
            assert all(binding["HostIp"] == "127.0.0.1" for bindings in inspected[2]["HostConfig"]["PortBindings"].values() for binding in bindings)
            assert not any(mount["Destination"] in ("/var/run/docker.sock", "/run/secrets/bootstrap_password", "/etc/caddy", "/data/caddy") for mount in inspected[1]["Mounts"])
            forbidden = compose("exec", "-T", "caddy", "sh", "-c", "if wget -q -T 2 -O /dev/null http://127.0.0.1:2019/config/; then exit 1; fi")
            assert forbidden.returncode == 0
            passed("loopback-only database and proxy ports, no app host ports, no dashboard bootstrap/Docker access, Caddy admin disabled")
            projects = {}
            for name in ("Shop", "Blog"):
                code, created = request("/api/v1/projects", {"name": name}, session["csrf_token"])
                assert code == 202, (code, created)
                assert request("/api/v1/projects", {"name": name}, session["csrf_token"])[0] == 200, "repeat within a minute must be idempotent"
                deadline = time.monotonic() + 90
                while True:
                    code, body = request(f"/api/v1/projects/{created['id']}")
                    assert code == 200 and not body["project"]["failed"], body
                    if body["project"]["stage"] == "ready":
                        break
                    assert time.monotonic() < deadline, body
                    time.sleep(1)
                assert body["project"]["policy"]["state"] == "applied" and body["project"]["policy"]["applied_revision"] == 1, body["project"]["policy"]
                assert body["project"]["size_bytes"] > 0
                code, credentials = request(f"/api/v1/projects/{created['id']}/credentials")
                assert code == 200 and credentials["sslmode"] == "disable" and credentials["host"] == "127.0.0.1" and "sslrootcert" not in credentials["url"]
                projects[name] = dict(created, credentials=credentials)
            assert len(request("/api/v1/projects")[1]["projects"]) == 2
            assert sql("SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication FROM pg_roles WHERE rolname='" + projects["Shop"]["db_name"] + "';") == "f"
            passed("two projects provisioned with restricted roles, open-by-default policy applied, size measured")
            def psql(url, statement, network=project + "_dbpublic", extra=()):
                # A container on the published network is a remote client; host mode is the tunnel path.
                return subprocess.run(["docker", "run", "--rm", "--network", network, *extra, "--entrypoint", "psql", images["postgres"], url, "-XAt", "-c", statement], capture_output=True, text=True, timeout=60)
            def remote_url(name, sslmode="require", database=None):
                c = projects[name]["credentials"]
                return f"postgresql://{c['user']}:{c['password']}@postgres:5432/{database or c['database']}?sslmode={sslmode}"
            first = psql(remote_url("Shop"), "CREATE TABLE guestbook(entry text); INSERT INTO guestbook VALUES ('hello from shop'); SELECT entry FROM guestbook;")
            assert first.stdout.strip().endswith("hello from shop"), (first.stdout, first.stderr)
            assert psql(remote_url("Blog"), "SELECT current_database();").stdout.strip() == projects["Blog"]["db_name"]
            assert psql(remote_url("Shop", database=projects["Blog"]["db_name"]), "SELECT 1;").returncode != 0
            assert psql(remote_url("Shop", sslmode="disable"), "SELECT 1;").returncode != 0
            assert psql(remote_url("Shop", database="pgfy_system"), "SELECT 1;").returncode != 0
            assert psql(remote_url("Shop", database="postgres"), "SELECT 1;").returncode != 0
            passed("TLS application connections succeed; cross-project, non-TLS, and system-database access rejected")
            tunnel = projects["Shop"]["credentials"]["url"]
            assert psql(tunnel, "SELECT entry FROM guestbook;", network="host").stdout.strip() == "hello from shop"
            shop = projects["Shop"]["id"]
            code, policy = request(f"/api/v1/projects/{shop}/access", {"revision": 1, "addresses": ["203.0.113.0/24", "not-an-address"]}, session["csrf_token"], "PUT")
            assert code == 400, (code, policy)
            code, policy = request(f"/api/v1/projects/{shop}/access", {"revision": 1, "addresses": ["203.0.113.0/24"]}, session["csrf_token"], "PUT")
            assert code == 200 and policy["state"] == "applied" and policy["applied_revision"] == 2, (code, policy)
            assert request(f"/api/v1/projects/{shop}/access", {"revision": 1, "addresses": []}, session["csrf_token"], "PUT")[0] == 409
            assert psql(remote_url("Shop"), "SELECT 1;").returncode != 0
            assert psql(remote_url("Blog"), "SELECT 1;").returncode == 0
            assert psql(tunnel, "SELECT 1;", network="host").returncode == 0
            code, policy = request(f"/api/v1/projects/{shop}/access", {"revision": 2, "addresses": ["0.0.0.0/0"]}, session["csrf_token"], "PUT")
            assert code == 200 and policy["state"] == "applied"
            assert psql(remote_url("Shop"), "SELECT 1;").returncode == 0
            passed("per-project allowlist enforced after reload; stale revisions rejected; tunnel path unaffected")
            code, check = request(f"/api/v1/projects/{shop}/connection-checks", {}, session["csrf_token"])
            assert code == 201, (code, check)
            probe = subprocess.Popen(["docker", "run", "--rm", "--network", project + "_dbpublic", "--entrypoint", "psql", images["postgres"], remote_url("Shop") + "&application_name=" + check["application_name"], "-c", "select pg_sleep(15)"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(20):
                    code, state = request(f"/api/v1/projects/{shop}/connection-checks/{check['id']}")
                    if state["state"] == "successful":
                        break
                    time.sleep(1)
                assert state["state"] == "successful" and state["evidence"]["tls"] is True and state["evidence"]["client_addr"], state
                assert request(f"/api/v1/projects/{shop}")[1]["project"]["connections_now"]
            finally:
                probe.wait(timeout=60)
            passed("connection check observes the application's TLS session as evidence")
            # --- Backups against a disposable S3-compatible store on the proxy network ---
            minio_secret = secrets.token_hex(16)
            minio_name = project.replace("_", "-") + "-minio"  # S3 clients need a valid hostname
            (directory / "minio/pgfy-backups").mkdir()
            run(["docker", "run", "-d", "--name", minio_name, "--network", project + "_proxy", "-e", "MINIO_ROOT_USER=pgfytest", "-e", "MINIO_ROOT_PASSWORD=" + minio_secret, "-v", f"{directory}/minio:/data", images["minio_test"], "server", "/data"])
            time.sleep(3)
            storage = {"endpoint": f"http://{minio_name}:9000", "region": "us-east-1", "bucket": "pgfy-backups", "prefix": "pgfy/test", "access_key": "pgfytest", "secret_key": minio_secret, "session_token": "", "path_style": True}
            code, saved = request("/api/v1/settings/storage", storage, session["csrf_token"], "PUT")
            assert code == 200 and saved["settings"]["secret_key"].startswith("••••") and minio_secret not in json.dumps(saved), (code, saved)
            code, checked = request("/api/v1/settings/storage/check", {}, session["csrf_token"])
            assert code == 200 and checked["ok"] and [s["name"] for s in checked["steps"]] == ["upload", "list", "download", "cleanup"], (code, checked)
            code, discovered = request("/api/v1/recovery/backups")
            assert code == 200 and discovered["state"] == "ok" and discovered["backups"] == [], discovered
            passed("storage settings sealed and masked; upload/list/download/cleanup check passes")
            def wait_job(job_id, timeout=120):
                deadline = time.monotonic() + timeout
                while True:
                    code, job = request(f"/api/v1/jobs/{job_id}")
                    assert code == 200, (code, job)
                    if job["state"] not in ("queued", "running"):
                        return job
                    assert time.monotonic() < deadline, job
                    time.sleep(1)
            code, job = request(f"/api/v1/projects/{shop}/backups", {}, session["csrf_token"])
            assert code == 202, (code, job)
            assert request(f"/api/v1/projects/{shop}/backups", {}, session["csrf_token"])[0] == 409, "second heavy job must wait"
            job = wait_job(job["id"])
            assert job["state"] == "succeeded" and job["result"]["sha256"] and job["result"]["tables"] == [{"schema": "public", "name": "guestbook", "rows": 1}], job
            code, history = request(f"/api/v1/projects/{shop}/backups")
            assert code == 200 and len(history["backups"]) == 1 and history["next_scheduled_at"] > 0 and history["storage_configured"], history
            manifest_key = history["backups"][0]["object_key"]
            assert (directory / "minio/pgfy-backups/pgfy/test/backups" / projects["Shop"]["db_name"]).exists()
            passed("manual backup dumps within one snapshot, uploads archive then manifest, records history")
            # An upload that cannot finish must never become a published backup after a restart.
            run(["docker", "pause", minio_name])
            code, stuck = request(f"/api/v1/projects/{projects['Blog']['id']}/backups", {}, session["csrf_token"])
            assert code == 202, (code, stuck)
            for _ in range(30):
                if request(f"/api/v1/jobs/{stuck['id']}")[1]["stage"] == "upload_archive":
                    break
                time.sleep(0.5)
            compose("kill", "application")
            compose("up", "-d", "application")
            wait_ready()
            run(["docker", "unpause", minio_name])
            interrupted = request(f"/api/v1/jobs/{stuck['id']}")[1]
            assert interrupted["state"] == "interrupted" and interrupted["stage"] == "upload_archive", interrupted
            # The schedule notices Blog has no backup yet and catches up after the restart; the
            # interrupted job itself must never have published a manifest.
            for _ in range(90):
                blog_history = request(f"/api/v1/projects/{projects['Blog']['id']}/backups")[1]
                if blog_history["backups"] and not any(j["state"] in ("queued", "running") for j in blog_history["jobs"]):
                    break
                time.sleep(1)
            assert blog_history["backups"] and blog_history["backups"][0]["job_id"] != stuck["id"], blog_history
            assert {j["state"] for j in blog_history["jobs"]} == {"interrupted", "succeeded"}, blog_history["jobs"]
            assert run([*helper, "ls -A /fixture/data/work"]).stdout.strip() == "", "workspace not cleaned"
            passed("restart marks the in-flight backup interrupted without a manifest; the daily schedule catches up; workspace clean")
            code, restore = request("/api/v1/recovery/restores", {"manifest_key": manifest_key, "name": "Shop restored"}, session["csrf_token"])
            assert code == 202, (code, restore)
            job = wait_job(restore["job"]["id"], 180)
            assert job["state"] == "succeeded" and job["result"]["verified"] and all(c["ok"] for c in job["result"]["checks"]), job
            restored = request(f"/api/v1/projects/{restore['project']['id']}")[1]["project"]
            assert restored["stage"] == "ready" and restored["policy"]["state"] == "applied"
            code, restored_credentials = request(f"/api/v1/projects/{restore['project']['id']}/credentials")
            projects["Restored"] = dict(restore["project"], credentials=restored_credentials)
            assert psql(remote_url("Restored"), "SELECT entry FROM guestbook;").stdout.strip() == "hello from shop"
            assert "after restore" in psql(remote_url("Restored"), "INSERT INTO guestbook VALUES ('after restore') RETURNING entry;").stdout
            assert psql(remote_url("Shop"), "SELECT count(*) FROM guestbook;").stdout.strip() == "1", "original database must stay untouched"
            assert request("/api/v1/recovery/restores", {"manifest_key": "pgfy/test/backups/nope/manifest.json", "name": "x"}, session["csrf_token"])[0] == 400
            passed("restore into a new project verifies checksum, row counts, ownership; original untouched")
            sql("CREATE TABLE pgfy_internal.recognizable (value text); INSERT INTO pgfy_internal.recognizable VALUES ('phase-one-record');")
            volumes_before = sorted(m["Name"] for item in inspected for m in item["Mounts"] if m["Type"] == "volume")
            compose("restart")
            wait_ready()
            assert sql("SELECT value FROM pgfy_internal.recognizable;") == "phase-one-record"
            assert psql(remote_url("Shop"), "SELECT entry FROM guestbook;").stdout.strip() == "hello from shop"
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
            run([*helper, "test ! -e /fixture/data/sqlite/pgfy.db"])
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
            sensitive_values = [token, password, secret_values["bootstrap_password"].decode(), secret_values["health_password"].decode(), secret_values["management_password"].decode(), *[p["credentials"]["password"] for p in projects.values()], *[cookie.value for cookie in jar]]
            for value in sensitive_values:
                assert value not in logs
            passed("setup token, password, and database credentials absent from container logs")
            evidence["container_stats"] = compose("stats", "--no-stream", "--format", "json").stdout
            evidence["seconds"] = round(time.monotonic() - started, 2)
            evidence["volume_identities"] = volumes_before
            (ROOT / ".cache/integration-results.json").write_text(json.dumps(evidence, indent=2) + "\n")
            if os.environ.get("PGFY_INTEGRATION_HOLD"):
                # Developer aid: keep the stack up for manual inspection at http://127.0.0.1:8080.
                (ROOT / ".cache/integration-hold.json").write_text(json.dumps({"email": "admin@example.com", "password": password, "projects": projects}))
                print(f"Holding the fixture for {os.environ['PGFY_INTEGRATION_HOLD']} seconds; credentials in .cache/integration-hold.json", flush=True)
                time.sleep(int(os.environ["PGFY_INTEGRATION_HOLD"]))
        finally:
            # These resources are generated test artifacts, not installation data.
            subprocess.run([*base, "-f", ROOT / "deploy/compose.tunnel.yaml", "down", "--volumes", "--remove-orphans"], capture_output=True, timeout=90)
            subprocess.run(["docker", "rm", "-f", project.replace("_", "-") + "-minio"], capture_output=True, timeout=60)
            run([*helper, f"chown -R {os.getuid()}:{os.getgid()} /fixture; chmod -R u+rwX /fixture"])

if __name__ == "__main__":
    main()
