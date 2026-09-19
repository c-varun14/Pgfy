#!/usr/bin/env bash
set -Eeuo pipefail
# Docker invokes this once on an empty cluster. A failed script is NEVER assumed resumable.
# Secrets are passed over stdin and are never SQL command arguments or log output.
health_password=$(cat /run/secrets/health_password)
installation_id=$(cat /etc/pgfy/installation-id)
[[ "$health_password" =~ ^[a-f0-9]{64}$ ]] || exit 1
[[ "$installation_id" =~ ^[a-f0-9]{32}$ ]] || exit 1
export PGPASSWORD
PGPASSWORD=$(cat /run/secrets/bootstrap_password)
psql --username pgfy_bootstrap --dbname pgfy_system --no-psqlrc --set ON_ERROR_STOP=1 <<SQL
BEGIN;
REVOKE ALL ON DATABASE pgfy_system FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE ROLE pgfy_health LOGIN PASSWORD '$health_password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 5;
GRANT CONNECT ON DATABASE pgfy_system TO pgfy_health;
CREATE SCHEMA pgfy_internal;
REVOKE ALL ON SCHEMA pgfy_internal FROM PUBLIC;
CREATE TABLE pgfy_internal.initialization (singleton boolean PRIMARY KEY CHECK(singleton), installation_id text NOT NULL);
INSERT INTO pgfy_internal.initialization VALUES (true, '$installation_id');
GRANT USAGE ON SCHEMA pgfy_internal TO pgfy_health;
GRANT SELECT ON pgfy_internal.initialization TO pgfy_health;
COMMIT;
SQL
# This final marker detects a failure after SQL succeeds as well as skipped init scripts.
printf '%s\n' "$installation_id" > "$PGDATA/.pgfy-initialized"
chmod 600 "$PGDATA/.pgfy-initialized"
unset PGPASSWORD health_password
