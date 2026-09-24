#!/usr/bin/env bash
set -Eeuo pipefail
# Docker invokes this once on an empty cluster. A failed script is NEVER assumed resumable.
# Secrets are passed over stdin and are never SQL command arguments or log output.
health_password=$(cat /run/secrets/health_password)
management_password=$(cat /run/secrets/management_password)
installation_id=$(cat /etc/pgfy/installation-id)
[[ "$health_password" =~ ^[a-f0-9]{64}$ ]] || exit 1
[[ "$management_password" =~ ^[a-f0-9]{64}$ ]] || exit 1
[[ "$installation_id" =~ ^[a-f0-9]{32}$ ]] || exit 1
export PGPASSWORD
PGPASSWORD=$(cat /run/secrets/bootstrap_password)
psql --username pgfy_bootstrap --dbname pgfy_system --no-psqlrc --set ON_ERROR_STOP=1 <<SQL
BEGIN;
REVOKE ALL ON DATABASE pgfy_system FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE ROLE pgfy_health LOGIN PASSWORD '$health_password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 5;
GRANT CONNECT ON DATABASE pgfy_system TO pgfy_health;
-- Provisioning role: never a superuser, never replication; only administers roles it creates.
CREATE ROLE pgfy_mgmt LOGIN PASSWORD '$management_password' NOSUPERUSER CREATEDB CREATEROLE NOREPLICATION CONNECTION LIMIT 8;
GRANT CONNECT ON DATABASE pgfy_system TO pgfy_mgmt;
-- Roles it creates are granted back to it, so it can own project databases, dump and restore them.
ALTER ROLE pgfy_mgmt SET createrole_self_grant = 'set, inherit';
-- Connection evidence (pg_stat_activity/pg_stat_ssl) and access-policy reloads, nothing broader.
GRANT pg_read_all_stats TO pgfy_mgmt;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_reload_conf() TO pgfy_mgmt;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_hba_file_rules() TO pgfy_mgmt;
GRANT SELECT ON pg_catalog.pg_hba_file_rules TO pgfy_mgmt;
-- Management and health keep reserved connection slots when project roles fill the rest,
-- and management may set temp_file_limit on project roles (a superuser-only parameter).
GRANT pg_use_reserved_connections TO pgfy_mgmt, pgfy_health;
GRANT SET ON PARAMETER temp_file_limit TO pgfy_mgmt;
-- Project roles must never reach the maintenance databases.
REVOKE CONNECT ON DATABASE postgres FROM PUBLIC;
REVOKE CONNECT ON DATABASE template1 FROM PUBLIC;
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
unset PGPASSWORD health_password management_password
