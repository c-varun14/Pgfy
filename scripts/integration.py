#!/usr/bin/env python3
"""Disposable Compose integration tests; never points at /opt/firstcommit.

Only resources bearing a fresh pgfy_test_* identity are created and removed.
Run after scripts/build-image.py. Public ports are never opened by this fixture.
"""
import base64
import hashlib
import hmac
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

def write_bundle(directory, version, application_image, images):
    """A release bundle from this checkout's deploy files, pointing at a locally built application image."""
    import hashlib
    directory.mkdir(parents=True)
    names = ["installer.py", "install.sh", "pgfyctl", "compose.yaml", "compose.https.yaml", "compose.tunnel.yaml", "postgres/init.sh", "postgres/health.sh"]
    for name in names:
        (directory / name).parent.mkdir(parents=True, exist_ok=True)
        (directory / name).write_bytes((ROOT / "deploy" / name).read_bytes())
    release = {"version": version, "images": {"application": application_image, "postgres": images["postgres"], "caddy": images["caddy"]}}
    (directory / "release.json").write_text(json.dumps(release))
    names.append("release.json")
    (directory / "SHA256SUMS").write_text("".join(f"{hashlib.sha256((directory / n).read_bytes()).hexdigest()}  {n}\n" for n in names))
    return release

def update_and_rollback(directory, installation, application_image, next_image, images, request, run, helper, password, passed):
    """pgfyctl update: a failure after the new release migrated rolls everything back; a clean update commits."""
    current = write_bundle(directory / "releases/v0.0.1", "v0.0.1", application_image, images)
    state = dict(installation.state, release="v0.0.1", images=current["images"], stage="installed")
    host.json_write(directory / "state.json", state)
    cfg = dict(installation.config(), release="v0.0.1")
    host.json_write(directory / "config/install.json", cfg, 0o644)
    host.atomic(directory / "compose.env", host.compose_env(directory, state, cfg))
    host.write_pgfyctl(directory, directory / "releases/v0.0.1")
    write_bundle(directory / "incoming/pgfy-v0.0.2", "v0.0.2", next_image, images)
    def probe(query):
        run([*helper, "mkdir -p /fixture/probe && cp /fixture/data/sqlite/pgfy.db* /fixture/probe/ && chmod -R a+rwX /fixture/probe"])
        import sqlite3
        with sqlite3.connect(directory / "probe/pgfy.db") as db:
            result = db.execute(query).fetchall()
        run([*helper, "rm -rf /fixture/probe"])
        return result
    def signed_in():
        assert request("/api/v1/auth/login", {"email": "admin@example.com", "password": password})[0] == 200
        code, session = request("/api/v1/auth/session")
        assert code == 200
        return session
    def project_ids():
        return sorted((p["id"], p["name"], p["stage"]) for p in request("/api/v1/projects")[1]["projects"])
    projects_before = project_ids()
    original = {"DIGEST": host.DIGEST, "pull_images": host.pull_images, "run_converge": host.run_converge, "verify": host.Installation.verify, "converge_host": host.converge_host}
    verified = []
    def verify_then_fail_once(self, require_dependencies=True):
        original["verify"](self, require_dependencies)
        verified.append(self.state["release"])
        if len(verified) == 1:
            migrated = probe("SELECT name FROM schema_migrations WHERE name='999_updatetest.sql'")
            assert migrated, "the new release did not migrate before the injected failure"
            raise host.InstallError("injected failure after the new release migrated")
    host.DIGEST = __import__("re").compile(r"[a-zA-Z0-9./:_-]+(@sha256:[a-f0-9]{64})?\Z")  # local images carry no registry digest
    host.pull_images = lambda images: None
    host.converge_host = lambda root, mode: None  # systemd units under /etc are not the fixture's to write
    host.run_converge = lambda installation, bundle, lock_fd: host.converge_installation(installation)
    host.Installation.verify = verify_then_fail_once
    try:
        with patch_atomic_owner():
            try:
                host.update(directory, directory / "incoming/pgfy-v0.0.2")
                raise AssertionError("the injected failure did not stop the update")
            except host.InstallError as error:
                assert "Restored v0.0.1" in str(error), error
            assert verified == ["v0.0.2", "v0.0.1"], verified
            assert host.read_json(directory / "state.json")["release"] == "v0.0.1"
            assert not (directory / "update-rollback").exists()
            assert not probe("SELECT name FROM schema_migrations WHERE name='999_updatetest.sql'"), "rollback kept the new schema"
            session = signed_in()
            assert project_ids() == projects_before
            status = request("/api/v1/system/status")[1]
            assert status["ready"] and not status["maintenance"] and status["versions"]["application"] != "v0.0.2"
            passed("an update that fails after migrating restores the previous release, its storage snapshot and configuration")
            host.update(directory, directory / "incoming/pgfy-v0.0.2")
            assert host.read_json(directory / "state.json")["release"] == "v0.0.2"
            assert "releases/v0.0.2/installer.py" in (directory / "pgfyctl").read_text()
            assert probe("SELECT count(*) FROM schema_migrations WHERE name='999_updatetest.sql'") == [(1,)]
            assert request("/api/v1/projects")[0] == 401, "sessions from before the update stayed valid"
            session = signed_in()
            assert project_ids() == projects_before
            status = request("/api/v1/system/status")[1]
            assert status["ready"] and not status["maintenance"] and status["versions"]["application"] == "v0.0.2"
            assert request("/api/v1/projects", {"name": "after-update"}, session["csrf_token"])[0] == 202
            passed("pgfyctl update migrates, verifies, resumes backups and signs sessions out")
    finally:
        host.DIGEST, host.pull_images, host.run_converge, host.Installation.verify, host.converge_host = original["DIGEST"], original["pull_images"], original["run_converge"], original["verify"], original["converge_host"]

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
        os.environ["PGFY_SCHEDULE_INTERVAL"] = "1m"  # scheduling passes the fixture can wait for
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
            code, frozen = request(f"/api/v1/projects/{shop}/writes", {"frozen": True}, session["csrf_token"], "PUT")
            assert code == 200 and frozen["frozen_at"] > 0, (code, frozen)
            assert request(f"/api/v1/projects/{shop}")[1]["project"]["frozen_at"] == frozen["frozen_at"]
            assert psql(remote_url("Shop"), "SELECT entry FROM guestbook;").stdout.strip() == "hello from shop", "reads must continue while frozen"
            rejected = psql(remote_url("Shop"), "INSERT INTO guestbook VALUES ('while frozen');")
            assert rejected.returncode != 0 and "read-only" in rejected.stderr, (rejected.stdout, rejected.stderr)
            assert psql(remote_url("Blog"), "CREATE TABLE t(x int); INSERT INTO t VALUES (1); SELECT x FROM t;").stdout.strip().endswith("\n1"), "other projects keep writing"
            assert request(f"/api/v1/projects/{shop}/writes", {"frozen": True}, session["csrf_token"], "PUT")[0] == 200, "freeze is idempotent"
            code, resumed = request(f"/api/v1/projects/{shop}/writes", {"frozen": False}, session["csrf_token"], "PUT")
            assert code == 200 and resumed["frozen_at"] == 0, (code, resumed)
            assert psql(remote_url("Shop"), "INSERT INTO guestbook VALUES ('after resume'); DELETE FROM guestbook WHERE entry='after resume'; SELECT count(*) FROM guestbook;").stdout.strip().endswith("\n1"), "writes must work again after resume"
            passed("freezing writes keeps reads, rejects writes, leaves other projects alone, and resumes")
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
            # --- Access and capacity: reserved slots, guardrails, limits, rotation ---
            shop_role, blog = projects["Shop"]["credentials"]["user"], projects["Blog"]["id"]
            assert sql("SHOW reserved_connections;") == "10"
            assert sql("SELECT pg_has_role('pgfy_mgmt','pg_use_reserved_connections','MEMBER') AND pg_has_role('pgfy_health','pg_use_reserved_connections','MEMBER');") == "t"
            def role_config(role):
                return sql(f"SELECT array_to_string(s.setconfig, ',') FROM pg_db_role_setting s JOIN pg_roles r ON r.oid=s.setrole WHERE r.rolname='{role}' AND s.setdatabase=0;")
            for expected in ("statement_timeout=60000ms", "idle_in_transaction_session_timeout=300000ms", "temp_file_limit=1048576kB", "lock_timeout=10000ms"):
                assert expected in role_config(shop_role), role_config(shop_role)
            assert psql(remote_url("Shop"), "SHOW statement_timeout;").stdout.strip() == "1min"
            refused = psql(remote_url("Shop"), "SET temp_file_limit = -1;")
            assert refused.returncode != 0 and "permission denied" in refused.stderr, refused.stderr
            assert psql(remote_url("Shop"), f"ALTER ROLE {shop_role} CONNECTION LIMIT 100;").returncode != 0
            limits = request(f"/api/v1/projects/{shop}")[1]["project"]["limits"]
            change = {key: limits[key] for key in ("statement_timeout_ms", "idle_in_transaction_ms", "temp_file_limit_kb", "lock_timeout_ms", "connection_limit")}
            code, updated = request(f"/api/v1/projects/{shop}/limits", dict(change, statement_timeout_ms=30000, connection_limit=30, revision=limits["revision"]), session["csrf_token"], "PUT")
            assert code == 200 and updated["applied_revision"] == updated["revision"], (code, updated)
            assert sql(f"SELECT rolconnlimit FROM pg_roles WHERE rolname='{shop_role}';") == "30"
            assert psql(remote_url("Shop"), "SHOW statement_timeout;").stdout.strip() == "30s"
            assert "default_transaction_read_only" not in role_config(shop_role), "limits must not disturb the write freeze setting"
            code, budget = request("/api/v1/system/connections")
            assert code == 200 and budget["reserved"] == 10 and budget["available"] == budget["max_connections"] - budget["superuser_reserved"] - 10, budget
            assert any(r["project"] == "Shop" and r["limit"] == 30 for r in budget["roles"]) and {r["role"] for r in budget["system"]} == {"pgfy_mgmt", "pgfy_health"}, budget
            passed("reserved slots for management and health; guardrails set, enforced where PostgreSQL allows, changeable per project; budget reported")
            old_url = remote_url("Blog")
            blog_role = projects["Blog"]["credentials"]["user"]
            holder = subprocess.Popen(["docker", "run", "--rm", "--network", project + "_dbpublic", "--entrypoint", "psql", images["postgres"], old_url, "-c", "select pg_sleep(60)"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(30):
                    if sql(f"SELECT count(*) FROM pg_stat_activity WHERE usename='{blog_role}';") != "0":
                        break
                    time.sleep(1)
                else:
                    raise AssertionError("the session holding the old password never connected")
                code, rotated = request(f"/api/v1/projects/{blog}/credentials/rotate", {}, session["csrf_token"])
                assert code == 200 and rotated["credentials"]["password"] != projects["Blog"]["credentials"]["password"], (code, rotated)
                assert holder.wait(timeout=30) != 0, "the session using the old password survived the rotation"
            finally:
                if holder.poll() is None:
                    holder.kill()
            assert psql(old_url, "SELECT 1;").returncode != 0, "the old password still works"
            projects["Blog"]["credentials"] = rotated["credentials"]
            assert psql(remote_url("Blog"), "SELECT x FROM t;").stdout.strip() == "1"
            code, revealed = request(f"/api/v1/projects/{blog}/credentials")
            assert code == 200 and revealed["password"] == rotated["credentials"]["password"] and not revealed["rotation_pending"]
            listed = {p["id"]: p["open_to_internet"] for p in request("/api/v1/projects")[1]["projects"]}
            for name in ("Shop", "Blog"):
                addresses = request(f"/api/v1/projects/{projects[name]['id']}")[1]["project"]["policy"]["addresses"]
                assert listed[projects[name]["id"]] == any(a in ("0.0.0.0/0", "::/0") for a in addresses), (name, addresses, listed)
            passed("rotation ends sessions using the old password, which stops working; the new one is the one revealed")
            # --- Backups against a disposable S3-compatible store on the proxy network ---
            minio_secret = secrets.token_hex(16)
            minio_name = project.replace("_", "-") + "-minio"  # S3 clients need a valid hostname
            (directory / "minio/pgfy-backups").mkdir()
            run(["docker", "run", "-d", "--name", minio_name, "--network", project + "_proxy", "-e", "MINIO_ROOT_USER=pgfytest", "-e", "MINIO_ROOT_PASSWORD=" + minio_secret, "-v", f"{directory}/minio:/data", os.environ.get("PGFY_TEST_MINIO_IMAGE", images["minio_test"]), "server", "/data"])
            time.sleep(3)
            storage = {"endpoint": f"http://{minio_name}:9000", "region": "us-east-1", "bucket": "pgfy-backups", "prefix": "pgfy/test", "access_key": "pgfytest", "secret_key": minio_secret, "session_token": "", "path_style": True, "private_endpoint": True, "bucket_protection": "versioning"}
            # A plaintext endpoint is only allowed for a private address, and a bucket
            # that reports versioning off is refused whichever protection is chosen.
            assert request("/api/v1/settings/storage", dict(storage, private_endpoint=False), session["csrf_token"], "PUT")[0] == 400, "plaintext public endpoint accepted"
            for protection in ("versioning", "acknowledged"):
                code, refused = request("/api/v1/settings/storage", dict(storage, bucket_protection=protection), session["csrf_token"], "PUT")
                assert code == 400 and "versioning" in str(refused), (protection, code, refused)
            def mc(command, **kwargs):
                # MinIO keeps each object as a directory of its own, so the fixture
                # manipulates the bucket through the S3 API rather than the disk.
                return run(["docker", "exec", "-i", minio_name, "sh", "-c", f"mc --quiet {command}"], **kwargs)
            run(["docker", "exec", minio_name, "sh", "-c", f"mc alias set fixture http://127.0.0.1:9000 pgfytest {minio_secret}"])
            mc("version enable fixture/pgfy-backups")
            def clone_backup(db_name, source, folder, rows=None):
                """Copy one backup to another timestamp, keeping the manifest consistent."""
                base = f"fixture/pgfy-backups/pgfy/test/backups/{db_name}"
                document = json.loads(mc(f"cat {base}/{source}/manifest.json").stdout)
                at = time.strptime(folder, "%Y%m%dT%H%M%SZ")
                document["created_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", at)
                document["archive_key"] = document["archive_key"].replace(source, folder)
                document["manifest_key"] = document["manifest_key"].replace(source, folder)
                if rows is not None:
                    for table in document["tables"]:
                        table["rows"] = rows
                mc(f"cp {base}/{source}/archive.dump {base}/{folder}/archive.dump")
                mc(f"pipe {base}/{folder}/manifest.json", input=json.dumps(document))
                return f"pgfy/test/backups/{db_name}/{folder}/manifest.json"
            code, saved = request("/api/v1/settings/storage", storage, session["csrf_token"], "PUT")
            assert code == 200 and saved["settings"]["secret_key"].startswith("••••") and minio_secret not in json.dumps(saved), (code, saved)
            assert saved["settings"]["protection_state"] == "enabled", saved
            code, checked = request("/api/v1/settings/storage/check", {}, session["csrf_token"])
            assert code == 200 and checked["ok"] and [s["name"] for s in checked["steps"]] == ["upload", "list", "download", "cleanup", "protection"], (code, checked)
            def discovery():
                # An empty bucket is a complete answer, so reconciliation settles quickly.
                for _ in range(30):
                    code, body = request("/api/v1/recovery/backups")
                    assert code == 200, (code, body)
                    if body["state"] == "ok":
                        return body
                    time.sleep(1)
                raise AssertionError(body)
            assert discovery()["state"] == "ok", "the bucket was never read completely"
            passed("storage protection required and verified; upload/list/download/cleanup/protection check passes")
            def wait_job(job_id, timeout=120):
                deadline = time.monotonic() + timeout
                while True:
                    code, job = request(f"/api/v1/jobs/{job_id}")
                    assert code == 200, (code, job)
                    if job["state"] not in ("queued", "running"):
                        return job
                    assert time.monotonic() < deadline, job
                    time.sleep(1)
            def wait_idle(timeout=240):
                deadline = time.monotonic() + timeout
                while True:
                    body = request("/api/v1/recovery/backups")[1]
                    if not body.get("busy"):
                        return body
                    assert time.monotonic() < deadline, body
                    time.sleep(1)
            # Configuring storage makes every ready database overdue, so the
            # schedule starts backing them up without anyone asking.
            for _ in range(240):
                histories = {name: request(f"/api/v1/projects/{item['id']}/backups")[1] for name, item in projects.items()}
                if all(history["newest_backup_at"] for history in histories.values()):
                    break
                time.sleep(1)
            assert all(history["newest_backup_at"] for history in histories.values()), histories
            assert all(job["scheduled"] for history in histories.values() for job in history["jobs"] if job["kind"] == "backup"), histories
            passed("scheduled backups start on their own once storage is configured")
            wait_idle()
            before = len(request(f"/api/v1/projects/{shop}/backups")[1]["backups"])
            code, job = request(f"/api/v1/projects/{shop}/backups", {}, session["csrf_token"])
            assert code == 202, (code, job)
            assert request(f"/api/v1/projects/{shop}/backups", {}, session["csrf_token"])[0] == 409, "second heavy job must wait"
            job = wait_job(job["id"])
            assert job["state"] == "succeeded" and job["result"]["sha256"] and job["result"]["tables"] == [{"schema": "public", "name": "guestbook", "rows": 1}], job
            assert job["result"]["objects"], "object baselines were not captured"
            code, history = request(f"/api/v1/projects/{shop}/backups")
            assert code == 200 and len(history["backups"]) == before + 1 and history["next_scheduled_at"] > 0 and history["storage_configured"], history
            assert history["target_interval_hours"] == 24 and history["failures"] == 0, history
            manifest_key = history["backups"][0]["object_key"]
            # The age the dashboard shows comes from the bucket, not from history.
            for _ in range(30):
                history = request(f"/api/v1/projects/{shop}/backups")[1]
                if history["newest_backup_at"]:
                    break
                time.sleep(1)
            assert history["newest_backup_at"] == history["backups"][0]["created_at"], history
            assert (directory / "minio/pgfy-backups/pgfy/test/backups" / projects["Shop"]["db_name"]).exists()
            passed("manual backup dumps within one snapshot, uploads archive then manifest, records history")
            # An upload that cannot finish must never become a published backup after a restart.
            blog_recoverable = request(f"/api/v1/projects/{projects['Blog']['id']}/backups")[1]["newest_backup_at"]
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
            # The half-finished upload must never become a recoverable backup: its
            # archive is in the bucket, but without a manifest nothing offers it.
            blog_before = blog_recoverable
            for _ in range(60):
                blog_history = request(f"/api/v1/projects/{projects['Blog']['id']}/backups")[1]
                if not any(j["state"] in ("queued", "running") for j in blog_history["jobs"]):
                    break
                time.sleep(1)
            assert blog_history["newest_backup_at"] == blog_before, ("an interrupted upload became recoverable", blog_history)
            assert all(j["id"] != stuck["id"] for j in blog_history["jobs"] if j["state"] == "succeeded"), blog_history["jobs"]
            assert "interrupted" in {j["state"] for j in blog_history["jobs"]}, blog_history["jobs"]
            blog_group = next(g for g in discovery()["databases"] if g["db_name"] == projects["Blog"]["db_name"])
            assert all(b["state"] == "complete" for b in blog_group["backups"]), blog_group
            assert run([*helper, "ls -A /fixture/data/work"]).stdout.strip() == "", "workspace not cleaned"
            passed("restart marks the in-flight backup interrupted, its archive never becomes recoverable, workspace clean")
            # Discovery lists every database separately, newest first, and the
            # incomplete upload left behind is hidden rather than offered.
            found = discovery()
            groups = {group["db_name"]: group for group in found["databases"]}
            assert set(groups) == {projects["Shop"]["db_name"], projects["Blog"]["db_name"]}, found
            for group in groups.values():
                assert group["count"] >= 1 and not group["foreign"] and not group["mixed"], group
                assert [b["taken_at"] for b in group["backups"]] == sorted((b["taken_at"] for b in group["backups"]), reverse=True), group
                assert all(b["state"] == "complete" for b in group["backups"]), group
            assert groups[projects["Blog"]["db_name"]]["project_id"] == projects["Blog"]["id"], groups
            assert request("/api/v1/recovery/backups?db=../etc")[0] == 400
            assert request(f"/api/v1/recovery/backups?db={projects['Shop']['db_name']}&limit=500")[0] == 400
            passed("discovery pages each database newest first and hides incomplete work")
            # A backup removed from the bucket stops counting as recoverable, so the
            # dashboard never offers something that is no longer there.
            shop_backups = groups[projects["Shop"]["db_name"]]["backups"]
            mc(f"rm fixture/pgfy-backups/{shop_backups[0]['manifest_key']}")
            request(f"/api/v1/projects/{shop}/backups", {}, session["csrf_token"])  # the next backup reconciles the bucket
            for _ in range(90):
                history = request(f"/api/v1/projects/{shop}/backups")[1]
                if not any(b["object_key"] == shop_backups[0]["manifest_key"] for b in history["backups"]):
                    break
                time.sleep(1)
            assert not any(b["object_key"] == shop_backups[0]["manifest_key"] for b in history["backups"]), history
            assert all(b["manifest_key"] != shop_backups[0]["manifest_key"] for g in discovery()["databases"] for b in g["backups"])
            passed("a backup deleted outside the application stops being offered and stops counting as recoverable")
            # Restore whatever is newest now: earlier checks deliberately removed a backup.
            manifest_key = request(f"/api/v1/projects/{shop}/backups")[1]["backups"][0]["object_key"]
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
            assert job["result"]["verification"] == "verified", job
            for bad in ("pgfy/test/backups/nope/manifest.json", "pgfy/test/backups/app_x/latest/manifest.json", manifest_key.replace("manifest.json", "archive.dump")):
                assert request("/api/v1/recovery/restores", {"manifest_key": bad, "name": "x"}, session["csrf_token"])[0] == 400, bad
            passed("restore into a new project verifies checksum, row counts, objects, ownership; original untouched")
            # A restore that cannot be fully verified says so, and still leaves a
            # usable database rather than hiding it behind a failed job.
            folder = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime(time.time() - 2 * 86400))
            doubtful = clone_backup(projects["Shop"]["db_name"], manifest_key.split("/")[-2], folder, rows=99)
            code, restore = request("/api/v1/recovery/restores", {"manifest_key": doubtful, "name": "Shop doubtful"}, session["csrf_token"])
            assert code == 202, (code, restore)
            job = wait_job(restore["job"]["id"], 180)
            assert job["state"] == "succeeded" and job["result"]["verification"] == "failed" and not job["result"]["verified"], job
            assert any(not c["ok"] for c in job["result"]["checks"]), job["result"]["checks"]
            doubtful_project = request(f"/api/v1/projects/{restore['project']['id']}")[1]["project"]
            assert doubtful_project["stage"] == "ready", doubtful_project
            projects["Doubtful"] = dict(restore["project"], credentials=request(f"/api/v1/projects/{restore['project']['id']}/credentials")[1])
            assert psql(remote_url("Doubtful"), "SELECT entry FROM guestbook;").stdout.strip() == "hello from shop"
            passed("a restore that cannot be verified says so and still leaves a usable database")
            # Retention keeps the last day whole, then one backup per older day.
            # Older backups are made by copying one to an earlier timestamp, with
            # the manifest kept consistent with the folder holding it.
            def shop_objects():
                # Listed recursively: a versioned bucket keeps showing a folder whose
                # objects are all deleted, so only real objects are counted here.
                listing = mc(f"ls --recursive fixture/pgfy-backups/pgfy/test/backups/{projects['Shop']['db_name']}/").stdout
                found = {}
                for line in listing.splitlines():
                    if "/" in line:
                        folder, name = line.split()[-1].split("/")[-2:]
                        found.setdefault(folder, set()).add(name)
                return found
            newest = sorted(shop_objects())[-1]
            aged = []
            for days in (10, 20):
                folder = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime(time.time() - days * 86400))
                clone_backup(projects["Shop"]["db_name"], newest, folder)
                aged.append(folder)
            assert request("/api/v1/settings/backups", {"target_interval_hours": 24, "retention_daily": 1, "retention_weekly": 0}, session["csrf_token"], "PUT")[0] == 200
            code, job = request(f"/api/v1/projects/{shop}/backups", {}, session["csrf_token"])
            assert code == 202, (code, job)
            assert wait_job(job["id"])["state"] == "succeeded"
            for _ in range(60):
                objects = shop_objects()
                if not set(objects) & set(aged):
                    break
                time.sleep(1)
            assert not set(objects) & set(aged), ("expired backups were kept", objects)
            # Nothing is left half-deleted: every backup still offered has both
            # objects, and the aged copies took their archives with them.
            history = request(f"/api/v1/projects/{shop}/backups")[1]
            offered = {b["object_key"].split("/")[-2] for b in history["backups"]}
            assert offered and all(objects[folder] == {"manifest.json", "archive.dump"} for folder in offered), (offered, objects)
            assert offered == {folder for folder, names in objects.items() if "manifest.json" in names}, (offered, objects)
            passed("retention prunes older days to one backup each and leaves no manifest without its archive")
            # Starvation regression: the oldest project is the one whose backups
            # fail, which is exactly the case the previous scheduler never got
            # past. Both projects are made due by emptying their folders, and the
            # restored project's backup makes the application read the bucket again.
            sql(f"ALTER DATABASE {projects['Shop']['db_name']} WITH ALLOW_CONNECTIONS false;")
            for name in ("Shop", "Blog"):
                mc(f"rm --recursive --force fixture/pgfy-backups/pgfy/test/backups/{projects[name]['db_name']}/")
            code, job = request(f"/api/v1/projects/{projects['Restored']['id']}/backups", {}, session["csrf_token"])
            assert code == 202, (code, job)
            assert wait_job(job["id"])["state"] == "succeeded"
            deadline = time.monotonic() + 300
            while time.monotonic() < deadline:
                shop_schedule = request(f"/api/v1/projects/{shop}/backups")[1]
                blog_history = request(f"/api/v1/projects/{projects['Blog']['id']}/backups")[1]
                if shop_schedule["failures"] > 0 and blog_history["newest_backup_at"]:
                    break
                time.sleep(2)
            assert shop_schedule["failures"] > 0, ("the failing project recorded no failure", shop_schedule["failures"], shop_schedule["jobs"][:2])
            assert shop_schedule["next_scheduled_at"] > time.time(), ("no backoff after a failure", shop_schedule["next_scheduled_at"])
            assert blog_history["newest_backup_at"], ("a failing project blocked another project's backup", blog_history["jobs"][:2])
            sql(f"ALTER DATABASE {projects['Shop']['db_name']} WITH ALLOW_CONNECTIONS true;")
            passed("a failing backup backs off and never blocks another project's schedule")
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
            code, status = request("/api/v1/system/status")
            assert code == 200 and status["host"]["state"] == "unknown", status.get("host")
            host.host_status(installation)
            code, status = request("/api/v1/system/status")
            reported = status["host"]
            assert reported["state"] == "ok" and {d["name"] for d in reported["disks"]} == {"postgres", "workspace", "root"}, reported
            assert all(d.get("error") or d["free_percent"] > 0 for d in reported["disks"]), reported
            assert reported["certificate"]["state"] == "not_used", reported
            passed("host status written by the host timer's command is read by the application")
            # --- Alerts to a webhook receiver on the proxy network (reached like MinIO, as a private endpoint) ---
            sink_name = project.replace("_", "-") + "-sink"
            receiver = r"""use IO::Socket::INET; $|=1;
my $s = IO::Socket::INET->new(LocalPort => 8080, Listen => 5, ReuseAddr => 1) or die;
while (my $c = $s->accept) { my ($len, $sig, $ts) = (0, "", "");
  while (my $l = <$c>) { $l =~ s/\r?\n$//; last if $l eq ""; $len = $1 if $l =~ /^Content-Length:\s*(\d+)/i; $sig = $1 if $l =~ /^X-Pgfy-Signature:\s*(\S+)/i; $ts = $1 if $l =~ /^X-Pgfy-Timestamp:\s*(\S+)/i; }
  my $b = ""; read($c, $b, $len) if $len; print "$sig\t$ts\t$b\n";
  print $c "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n"; close $c; }"""
            run(["docker", "run", "-d", "--name", sink_name, "--network", project + "_proxy", "--entrypoint", "perl", images["postgres"], "-e", receiver])
            hook_secret = "integration-signing-secret"
            code, session = request("/api/v1/auth/session")  # earlier phases signed in again
            assert code == 200, session
            code, saved = request("/api/v1/settings/alerts", {"url": f"http://{sink_name}:8080/hooks/T0KEN", "secret": hook_secret, "private_endpoint": True}, session["csrf_token"], "PUT")
            assert code == 200 and saved["configured"] and "T0KEN" not in json.dumps(saved), (code, saved)
            for _ in range(15):  # the receiver may still be starting
                code, tested = request("/api/v1/settings/alerts/test", {}, session["csrf_token"])
                if code == 200 and tested["ok"]:
                    break
                time.sleep(1)
            assert code == 200 and tested["ok"], tested
            def deliveries():
                out = []
                for line in run(["docker", "logs", sink_name]).stdout.splitlines():
                    signature, timestamp, body = line.split("\t", 2)
                    expected = "sha256=" + hmac.new(hook_secret.encode(), (timestamp + "." + body).encode(), hashlib.sha256).hexdigest()
                    assert signature == expected, "a delivery was not signed with the configured secret"
                    out.append(json.loads(body))
                return out
            # The password change earlier in this run was queued as an event and is delivered now that a receiver exists.
            deadline = time.monotonic() + 90
            while not any(d["kind"] == "credential_rotated" for d in deliveries()):
                assert time.monotonic() < deadline, deliveries()
                time.sleep(2)
            assert any(d["kind"] == "test" for d in deliveries())
            assert all(d["installation_id"] == identifier and "T0KEN" not in json.dumps(d) for d in deliveries())
            assert "T0KEN" not in compose("logs", "--no-color", "application").stdout
            passed("alerts reach a signed webhook: a test message and the queued password-change event")
            next_image = os.environ.get("PGFY_TEST_NEXT_IMAGE")
            if next_image:
                update_and_rollback(directory, installation, application_image, next_image, images, request, run, helper, password, passed)
            # --- Second factor through the SSH-issued reset (tunnel mode gets one only this way) ---
            def totp(key, at=None):
                secret = base64.b32decode(key.replace(" ", "") + "=" * (-len(key.replace(" ", "")) % 8))
                counter = int(at if at is not None else time.time()) // 30
                digest = hmac.new(secret, counter.to_bytes(8, "big"), hashlib.sha1).digest()
                offset = digest[-1] & 0x0F
                return "%06d" % ((int.from_bytes(digest[offset:offset + 4], "big") & 0x7FFFFFFF) % 1_000_000)
            def next_step():
                time.sleep(30 - time.time() % 30 + 1)  # a code is accepted once; wait for a fresh one
            reset_token = compose("exec", "-T", "application", "pgfy", "reset-admin").stdout.strip()
            assert len(reset_token) == 43 and reset_token not in compose("logs", "--no-color", "application").stdout
            new_password = "a different integration passphrase"
            code, enrol = request("/api/v1/auth/reset", {"token": reset_token, "password": new_password})
            assert code == 200 and enrol["next"] == "enrol" and len(enrol["key"].replace(" ", "")) == 32, (code, enrol)
            assert request("/api/v1/auth/login", {"email": "admin@example.com", "password": password})[1].get("csrf_token"), "the old password must work until the reset is confirmed"
            earlier_session = [c for c in jar if c.name == "pgfy_tunnel_session"][0]
            code, confirmed = request("/api/v1/auth/enrol/confirm", {"code": totp(enrol["key"])})
            assert code == 200 and confirmed["csrf_token"], (code, confirmed)
            assert request("/api/v1/auth/login", {"email": "admin@example.com", "password": password})[0] == 401, "the old password survived the reset"
            replay = urllib.request.Request(cfg["origin"] + "/api/v1/auth/session", headers={"Cookie": f"pgfy_tunnel_session={earlier_session.value}"})
            try:
                urllib.request.build_opener(urllib.request.ProxyHandler({})).open(replay, timeout=10)
                raise AssertionError("a session from before the reset still works")
            except urllib.error.HTTPError as error:
                assert error.code == 401
            next_step()
            jar.clear()  # start signed out, as a new browser would
            code, step = request("/api/v1/auth/login", {"email": "admin@example.com", "password": new_password})
            assert code == 200 and step == {"next": "code", "server_time": step["server_time"]}, (code, step)
            assert request("/api/v1/projects")[0] == 401, "a session existed before the code"
            assert request("/api/v1/auth/code", {"code": totp(enrol["key"], time.time() - 3600)})[0] == 401, "an hour-old code was accepted"
            code, session = request("/api/v1/auth/code", {"code": totp(enrol["key"])})
            assert code == 200 and request("/api/v1/projects")[0] == 200, (code, session)
            deadline = time.monotonic() + 90
            while not any(d["kind"] == "admin_reset" for d in deliveries()):
                assert time.monotonic() < deadline, "the reset was not alerted"
                time.sleep(2)
            passed("an SSH-issued reset enrols a second factor; the old password stops working; sign-in then needs a code; the reset is alerted")
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
            subprocess.run(["docker", "rm", "-f", project.replace("_", "-") + "-minio", project.replace("_", "-") + "-sink"], capture_output=True, timeout=60)
            run([*helper, f"chown -R {os.getuid()}:{os.getgid()} /fixture; chmod -R u+rwX /fixture"])

if __name__ == "__main__":
    main()
