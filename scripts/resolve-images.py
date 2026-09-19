#!/usr/bin/env python3
"""Explicit release-maintainer action; installation never resolves mutable tags."""
import hashlib
import json
import pathlib
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]

def resolve(repository, tag):
    auth = f"https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/{repository}:pull"
    with urllib.request.urlopen(auth, timeout=30) as response:
        token = json.load(response)["token"]
    request = urllib.request.Request(
        f"https://registry-1.docker.io/v2/library/{repository}/manifests/{tag}",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"},
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        body = response.read()
        digest = response.headers["Docker-Content-Digest"]
        assert digest == "sha256:" + hashlib.sha256(body).hexdigest()
    return f"{repository}:{tag}@{digest}"

if __name__ == "__main__":
    result = {name: resolve(name, tag) for name, tag in {"postgres": "18.6-bookworm", "caddy": "2.10.2-alpine", "golang": "1.27.1-bookworm", "node": "24.18.0-bookworm-slim"}.items()}
    (ROOT / "deploy/images.lock.json").write_text(json.dumps(result, indent=2) + "\n")
