#!/usr/bin/env bash
# Standalone release bootstrap. Installation remains in the verified bundle.
set -Eeuo pipefail
command -v python3 >/dev/null || { echo 'Pgfy requires Python 3 (included in Ubuntu 24.04).' >&2; exit 1; }
exec python3 - "$@" <<'PGFY_PYTHON'
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

REPOSITORY = "https://github.com/c-varun14/Pgfy"
LATEST = "https://api.github.com/repos/c-varun14/Pgfy/releases/latest"
VERSION = re.compile(r"v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?\Z")
DIGEST = re.compile(r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}\Z")
REQUIRED = {
    "release.json", "install.sh", "installer.py", "pgfyctl", "compose.yaml",
    "compose.https.yaml", "compose.tunnel.yaml", "postgres/init.sh", "postgres/health.sh",
}


class BootstrapError(Exception):
    pass


def version(value):
    if not isinstance(value, str) or not VERSION.fullmatch(value):
        raise BootstrapError("Invalid release version; use vMAJOR.MINOR.PATCH (optionally with a prerelease suffix).")
    return value


def read_json(path):
    try:
        value = json.loads(path.read_text())
    except (OSError, ValueError) as error:
        raise BootstrapError(f"Cannot read valid JSON from {path.name}; preserve existing installation state.") from error
    if not isinstance(value, dict):
        raise BootstrapError(f"Expected a JSON object in {path.name}.")
    return value


def fetch(url, destination, limit):
    # No token, cloud credentials, alternate mirrors, or shell interpolation.
    result = subprocess.run([
        "curl", "--disable", "--fail", "--silent", "--show-error", "--location",
        "--proto", "=https", "--proto-redir", "=https",
        "--connect-timeout", "15", "--max-time", "120", "--max-filesize", str(limit),
        "--user-agent", "pgfy-bootstrap", "--output", str(destination), url,
    ], capture_output=True, timeout=130)
    if result.returncode:
        raise BootstrapError("Release download failed. Check network access, release availability, and GitHub API rate limits; rerun to retry.")
    if not destination.is_file() or destination.stat().st_size > limit:
        raise BootstrapError("Release download is missing or exceeds its size limit.")


def installed_state(root):
    state_path = root / "state.json"
    if state_path.is_symlink():
        raise BootstrapError("Installation state must not be a symlink.")
    if not state_path.exists():
        if root.exists() and (not root.is_dir() or any(root.iterdir())):
            raise BootstrapError("Installation directory is nonempty without state; inspect it manually. Nothing was overwritten.")
        return None
    state = read_json(state_path)
    version(state.get("release"))
    validate_images(state.get("images"))
    return state


def validate_images(images):
    if not isinstance(images, dict) or any(
        not isinstance(images.get(key), str) or not DIGEST.fullmatch(images[key])
        for key in ("application", "postgres", "caddy")
    ):
        raise BootstrapError("Release or installation state contains invalid image digests.")


def select_version(requested, state, temporary):
    if requested:
        version(requested)
    if state:
        if requested and requested != state["release"]:
            raise BootstrapError("An existing installation must retain its release. Updates require a separate procedure.")
        return state["release"]
    if requested:
        return requested
    metadata = temporary / "latest.json"
    fetch(LATEST, metadata, 1024 * 1024)
    release = read_json(metadata)
    selected = version(release.get("tag_name"))
    if release.get("draft") is not False or release.get("prerelease") is not False or "-" in selected:
        raise BootstrapError("GitHub did not return a stable published release; supply --version explicitly.")
    return selected


def verify_archive(archive, checksum):
    match = re.fullmatch(r"([a-fA-F0-9]{64})  " + re.escape(archive.name) + r"\n?", checksum.read_text())
    if not match:
        raise BootstrapError("Invalid release checksum file.")
    digest = hashlib.sha256()
    with archive.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    if digest.hexdigest() != match[1].lower():
        raise BootstrapError("Release checksum mismatch; no installer was executed. Download again from the release channel.")


def extract_bundle(archive, temporary, selected):
    expected_root = "pgfy-" + selected
    destination = temporary / "extracted"
    destination.mkdir(mode=0o700)
    # Extract regular files/directories ourselves: never restore archive links,
    # ownership, special files, or permissions, even from a checksummed archive.
    with tarfile.open(archive, "r:gz") as source:
        members = []
        seen = set()
        total = 0
        for member in source:
            name = PurePosixPath(member.name)
            if (name.is_absolute() or not name.parts or name.parts[0] != expected_root
                    or ".." in name.parts or "\\" in member.name
                    or not (member.isdir() or member.isfile()) or name in seen):
                raise BootstrapError("Release archive contains an unsafe or duplicate entry.")
            seen.add(name)
            total += member.size
            if total > 128 * 1024 * 1024 or len(seen) > 4096:
                raise BootstrapError("Expanded release archive exceeds its size limit.")
            members.append((member, name))
        for member, name in members:
            target = destination.joinpath(*name.parts)
            if member.isdir():
                target.mkdir(mode=0o700, parents=True, exist_ok=True)
            else:
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with source.extractfile(member) as incoming, target.open("xb") as output:
                    shutil.copyfileobj(incoming, output)
                target.chmod(0o600)
    return destination / expected_root


def verify_bundle(bundle, selected, state):
    manifest = bundle / "SHA256SUMS"
    if not manifest.is_file():
        raise BootstrapError("Release bundle has no internal checksums.")
    verified = set()
    for line in manifest.read_text().splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  (.+)", line)
        if not match:
            raise BootstrapError("Invalid internal checksum manifest.")
        expected, name = match.groups()
        path = bundle / name
        if (name in verified or not path.resolve().is_relative_to(bundle.resolve())
                or path.is_symlink() or not path.is_file()
                or hashlib.sha256(path.read_bytes()).hexdigest() != expected):
            raise BootstrapError("Release bundle contains an unsafe, missing, or corrupt file.")
        verified.add(name)
    if not REQUIRED.issubset(verified):
        raise BootstrapError("Release bundle is incomplete.")
    release = read_json(bundle / "release.json")
    if release.get("version") != selected:
        raise BootstrapError("Release bundle version does not match the selected release.")
    validate_images(release.get("images"))
    if state and release["images"] != state["images"]:
        raise BootstrapError("Release image digests differ from the existing installation; nothing was changed.")


def bootstrap(args):
    if not Path(args.dir).is_absolute():
        raise BootstrapError("Installation directory must be absolute.")
    root = Path(args.dir).resolve()
    if not re.fullmatch(r"/[a-zA-Z0-9_./-]+", str(root)) or root == Path("/"):
        raise BootstrapError("Installation directory must be an absolute path without whitespace or shell characters.")
    state = installed_state(root)
    if not state and not args.hostname and not args.tunnel:
        raise BootstrapError("Supply --hostname dashboard.example.com or explicitly select --tunnel.")
    with tempfile.TemporaryDirectory(prefix="pgfy-bootstrap-") as directory:
        temporary = Path(directory)
        selected = select_version(args.version, state, temporary)
        print(f"Installing Pgfy {selected}; existing installations retain their release.", flush=True)
        archive = temporary / f"pgfy-{selected}.tar.gz"
        checksum = temporary / (archive.name + ".sha256")
        base = f"{REPOSITORY}/releases/download/{selected}"
        fetch(base + "/" + checksum.name, checksum, 4096)
        fetch(base + "/" + archive.name, archive, 64 * 1024 * 1024)
        verify_archive(archive, checksum)
        bundle = extract_bundle(archive, temporary, selected)
        verify_bundle(bundle, selected, state)
        command = ["bash", str(bundle / "install.sh"), "--dir", str(root)]
        if args.hostname:
            command.extend(["--hostname", args.hostname])
        if args.tunnel:
            command.append("--tunnel")
        return subprocess.run(command, check=False).returncode


def main():
    parser = argparse.ArgumentParser(description="Install a verified Pgfy release. Fresh installs default to latest stable; reruns retain their release.")
    parser.add_argument("--version", help="Explicit release tag, for example v0.1.0")
    parser.add_argument("--dir", default="/opt/firstcommit", help="Installation directory (default: /opt/firstcommit)")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--hostname", help="Dashboard DNS hostname")
    modes.add_argument("--tunnel", action="store_true", help="Explicit loopback-only SSH-tunnel access")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.exit(1, "Run the downloaded bootstrap with sudo or as root.\n")
    if not shutil.which("curl"):
        parser.exit(1, "curl is required to download a release.\n")
    try:
        return bootstrap(args)
    except (BootstrapError, OSError, ValueError, tarfile.TarError, subprocess.TimeoutExpired) as error:
        parser.exit(1, f"Pgfy: {error}\n")
    except KeyboardInterrupt:
        parser.exit(130, "Pgfy: interrupted. Rerun with the same installation directory; existing state is preserved.\n")


if __name__ == "__main__":
    sys.exit(main())
PGFY_PYTHON
