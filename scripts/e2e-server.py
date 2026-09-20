#!/usr/bin/env python3
"""Disposable local browser-test fixture. Never used by the installer."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--backend-only", action="store_true", help="Listen on 127.0.0.1:3000 behind the local Vite server on port 8080")
args = parser.parse_args()
port = os.environ.get("PGFY_E2E_PORT", "8080")
with tempfile.TemporaryDirectory(prefix="pgfy-e2e-") as temporary:
    directory = Path(temporary)
    config = {"id": "e2e", "mode": "tunnel", "origin": "http://127.0.0.1:8080", "hostname": "", "generation": "1", "release": "test", "caddy_version": "not used in browser fixture", "docker_version": "not used in browser fixture", "compose_version": "not used in browser fixture"}
    (directory / "install.json").write_text(json.dumps(config))
    (directory / "key").write_bytes(secrets.token_bytes(32))
    (directory / "key").chmod(0o600)
    env = dict(os.environ, PGFY_CONFIG=str(directory / "install.json"), PGFY_DB=str(directory / "metadata.db"), PGFY_KEY_FILE=str(directory / "key"), PGFY_HEALTH_PASSWORD_FILE=str(directory / "absent"), PGFY_LISTEN="127.0.0.1:3000" if args.backend_only else f"127.0.0.1:{port}")
    binary = root / "bin/pgfy"
    subprocess.run([binary, "initialize-store"], env=env, check=True)
    token = subprocess.check_output([binary, "setup-token"], env=env, text=True).strip()
    (root / ".cache").mkdir(exist_ok=True)
    token_file = root / ".cache" / (f"e2e-token-{port}" if "PGFY_E2E_PORT" in os.environ else "e2e-token")
    token_file.write_text(token)
    token_file.chmod(0o600)
    process = subprocess.Popen([binary], env=env)
    def stop(*_):
        process.terminate()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        process.wait()
    finally:
        process.terminate()
        process.wait(timeout=15)
        token_file.unlink(missing_ok=True)
