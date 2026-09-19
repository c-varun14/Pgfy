#!/usr/bin/env python3
"""Verify publicly downloadable release assets without running the installer."""
import argparse
from pathlib import Path
import tempfile
import types

ROOT = Path(__file__).resolve().parents[1]
source = (ROOT / "deploy/bootstrap.sh").read_text().split("<<'PGFY_PYTHON'\n", 1)[1].rsplit("\nPGFY_PYTHON", 1)[0]
bootstrap = types.ModuleType("pgfy_bootstrap")
exec(compile(source, "deploy/bootstrap.sh", "exec"), bootstrap.__dict__)

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--version", required=True)
args = parser.parse_args()
selected = bootstrap.version(args.version)
try:
    with tempfile.TemporaryDirectory(prefix="pgfy-release-check-") as directory:
        temporary = Path(directory)
        base = f"{bootstrap.REPOSITORY}/releases/download/{selected}"
        archive = temporary / f"pgfy-{selected}.tar.gz"
        checksum = temporary / (archive.name + ".sha256")
        bootstrap.fetch(base + "/" + archive.name, archive, 64 * 1024 * 1024)
        bootstrap.fetch(base + "/" + checksum.name, checksum, 4096)
        bootstrap.verify_archive(archive, checksum)
        bundle = bootstrap.extract_bundle(archive, temporary, selected)
        bootstrap.verify_bundle(bundle, selected, None)
        script = temporary / "bootstrap.sh"
        script_checksum = temporary / "bootstrap.sh.sha256"
        bootstrap.fetch(base + "/bootstrap.sh", script, 1024 * 1024)
        bootstrap.fetch(base + "/bootstrap.sh.sha256", script_checksum, 4096)
        bootstrap.verify_archive(script, script_checksum)
        if script.read_bytes() != (ROOT / "deploy/bootstrap.sh").read_bytes() or script.read_bytes() != (bundle / "bootstrap.sh").read_bytes():
            raise bootstrap.BootstrapError("Published bootstrap does not match the release source and bundle.")
        if "-" not in selected:
            latest = bootstrap.select_version(None, None, temporary)
            if latest == selected:
                latest_script = temporary / "latest-bootstrap.sh"
                bootstrap.fetch(f"{bootstrap.REPOSITORY}/releases/latest/download/bootstrap.sh", latest_script, 1024 * 1024)
                if latest_script.read_bytes() != script.read_bytes():
                    raise bootstrap.BootstrapError("Latest-release bootstrap does not match the selected release.")
            else:
                print(f"Latest stable is {latest}; explicit release {selected} remains downloadable.")
        print(f"PASS: anonymous bootstrap/bundle downloads and checksums for {selected}; installer was not executed.")
except (bootstrap.BootstrapError, OSError, ValueError) as error:
    parser.exit(1, f"Release verification failed: {error}\n")
