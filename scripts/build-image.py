#!/usr/bin/env python3
import argparse
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--tag", default="pgfy:dev")
parser.add_argument("--version", default="dev")
args = parser.parse_args()
images = json.loads((root / "deploy/images.lock.json").read_text())
commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
subprocess.run(["docker", "build", "--platform", "linux/amd64", "--build-arg", "POSTGRES_IMAGE=" + images["postgres"], "--build-arg", "GO_IMAGE=" + images["golang"], "--build-arg", "NODE_IMAGE=" + images["node"], "--build-arg", "VERSION=" + args.version, "--build-arg", "COMMIT=" + commit, "-t", args.tag, "."], cwd=root, check=True)
