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

class InstallerTests(unittest.TestCase):
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
                    patch.object(installer, "select_subnets", return_value=("172.20.240.0/24", "172.20.241.0/24")) as subnets, \
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
            installer.json_write(root / "state.json", {"release": "v0.1.0", "volume_prefix": "test"})
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
            self.assertEqual(json.loads((root / "access-rollback.json").read_text())["config"], old)

    def test_successful_host_recovery_does_not_require_database_readiness(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            installer.json_write(root / "state.json", {"release": "v0.1.0", "volume_prefix": "test"})
            installation = installer.Installation(root)
            installation.write_access({"mode": "https", "hostname": "broken.example.com", "origin": "https://broken.example.com", "generation": "old"})
            with patch.object(installation, "compose"), patch.object(installation, "validate_caddy"), patch.object(installation, "verify") as verify:
                installer.change_access(installation, "tunnel")
                verify.assert_called_once_with(require_dependencies=False)
            self.assertEqual(installation.config()["mode"], "tunnel")

    def test_explicit_rollback_restores_saved_caddy_configuration(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            installer.json_write(root / "state.json", {"release": "v0.1.0", "volume_prefix": "test"})
            installation = installer.Installation(root)
            original = {"mode": "tunnel", "hostname": "", "origin": "http://127.0.0.1:8080", "generation": "old"}
            saved_caddy = "# preserved host configuration\n" + installer.caddyfile(original)
            installer.json_write(root / "access-rollback.json", {"config": original, "caddyfile": saved_caddy})
            installation.write_access(dict(original, mode="https", hostname="new.example.com", origin="https://new.example.com"))
            with patch.object(installation, "compose"), patch.object(installation, "validate_caddy"), patch.object(installation, "verify"):
                installer.change_access(installation, "", rollback=True)
            self.assertEqual((root / "config/caddy/Caddyfile").read_text(), saved_caddy)
            self.assertEqual(installation.config()["origin"], original["origin"])
            self.assertNotEqual(installation.config()["generation"], original["generation"])

if __name__ == "__main__":
    unittest.main()
