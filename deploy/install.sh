#!/usr/bin/env bash
set -Eeuo pipefail
command -v python3 >/dev/null || { echo 'Python 3 is required (Ubuntu 24.04 includes it).' >&2; exit 1; }
bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$bundle_dir/installer.py" install --bundle "$bundle_dir" "$@"
