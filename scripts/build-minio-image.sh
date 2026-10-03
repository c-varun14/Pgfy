#!/usr/bin/env bash
# Builds the S3-compatible test store used by scripts/integration.py from pinned MinIO sources, wrapped in the
# pinned PostgreSQL image. The community images the fixture used to pull are no longer served.
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work="$root/.cache/minio-image"
mkdir -p "$work"
CGO_ENABLED=0 GOBIN="$work" go install github.com/minio/minio@RELEASE.2025-04-22T22-12-26Z
CGO_ENABLED=0 GOBIN="$work" go install github.com/minio/mc@RELEASE.2025-04-16T18-13-26Z
base=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["postgres"])' "$root/deploy/images.lock.json")
printf 'FROM %s\nCOPY minio mc /usr/local/bin/\nENTRYPOINT ["minio"]\n' "$base" > "$work/Dockerfile"
docker build -q -t "${1:-pgfy-minio:test}" "$work"
