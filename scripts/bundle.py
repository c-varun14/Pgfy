#!/usr/bin/env python3
"""Package an already published, digest-pinned image. Does not publish anything."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import tarfile

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--version", required=True)
parser.add_argument("--image", required=True)
parser.add_argument("--output-dir", type=Path, default=root / "dist", help="Directory for generated release artifacts")
args = parser.parse_args()
if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?", args.version) or not re.fullmatch(r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}", args.image):
    parser.error("version and immutable application image are required")
target = args.output_dir / ("pgfy-" + args.version)
target.mkdir(parents=True, exist_ok=False)
for name in ("bootstrap.sh", "install.sh", "pgfyctl", "installer.py", "compose.yaml", "compose.https.yaml", "compose.tunnel.yaml", "postgres"):
    source = root / "deploy" / name
    if source.is_dir():
        shutil.copytree(source, target / name)
    else:
        shutil.copy2(source, target / name)
for name in ("installation.md", "lightsail.md", "phase1-validation.md"):
    shutil.copy2(root / "docs" / name, target / name)
images = json.loads((root / "deploy/images.lock.json").read_text())
release = {"version": args.version, "images": {"application": args.image, "postgres": images["postgres"], "caddy": images["caddy"]}, "docker_packages": {"docker-ce": "5:29.8.0-1~ubuntu.24.04~noble", "docker-ce-cli": "5:29.8.0-1~ubuntu.24.04~noble", "containerd.io": "2.3.5-1~ubuntu.24.04~noble", "docker-compose-plugin": "5.5.1-1~ubuntu.24.04~noble"}}
(target / "release.json").write_text(json.dumps(release, indent=2) + "\n")
for name in ("bootstrap.sh", "install.sh", "pgfyctl"):
    (target / name).chmod(0o755)
checksums = "".join(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + str(path.relative_to(target)) + "\n" for path in sorted(target.rglob("*")) if path.is_file())
(target / "SHA256SUMS").write_text(checksums)
# Path.with_suffix would truncate a semantic version; retain the complete bundle name.
archive = target.parent / (target.name + ".tar.gz")
with tarfile.open(archive, "w:gz") as output:
    output.add(target, arcname=target.name)
(archive.parent / (archive.name + ".sha256")).write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
bootstrap = archive.parent / "bootstrap.sh"
shutil.copy2(target / "bootstrap.sh", bootstrap)
(archive.parent / "bootstrap.sh.sha256").write_text(hashlib.sha256(bootstrap.read_bytes()).hexdigest() + "  bootstrap.sh\n")
print(archive)
