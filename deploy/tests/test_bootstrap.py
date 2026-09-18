import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
source = (ROOT / "deploy/bootstrap.sh").read_text().split("<<'PGFY_PYTHON'\n", 1)[1].rsplit("\nPGFY_PYTHON", 1)[0]
bootstrap = types.ModuleType("pgfy_bootstrap")
exec(compile(source, "deploy/bootstrap.sh", "exec"), bootstrap.__dict__)


class BootstrapTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.package_directory = tempfile.TemporaryDirectory(prefix="pgfy-package-test-")
        cls.addClassCleanup(cls.package_directory.cleanup)
        cls.output = Path(cls.package_directory.name)
        subprocess.run([
            "python3", str(ROOT / "scripts/bundle.py"), "--version", "v0.1.0",
            "--image", "example.invalid/pgfy@sha256:" + "a" * 64,
            "--output-dir", str(cls.output),
        ], check=True, capture_output=True)
        cls.original = (cls.output / "pgfy-v0.1.0.tar.gz").read_bytes()
        cls.files = {}
        with tarfile.open(fileobj=io.BytesIO(cls.original), mode="r:gz") as archive:
            for member in archive:
                if member.isfile():
                    cls.files[member.name.split("/", 1)[1]] = archive.extractfile(member).read()
        cls.release = json.loads(cls.files["release.json"])

    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="pgfy-bootstrap-test-")
        self.addCleanup(directory.cleanup)
        self.temporary = Path(directory.name)
        self.args = argparse.Namespace(version=None, dir=str(self.temporary / "install"), hostname="admin.example.com", tunnel=False)
        self.archive = self.original
        self.requests = []
        self.latest = {"tag_name": "v0.1.0", "draft": False, "prerelease": False}

    def fetch(self, url, destination, limit):
        self.requests.append(url)
        if url == bootstrap.LATEST:
            destination.write_text(json.dumps(self.latest))
        elif url.endswith(".sha256"):
            destination.write_text(hashlib.sha256(self.archive).hexdigest() + "  pgfy-v0.1.0.tar.gz\n")
        elif url.endswith("pgfy-v0.1.0.tar.gz"):
            destination.write_bytes(self.archive)
        else:
            raise AssertionError(f"Unexpected download: {url}")

    def set_state(self, **changes):
        root = Path(self.args.dir)
        root.mkdir()
        state = {"release": "v0.1.0", "images": self.release["images"]}
        state.update(changes)
        (root / "state.json").write_text(json.dumps(state))

    def archive_with(self, changes=None, extra=None):
        files = dict(self.files)
        files.update(changes or {})
        files["SHA256SUMS"] = "".join(
            hashlib.sha256(data).hexdigest() + "  " + name + "\n"
            for name, data in sorted(files.items()) if name != "SHA256SUMS"
        ).encode()
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            for name, data in files.items():
                member = tarfile.TarInfo("pgfy-v0.1.0/" + name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
            if extra:
                archive.addfile(extra, io.BytesIO(b"x" * extra.size) if extra.isfile() else None)
        self.archive = buffer.getvalue()

    def invoke(self, code=0):
        with patch.object(bootstrap, "fetch", side_effect=self.fetch), patch.object(bootstrap.subprocess, "run") as run:
            run.return_value.returncode = code
            result = bootstrap.bootstrap(self.args)
            return result, run.call_args.args[0]

    def assert_rejected(self, message=None):
        with patch.object(bootstrap, "fetch", side_effect=self.fetch), patch.object(bootstrap.subprocess, "run") as run:
            with self.assertRaises((bootstrap.BootstrapError, OSError, tarfile.TarError)) as raised:
                bootstrap.bootstrap(self.args)
            if message:
                self.assertIn(message, str(raised.exception))
            run.assert_not_called()

    def test_real_packaged_bundle_latest_and_cleanup(self):
        result, command = self.invoke()
        self.assertEqual(result, 0)
        self.assertIn(bootstrap.LATEST, self.requests)
        self.assertEqual(command[2:], ["--dir", self.args.dir, "--hostname", "admin.example.com"])
        self.assertFalse(Path(command[1]).exists(), "download workspace must be cleaned")
        self.assertFalse(Path(self.args.dir).exists(), "bootstrap must leave installation mutations to installer")

    def test_explicit_version_skips_latest_and_supports_tunnel(self):
        self.args.version = "v0.1.0"
        self.args.hostname = None
        self.args.tunnel = True
        _, command = self.invoke()
        self.assertNotIn(bootstrap.LATEST, self.requests)
        self.assertEqual(command[2:], ["--dir", self.args.dir, "--tunnel"])

    def test_installed_version_wins_over_new_latest(self):
        self.set_state()
        self.args.hostname = None
        self.latest["tag_name"] = "v0.2.0"
        self.invoke()
        self.assertNotIn(bootstrap.LATEST, self.requests)

    def test_explicit_update_is_rejected_before_network(self):
        self.set_state()
        self.args.version = "v0.2.0"
        self.assert_rejected("retain its release")
        self.assertEqual(self.requests, [])

    def test_changed_digest_is_rejected(self):
        self.set_state(images=dict(self.release["images"], application="example.invalid/new@sha256:" + "b" * 64))
        self.assert_rejected("digests differ")

    def test_malformed_state_is_not_a_fresh_install(self):
        self.set_state()
        (Path(self.args.dir) / "state.json").write_text("broken")
        self.assert_rejected("valid JSON")
        self.assertEqual(self.requests, [])

    def test_incomplete_state_is_rejected(self):
        self.set_state(images={})
        self.assert_rejected("invalid image digests")

    def test_nonempty_directory_without_state_is_preserved(self):
        root = Path(self.args.dir)
        root.mkdir()
        sentinel = root / "keep"
        sentinel.write_text("existing data")
        self.assert_rejected("nonempty")
        self.assertEqual(sentinel.read_text(), "existing data")

    def test_no_mode_for_fresh_install_is_rejected(self):
        self.args.hostname = None
        self.assert_rejected("--hostname")

    def test_invalid_paths_and_version_are_rejected(self):
        for value in ("v1", "v1.2.3/../../x", "v1.2.3;touch x"):
            with self.subTest(version=value):
                self.args.version = value
                self.assert_rejected("Invalid release version")
        self.args.version = None
        self.args.dir = "relative"
        self.assert_rejected("absolute")

    def test_latest_must_be_published_and_stable(self):
        for change in ({"draft": True}, {"prerelease": True}, {"tag_name": "v0.1.0-rc.1"}):
            with self.subTest(change=change):
                self.latest = dict(tag_name="v0.1.0", draft=False, prerelease=False)
                self.latest.update(change)
                self.assert_rejected("stable published release")

    def test_download_failure_never_runs_installer(self):
        with patch.object(bootstrap, "fetch", side_effect=bootstrap.BootstrapError("download failed")), patch.object(bootstrap.subprocess, "run") as run:
            with self.assertRaises(bootstrap.BootstrapError):
                bootstrap.bootstrap(self.args)
            run.assert_not_called()

    def test_corrupt_archive_never_runs_installer(self):
        normal_fetch = self.fetch
        def fetch(url, destination, limit):
            normal_fetch(url, destination, limit)
            if url.endswith(".tar.gz"):
                destination.write_bytes(b"corrupt")
        with patch.object(bootstrap, "fetch", side_effect=fetch), patch.object(bootstrap.subprocess, "run") as run:
            with self.assertRaisesRegex(bootstrap.BootstrapError, "checksum mismatch"):
                bootstrap.bootstrap(self.args)
            run.assert_not_called()

    def test_unsafe_archive_entries_never_run_installer(self):
        for name, kind in (
            ("../escape", tarfile.REGTYPE), ("/tmp/escape", tarfile.REGTYPE),
            ("pgfy-v0.1.0/../../escape", tarfile.REGTYPE),
            ("pgfy-v0.1.0/link", tarfile.SYMTYPE), ("pgfy-v0.1.0/hard", tarfile.LNKTYPE),
            ("pgfy-v0.1.0/device", tarfile.CHRTYPE), ("pgfy-v0.1.0/install.sh", tarfile.REGTYPE),
        ):
            with self.subTest(name=name):
                extra = tarfile.TarInfo(name)
                extra.type = kind
                extra.linkname = "/tmp/escape" if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else ""
                self.archive_with(extra=extra)
                self.assert_rejected("unsafe or duplicate")

    def test_internal_checksum_failure_never_runs_installer(self):
        files = dict(self.files)
        files["install.sh"] += b"\n# modified after checksumming\n"
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            for name, data in files.items():
                member = tarfile.TarInfo("pgfy-v0.1.0/" + name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        self.archive = buffer.getvalue()
        self.assert_rejected("corrupt file")

    def test_release_identity_is_checked(self):
        release = dict(self.release, version="v0.2.0")
        self.archive_with({"release.json": json.dumps(release).encode()})
        self.assert_rejected("version does not match")

    def test_real_fixture_installer_exit_and_arguments(self):
        self.archive_with({"install.sh": b'#!/usr/bin/env bash\nprintf "%s\\n" "$@" > "$PGFY_TEST_ARGS"\nexit 17\n'})
        output = self.temporary / "arguments"
        with patch.object(bootstrap, "fetch", side_effect=self.fetch), patch.dict(os.environ, {"PGFY_TEST_ARGS": str(output)}):
            result = bootstrap.bootstrap(self.args)
        self.assertEqual(result, 17)
        self.assertEqual(output.read_text().splitlines(), ["--dir", self.args.dir, "--hostname", "admin.example.com"])

    def test_cleanup_on_installer_failure(self):
        result, command = self.invoke(code=23)
        self.assertEqual(result, 23)
        self.assertFalse(Path(command[1]).exists())

    def test_shell_help_and_conflicting_arguments(self):
        result = subprocess.run(["bash", str(ROOT / "deploy/bootstrap.sh"), "--help"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn("--version", result.stdout)
        result = subprocess.run(["bash", str(ROOT / "deploy/bootstrap.sh"), "--hostname", "admin.example.com", "--tunnel"], capture_output=True)
        self.assertEqual(result.returncode, 2)

    def test_standalone_bootstrap_matches_packaged_copy(self):
        script = self.output / "bootstrap.sh"
        self.assertEqual(script.read_bytes(), self.files["bootstrap.sh"])
        bootstrap.verify_archive(script, self.output / "bootstrap.sh.sha256")

    def test_fetch_is_https_only_bounded_and_does_not_use_curl_config(self):
        target = self.temporary / "download"
        target.write_bytes(b"test")
        with patch.object(bootstrap.subprocess, "run") as run:
            run.return_value.returncode = 0
            bootstrap.fetch("https://github.com/example", target, 1024)
            command = run.call_args.args[0]
            self.assertEqual(command[:2], ["curl", "--disable"])
            self.assertIn("--proto-redir", command)
            self.assertIn("--max-time", command)
            self.assertEqual(run.call_args.kwargs["timeout"], 130)


if __name__ == "__main__":
    unittest.main()
