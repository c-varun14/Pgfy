import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("installer", Path(__file__).parents[1] / "installer.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

TEST_STATE = {"release": "v0.1.0", "volume_prefix": "test", "images": {"application": "a", "postgres": "p", "caddy": "c"}, "database_subnet": "172.20.240.0/24", "proxy_subnet": "172.20.241.0/24", "public_subnet": "172.20.242.0/24"}

class InstallerTests(unittest.TestCase):
    def setUp(self):
        # Host timers are systemd units under /etc; never touch them from tests.
        patcher = patch.object(installer, "converge_host")
        self.converge_host = patcher.start()
        self.addCleanup(patcher.stop)

    def test_interrupted_pull_retry_keeps_installation_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            bundle = base / "bundle"
            bundle.mkdir()
            (bundle / "installer.py").write_text("# fixture bundle\n")
            root = base / "installation"
            release = {"version": "v0.1.0", "images": {key: f"example.invalid/{key}@sha256:" + "a" * 64 for key in ("application", "postgres", "caddy")}}
            args = SimpleNamespace(bundle=str(bundle), dir=str(root), hostname="admin.example.com", tunnel=False)
            with patch.object(installer, "verify_bundle", return_value=release), \
                    patch.object(installer, "preflight", return_value=("29.8.0", "5.5.1")), \
                    patch.object(installer, "select_subnets", return_value=("172.20.240.0/24", "172.20.241.0/24", "172.20.242.0/24")) as subnets, \
                    patch.object(installer.os, "chown"), \
                    patch.object(installer, "run", return_value=SimpleNamespace(returncode=1)), \
                    patch.object(installer.Installation, "compose") as compose:
                with self.assertRaisesRegex(installer.InstallError, "Pinned image pull failed"):
                    installer.install(args)
                before = installer.read_json(root / "state.json")
                config_before = (root / "config/install.json").read_bytes()
                with self.assertRaisesRegex(installer.InstallError, "Pinned image pull failed"):
                    installer.install(args)
                self.assertEqual(installer.read_json(root / "state.json"), before)
                self.assertEqual((root / "config/install.json").read_bytes(), config_before)
                self.assertEqual(before["stage"], "preparing")
                self.assertEqual(list((root / "secrets").iterdir()), [])
                subnets.assert_called_once()
                compose.assert_not_called()

    def test_installer_rejects_release_change_before_preflight(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = {"release": "v0.1.0", "images": {"application": "old"}}
            installer.json_write(root / "state.json", old)
            args = SimpleNamespace(bundle=directory, dir=directory, hostname=None, tunnel=False)
            with patch.object(installer, "verify_bundle", return_value={"version": "v0.2.0", "images": {"application": "new"}}), \
                    patch.object(installer, "preflight") as preflight:
                with self.assertRaisesRegex(installer.InstallError, "Updates require a separate procedure"):
                    installer.install(args)
                preflight.assert_not_called()
                self.assertEqual(installer.read_json(root / "state.json"), old)

    def test_hostname_injection_and_ip_rejected(self):
        for value in ("127.0.0.1", "https://example.com", "example.com:443", "example.com\nadmin :2019", "*.example.com", "EXAMPLE.com", "-bad.example.com"):
            with self.subTest(value=value), self.assertRaises(installer.InstallError):
                installer.valid_hostname(value)
        self.assertEqual(installer.valid_hostname("db.example.com"), "db.example.com")

    def test_no_public_http_proxy_or_admin(self):
        https = installer.caddyfile({"mode": "https", "hostname": "db.example.com"})
        self.assertIn("admin off", https)
        self.assertNotIn("http://db.example.com", https)
        self.assertNotIn("health_uri", https)
        self.assertIn("header_up X-Forwarded-For {remote_host}", https)
        tunnel = installer.caddyfile({"mode": "tunnel"})
        self.assertIn("http://:8080", tunnel)

    def test_hba_rejects_other_sources_and_roles(self):
        hba = installer.pg_hba("172.20.240.0/24")
        self.assertIn("host all all 0.0.0.0/0 reject", hba)
        self.assertIn("host all all ::/0 reject", hba)
        self.assertNotIn("trust", hba)
        self.assertNotIn("md5", hba)
        lines = hba.splitlines()
        include = lines.index("include_if_exists managed/projects.conf")
        # System roles are admitted from the private subnet and rejected everywhere else
        # before the dashboard-managed include, so that file cannot widen their access.
        for role in ("pgfy_bootstrap", "pgfy_health", "pgfy_mgmt"):
            self.assertLess(lines.index(f"host all {role} all reject"), include)
        self.assertLess(include, lines.index("host all all 0.0.0.0/0 reject"))

    def test_compose_env_binds_postgres_by_mode(self):
        state = {"images": {"application": "a", "postgres": "p", "caddy": "c"}, "volume_prefix": "pgfy_x", "database_subnet": "172.20.240.0/24", "proxy_subnet": "172.20.241.0/24", "public_subnet": "172.20.242.0/24"}
        self.assertIn("PG_BIND=0.0.0.0", installer.compose_env(Path("/opt/x"), state, {"mode": "https"}))
        self.assertIn("TUNNEL_SOURCE=172.20.242.1/32", installer.compose_env(Path("/opt/x"), state, {"mode": "https"}))
        self.assertIn("PG_BIND=127.0.0.1", installer.compose_env(Path("/opt/x"), state, {"mode": "tunnel"}))

    def test_version_floors(self):
        self.assertFalse(installer.version_at_least("27.5.1", (28, 0, 0)))
        self.assertTrue(installer.version_at_least("29.8.0", (28, 0, 0)))
        self.assertFalse(installer.version_at_least("v2.29.7", (2, 30, 0)))
        self.assertTrue(installer.version_at_least("v5.5.1", (2, 30, 0)))

    def test_atomic_files_and_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            installer.json_write(path, {"state": "ready"})
            self.assertEqual(installer.read_json(path), {"state": "ready"})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_failed_access_change_rolls_back(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            installer.json_write(root / "state.json", TEST_STATE)
            old = {"mode": "tunnel", "hostname": "", "origin": "http://127.0.0.1:8080", "generation": "old"}
            installation = installer.Installation(root)
            installation.write_access(old)
            original_caddy = (root / "config/caddy/Caddyfile").read_text()
            with patch.object(installer, "network_preflight"), patch.object(installation, "compose"), patch.object(installation, "validate_caddy"), patch.object(installation, "verify", side_effect=installer.InstallError("certificate failed")):
                with self.assertRaises(installer.InstallError):
                    installer.change_access(installation, "https", "new.example.com")
            current = installation.config()
            self.assertEqual(current["origin"], old["origin"])
            self.assertNotEqual(current["generation"], old["generation"])
            self.assertEqual((root / "config/caddy/Caddyfile").read_text(), original_caddy)
            # The certificate timer follows the mode actually in force after the restore.
            self.converge_host.assert_called_with(root, "tunnel")
            self.assertEqual(json.loads((root / "access-rollback.json").read_text())["config"], old)

    def test_successful_host_recovery_does_not_require_database_readiness(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            installer.json_write(root / "state.json", TEST_STATE)
            installation = installer.Installation(root)
            installation.write_access({"mode": "https", "hostname": "broken.example.com", "origin": "https://broken.example.com", "generation": "old"})
            with patch.object(installation, "compose"), patch.object(installation, "validate_caddy"), patch.object(installation, "verify") as verify:
                installer.change_access(installation, "tunnel")
                verify.assert_called_once_with(require_dependencies=False)
            self.assertEqual(installation.config()["mode"], "tunnel")

    def test_explicit_rollback_restores_access_but_not_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            installer.json_write(root / "state.json", TEST_STATE)
            installation = installer.Installation(root)
            original = {"mode": "tunnel", "hostname": "", "origin": "http://127.0.0.1:8080", "generation": "old", "release": "v0.1.0"}
            saved_caddy = "# an older release's template\n" + installer.caddyfile(original)
            installer.json_write(root / "access-rollback.json", {"config": original, "caddyfile": saved_caddy})
            # The access record predates an update to v0.2.0.
            installation.write_access(dict(original, mode="https", hostname="new.example.com", origin="https://new.example.com", release="v0.2.0"))
            with patch.object(installation, "compose"), patch.object(installation, "validate_caddy"), patch.object(installation, "verify"):
                installer.change_access(installation, "", rollback=True)
            restored = installation.config()
            self.assertEqual((root / "config/caddy/Caddyfile").read_text(), installer.caddyfile(restored))
            self.assertEqual((restored["mode"], restored["origin"]), ("tunnel", original["origin"]))
            self.assertEqual(restored["release"], "v0.2.0")
            self.assertNotEqual(restored["generation"], original["generation"])

if __name__ == "__main__":
    unittest.main()

DIGEST = "@sha256:" + "a" * 64

def write_release(bundle, version, postgres="postgres:18.6-bookworm"):
    """A bundle whose checksum manifest covers every required file."""
    import hashlib
    bundle.mkdir(parents=True)
    release = {"version": version, "images": {"application": f"example.invalid/pgfy-{version}{DIGEST}", "postgres": postgres + DIGEST, "caddy": "caddy:2" + DIGEST}}
    files = {"release.json": json.dumps(release), "installer.py": "# installer\n", "install.sh": "#!/bin/sh\n", "pgfyctl": "#!/bin/sh\n", "compose.yaml": "services: {}\n", "compose.https.yaml": "services: {}\n", "compose.tunnel.yaml": "services: {}\n", "postgres/init.sh": "#!/bin/sh\n", "postgres/health.sh": "#!/bin/sh\n"}
    for name, content in files.items():
        (bundle / name).parent.mkdir(parents=True, exist_ok=True)
        (bundle / name).write_text(content)
    (bundle / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(content.encode()).hexdigest()}  {name}\n" for name, content in files.items()))
    return release

class FakeHost:
    """Records docker commands and models the application container's state."""
    def __init__(self, running_jobs=0, app="running"):
        self.commands, self.running_jobs, self.app = [], running_jobs, app
        self.fail_on = None

    def __call__(self, args, **kwargs):
        args = [str(a) for a in args]
        self.commands.append(args)
        out = ""
        if self.fail_on and self.fail_on(args):
            if kwargs.get("check", True):
                raise installer.InstallError("injected failure")
            return SimpleNamespace(returncode=1, stdout="", stderr="")
        if args[-2:] == ["jobs", "running"]:
            out = str(self.running_jobs)
        elif "ps" in args and "-aq" in args:
            out = "container-id"
        elif args[:2] == ["docker", "inspect"]:
            out = self.app
        elif args[-2:] == ["stop", "application"]:
            self.app = "exited"
        elif "up" in args and "-d" in args:
            self.app = "running"
        return SimpleNamespace(returncode=0, stdout=out, stderr="")

    def ran(self, *words):
        return [c for c in self.commands if all(w in c for w in words)]

class UpdateTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        base = Path(self.directory.name)
        self.root = base / "install"
        self.old = write_release(self.root / "releases/v0.1.0", "v0.1.0")
        state = dict(TEST_STATE, release="v0.1.0", images=self.old["images"], stage="installed", id="abc")
        installer.json_write(self.root / "state.json", state)
        cfg = {"id": "abc", "mode": "tunnel", "hostname": "", "origin": "http://127.0.0.1:8080", "generation": "g1", "release": "v0.1.0", "caddy_version": "old"}
        installer.json_write(self.root / "config/install.json", cfg, 0o644)
        installer.atomic(self.root / "config/caddy/Caddyfile", installer.caddyfile(cfg), 0o644)
        installer.atomic(self.root / "config/pg/pg_hba.conf", installer.pg_hba(state["database_subnet"]), 0o644)
        installer.atomic(self.root / "compose.env", installer.compose_env(self.root, state, cfg))
        installer.write_pgfyctl(self.root, self.root / "releases/v0.1.0")
        (self.root / "data/sqlite").mkdir(parents=True)
        self.files = {name: (self.root / name).read_bytes() for name in installer.UPDATE_FILES}
        self.bundle = base / "pgfy-v0.2.0"
        self.new = write_release(self.bundle, "v0.2.0")

    def tearDown(self):
        self.directory.cleanup()

    def update(self, host, verify=None, **kwargs):
        with patch.object(installer, "run", host), patch.object(installer.time, "sleep"), \
                patch.object(installer, "run_converge") as converge, \
                patch.object(installer.Installation, "verify", verify or (lambda self, require_dependencies=True: None)), \
                patch.object(installer.Installation, "validate_caddy"):
            installer.update(self.root, self.bundle, **kwargs)
        return converge

    def test_version_rules(self):
        allowed = [("v1.0.0", "v1.0.1"), ("v1.0.0", "v2.0.0"), ("v1.0.0-rc.1", "v1.0.0-rc.2"), ("v1.0.0-rc.9", "v1.0.0-rc.10"), ("v1.0.0-rc.1", "v1.0.0"), ("v1.0.0-rc.1", "v1.1.0")]
        refused = [("v1.0.0", "v1.0.0"), ("v1.0.1", "v1.0.0"), ("v1.0.0", "v1.0.1-rc.1"), ("v1.0.0-rc.2", "v1.0.0-rc.1"), ("v1.0.0-rc.1", "v1.1.0-rc.1"), ("v1.0.0", "v0.9.9")]
        for old, new in allowed:
            self.assertTrue(installer.update_allowed(old, new), (old, new))
        for old, new in refused:
            self.assertFalse(installer.update_allowed(old, new), (old, new))

    def test_policy_allows_minor_postgres_but_not_major_or_base(self):
        installer.release_policy({"images": {"postgres": "postgres:18.7-bookworm" + DIGEST}})
        for image in ("postgres:19.0-bookworm", "postgres:18.7-trixie", "postgres:18.7"):
            with self.subTest(image=image), self.assertRaises(installer.InstallError):
                installer.release_policy({"images": {"postgres": image + DIGEST}})

    def test_refuses_older_release_before_touching_anything(self):
        self.bundle = Path(self.directory.name) / "old"
        write_release(self.bundle, "v0.0.9")
        host = FakeHost()
        with self.assertRaisesRegex(installer.InstallError, "newer release"):
            self.update(host)
        self.assertEqual(host.commands, [])

    def test_running_job_refuses_and_resumes_backups(self):
        host = FakeHost(running_jobs=1)
        with self.assertRaisesRegex(installer.InstallError, "--drain"):
            self.update(host)
        self.assertTrue(host.ran("maintenance", "on"))
        self.assertTrue(host.ran("maintenance", "off"))
        self.assertFalse(host.ran("stop", "application"))
        self.assertFalse((self.root / "update-rollback").exists())
        self.assertEqual({n: (self.root / n).read_bytes() for n in installer.UPDATE_FILES}, self.files)

    def test_pull_failure_changes_nothing(self):
        host = FakeHost()
        host.fail_on = lambda args: args[:2] == ["docker", "pull"]
        with self.assertRaisesRegex(installer.InstallError, "pull failed"):
            self.update(host)
        self.assertFalse(host.ran("maintenance"))
        self.assertEqual({n: (self.root / n).read_bytes() for n in installer.UPDATE_FILES}, self.files)

    def test_failure_after_apply_restores_the_previous_release(self):
        host = FakeHost()
        calls = []
        def verify(installation, require_dependencies=True):
            calls.append(installation.state["release"])
            if len(calls) == 1:
                raise OSError("disk full")  # not an InstallError: must still roll back
        with self.assertRaisesRegex(installer.InstallError, "Update to v0.2.0 failed.*Restored v0.1.0"):
            self.update(host, verify=verify)
        self.assertEqual(calls, ["v0.2.0", "v0.1.0"])
        for name in ("state.json", "compose.env", "pgfyctl", "config/caddy/Caddyfile", "config/pg/pg_hba.conf"):
            self.assertEqual((self.root / name).read_bytes(), self.files[name], name)
        restored = installer.read_json(self.root / "config/install.json")
        self.assertEqual(restored["release"], "v0.1.0")
        self.assertNotEqual(restored["generation"], "g1")
        old_image = self.old["images"]["application"]
        check, restore = host.ran(old_image, "--check"), host.ran(old_image, "store-restore")
        self.assertTrue(check and len(restore) == 2)
        self.assertLess(host.commands.index(check[0]), host.commands.index([c for c in restore if "--check" not in c][0]))
        self.assertTrue(host.ran("maintenance", "off"))
        self.assertFalse((self.root / "update-rollback").exists())

    def test_damaged_snapshot_leaves_everything_for_the_operator(self):
        host = FakeHost()
        host.fail_on = lambda args: "--check" in args
        def verify(installation, require_dependencies=True):
            raise installer.InstallError("not ready")
        with self.assertRaisesRegex(installer.InstallError, "snapshot is missing or damaged"):
            self.update(host, verify=verify)
        self.assertTrue((self.root / "update-rollback/meta.json").exists())
        self.assertFalse(host.ran("store-restore", "/data/" + installer.SNAPSHOT_DB)[1:])

    def test_success_commits_and_cleans_up(self):
        host = FakeHost()
        installer.json_write(self.root / "access-rollback.json", {"config": {}})
        converge = self.update(host)
        converge.assert_called_once()
        state = installer.read_json(self.root / "state.json")
        self.assertEqual((state["release"], state["images"]), ("v0.2.0", self.new["images"]))
        self.assertIn("releases/v0.2.0/installer.py", (self.root / "pgfyctl").read_text())
        self.assertIn(self.new["images"]["application"], (self.root / "compose.env").read_text())
        self.assertFalse((self.root / "update-rollback").exists())
        self.assertFalse((self.root / "access-rollback.json").exists())
        snapshot = host.ran("store-snapshot")
        self.assertIn(self.old["images"]["application"], snapshot[0])
        self.assertTrue(host.ran("maintenance", "off"))

    def test_stopped_application_is_quiesced_offline(self):
        host = FakeHost(app="restarting")
        self.update(host)
        on = host.ran("maintenance", "on")[0]
        self.assertIn("run", on)
        self.assertLess(host.commands.index(host.ran("stop", "application")[0]), host.commands.index(on))

    def test_rollback_without_complete_snapshot_only_restarts(self):
        (self.root / "update-rollback").mkdir()
        host = FakeHost(app="exited")
        with patch.object(installer, "run", host), patch.object(installer.time, "sleep"):
            installer.rollback_update(self.root)
        self.assertTrue(host.ran("up", "application"))
        self.assertFalse(host.ran("store-restore"))
        self.assertFalse((self.root / "update-rollback").exists())

    def test_failure_before_the_snapshot_is_recorded_changes_nothing(self):
        host = FakeHost()
        host.fail_on = lambda args: "store-snapshot" in args
        with self.assertRaisesRegex(installer.InstallError, "injected failure"):
            self.update(host)
        self.assertFalse((self.root / "update-rollback").exists(), "a half-made snapshot would block the next update")
        self.assertTrue(host.ran("--entrypoint", "rm"))
        self.assertTrue(host.ran("maintenance", "off"))
        self.assertFalse(host.ran("store-restore"))
        self.assertEqual({n: (self.root / n).read_bytes() for n in installer.UPDATE_FILES}, self.files)

    def test_any_exception_after_the_snapshot_rolls_back(self):
        host = FakeHost()
        with patch.object(installer, "compose_env", side_effect=[installer.compose_env(self.root, installer.read_json(self.root / "state.json"), installer.read_json(self.root / "config/install.json")), KeyError("images")]):
            with self.assertRaisesRegex(installer.InstallError, "failed while switching to the new release.*Restored v0.1.0"):
                self.update(host)
        self.assertEqual((self.root / "state.json").read_bytes(), self.files["state.json"])
        self.assertTrue(host.ran("store-restore"))

    def test_a_failure_after_the_commit_point_keeps_the_new_release(self):
        host = FakeHost()
        real = installer.fsync_dir
        calls = []
        def fsync(path):
            calls.append(path)
            if len(calls) == 2:
                raise OSError("fsync failed")
            real(path)
        with patch.object(installer, "fsync_dir", fsync):
            self.update(host)
        self.assertEqual(installer.read_json(self.root / "state.json")["release"], "v0.2.0")
        self.assertFalse(host.ran("store-restore"))

    def test_a_held_lock_is_accepted_through_an_inherited_descriptor(self):
        import fcntl, subprocess, sys
        lock = Path(self.directory.name) / "pgfy.lock"
        with patch.object(installer, "lock_path", lambda root: str(lock)):
            with open(lock, "a") as held:
                fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
                self.assertTrue(installer.holds_lock(self.root, held.fileno()))
                with open(lock, "a") as other:
                    self.assertFalse(installer.holds_lock(self.root, other.fileno()))
                probe = f"import fcntl,os,sys; fd=int(sys.argv[1]); fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB); s=os.fstat(fd); t=os.stat({str(lock)!r}); sys.exit(0 if (s.st_dev,s.st_ino)==(t.st_dev,t.st_ino) else 1)"
                child = subprocess.run([sys.executable, "-c", probe, str(held.fileno())], pass_fds=(held.fileno(),))
                self.assertEqual(child.returncode, 0)

    def test_converge_requires_the_held_lock(self):
        with self.assertRaisesRegex(installer.InstallError, "contract"):
            installer.converge(self.root, "2", None)
        with self.assertRaisesRegex(installer.InstallError, "holds the installation lock"):
            installer.converge(self.root, installer.CONVERGE_CONTRACT, None)
        with tempfile.TemporaryFile() as other, self.assertRaisesRegex(installer.InstallError, "holds the installation lock"):
            installer.converge(self.root, installer.CONVERGE_CONTRACT, other.fileno())

class ConvergeTests(unittest.TestCase):
    def test_update_converge_grants_without_recreating_postgres(self):
        installation = SimpleNamespace(root=Path("/nonexistent"), state=TEST_STATE)
        calls = []
        installation.compose = lambda *args, **kwargs: calls.append((args, kwargs)) or SimpleNamespace(returncode=0, stdout="")
        installation.wait_postgres = lambda: calls.append((("wait",), {}))
        installation.config = lambda: {"mode": "https"}
        with patch.object(installer, "converge_steps") as steps, patch.object(installer, "converge_host") as host:
            installer.converge_installation(installation)
        steps.assert_called_once()
        host.assert_called_once_with(installation.root, "https")
        up = [args for args, _ in calls if "up" in args]
        self.assertEqual(up, [("up", "-d", "--no-recreate", "postgres")])
        sql = [kwargs.get("input", "") for args, kwargs in calls if "exec" in args]
        self.assertEqual(len(sql), 1)
        self.assertIn("GRANT pg_use_reserved_connections TO pgfy_mgmt, pgfy_health", sql[0])
        self.assertIn("GRANT SET ON PARAMETER temp_file_limit TO pgfy_mgmt", sql[0])
        self.assertIn("pgfy_bootstrap", " ".join(a for args, _ in calls if "exec" in args for a in args))
        self.assertLess([a[0] for a, _ in calls].index("wait"), [i for i, (a, _) in enumerate(calls) if "exec" in a][0])

    def test_fresh_clusters_get_the_same_grants(self):
        init = (Path(__file__).parents[1] / "postgres/init.sh").read_text()
        for line in installer.POSTGRES_CONVERGE_SQL.strip().splitlines():
            self.assertIn(line, init)


class HostStatusTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.root = Path(self.directory.name)
        installer.json_write(self.root / "state.json", TEST_STATE)
        (self.root / "data/work").mkdir(parents=True)
        (self.root / "config").mkdir()

    def tearDown(self):
        self.directory.cleanup()

    def host(self, volume, ntp):
        def fake(args, **kwargs):
            if args[:3] == ["docker", "volume", "inspect"]:
                return SimpleNamespace(returncode=0 if volume else 1, stdout=volume or "")
            if args[0] == "timedatectl":
                return ntp
            raise AssertionError(args)
        return fake

    def test_status_is_readable_by_the_app_and_never_invents_numbers(self):
        with patch.object(installer, "run", self.host(str(self.root), SimpleNamespace(returncode=0, stdout="yes\n"))):
            status = installer.host_status(installer.Installation(self.root))
        path = self.root / "config/host-status.json"
        self.assertEqual(path.stat().st_mode & 0o777, 0o644)
        disks = {d["name"]: d for d in status["disks"]}
        self.assertEqual(set(disks), {"postgres", "workspace", "root"})
        self.assertGreater(disks["workspace"]["total_bytes"], 0)
        self.assertIn("device", disks["postgres"])
        self.assertIs(status["ntp"]["synchronized"], True)
        with patch.object(installer, "run", self.host(None, SimpleNamespace(returncode=1, stdout=""))):
            status = installer.host_status(installer.Installation(self.root))
        disks = {d["name"]: d for d in status["disks"]}
        self.assertEqual(disks["postgres"], {"name": "postgres", "error": "could not be measured"})
        self.assertIsNone(status["ntp"]["synchronized"])
        def missing(args, **kwargs):
            raise installer.InstallError(f"{args[0]} failed or timed out")
        with patch.object(installer, "run", missing):
            status = installer.host_status(installer.Installation(self.root))
        self.assertIsNone(status["ntp"]["synchronized"])

    def test_host_status_runs_while_the_lock_is_held(self):
        import fcntl
        lock = self.root / "held.lock"
        argv = ["pgfyctl", "--dir", str(self.root), "host-status"]
        with patch.object(installer, "lock_path", lambda root: str(lock)), open(lock, "a") as held:
            fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with patch.object(installer.sys, "argv", argv), patch.object(installer.os, "geteuid", return_value=0), \
                    patch.object(installer, "run", self.host(str(self.root), SimpleNamespace(returncode=0, stdout="no"))):
                installer.main()
        self.assertFalse(installer.read_json(self.root / "config/host-status.json")["ntp"]["synchronized"])

    def test_certificate_sync_outcomes_are_recorded_except_in_tunnel_mode(self):
        installation = installer.Installation(self.root)
        installer.json_write(self.root / "config/install.json", {"mode": "https", "hostname": "db.example.com"}, 0o644)
        with patch.object(installer, "deliver_db_cert", side_effect=installer.InstallError("Caddy has not obtained a certificate")):
            installer.sync_db_cert(installation, fatal=False)
        record = installer.read_json(self.root / "config/cert-sync.json")
        self.assertEqual((record["ok"], record["message"]), (False, "Caddy has not obtained a certificate"))
        self.assertEqual((self.root / "config/cert-sync.json").stat().st_mode & 0o777, 0o644)
        with patch.object(installer, "deliver_db_cert"):
            installer.sync_db_cert(installation)
        self.assertTrue(installer.read_json(self.root / "config/cert-sync.json")["ok"])
        (self.root / "config/cert-sync.json").unlink()
        installer.json_write(self.root / "config/install.json", {"mode": "tunnel", "hostname": ""}, 0o644)
        with self.assertRaises(installer.InstallError):
            installer.sync_db_cert(installation)
        self.assertFalse((self.root / "config/cert-sync.json").exists())

    def test_unattended_upgrades_respect_an_explicit_choice(self):
        outputs = {"UU='1'\n": "enabled", "UU='0'\n": "disabled by operator"}
        for printed, expected in outputs.items():
            with patch.object(installer, "run", return_value=SimpleNamespace(returncode=0, stdout=printed)), patch.object(installer, "atomic") as write, patch.object(installer.subprocess, "run") as apt:
                self.assertEqual(installer.unattended_upgrades(), expected)
                write.assert_not_called()
                apt.assert_not_called()

    def test_unset_unattended_upgrades_are_installed_and_enabled_once(self):
        def fake(printed, installed):
            def run(args, **kwargs):
                if args[0] == "apt-config":
                    return SimpleNamespace(returncode=0, stdout=printed)
                return SimpleNamespace(returncode=0, stdout="install ok installed" if installed else "")
            return run
        exists = Path.exists
        for installed, config_present in ((False, False), (True, True)):
            with patch.object(installer, "run", fake("", installed)), patch.object(installer, "atomic") as write, \
                    patch.object(installer.subprocess, "run") as apt, \
                    patch.object(installer.Path, "exists", lambda self: config_present if str(self).endswith("20auto-upgrades") else exists(self)):
                self.assertEqual(installer.unattended_upgrades(), "enabled")
            self.assertEqual([c.args[0][-1] for c in apt.call_args_list], [] if installed else ["update", "unattended-upgrades"])
            self.assertEqual(write.called, not config_present)
        for odd in ("UU=''\n", "UU='unbalanced\n"):
            with patch.object(installer, "run", fake(odd, True)), patch.object(installer, "atomic"), patch.object(installer.subprocess, "run"):
                self.assertIn(installer.unattended_upgrades(), ("enabled", "unknown"))

    def test_timers_follow_the_access_mode(self):
        written = {}
        with patch.object(installer, "atomic", lambda path, data, mode=0o600, uid=None, gid=None: written.__setitem__(path, data)), patch.object(installer, "run") as run:
            installer.converge_host(self.root, "tunnel")
        self.assertIn(f"ExecStart={self.root}/pgfyctl host-status", written["/etc/systemd/system/pgfy-host-status.service"])
        commands = [c.args[0] for c in run.call_args_list]
        self.assertIn(["systemctl", "enable", "--now", "pgfy-host-status.timer"], commands)
        self.assertIn(["systemctl", "disable", "--now", "pgfy-cert.timer"], commands)
        with patch.object(installer, "atomic"), patch.object(installer, "run") as run:
            installer.converge_host(self.root, "https")
        self.assertIn(["systemctl", "enable", "--now", "pgfy-cert.timer"], [c.args[0] for c in run.call_args_list])
