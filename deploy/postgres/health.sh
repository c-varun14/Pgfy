#!/usr/bin/env bash
set -Eeuo pipefail
[[ -f "$PGDATA/.pgfy-initialized" ]] || exit 1
[[ "$(cat "$PGDATA/.pgfy-initialized")" == "$(cat /etc/pgfy/installation-id)" ]] || exit 1
export PGPASSWORD
PGPASSWORD=$(cat /run/secrets/health_password)
# TCP proves SCRAM works. pg_isready alone does not authenticate.
result=$(PGCONNECT_TIMEOUT=3 psql -h 127.0.0.1 -U pgfy_health -d pgfy_system -XAt --set ON_ERROR_STOP=1 -c "SELECT installation_id FROM pgfy_internal.initialization WHERE singleton")
[[ "$result" == "$(cat /etc/pgfy/installation-id)" ]]
