# Phase 1 foundation

Pgfy provides a self-hosted PostgreSQL foundation with an embedded dashboard, single-admin setup/login, component status, read-only settings,
and host-managed installation/recovery commands. The release includes a standalone bootstrap and a checksummed bundle with digest-pinned
application, PostgreSQL, and Caddy images. Ubuntu 24.04 LTS x86-64 is the initial supported target.

The bootstrap selects the latest stable release on fresh installations, supports an explicit version, and retains the installed version on
reruns. HTTPS dashboard setup and explicit SSH-tunnel mode use the same bundled installer. See the repository installation guide for the
copy-paste command and operator prerequisites.

Project provisioning, public database TLS/access rules, S3-compatible backups, and fresh-server disaster recovery are not implemented in this
release. It is not production-ready. Publishing a release does not establish VPS acceptance; consult the Phase 1 validation document for
recorded Lightsail results and outstanding second-host portability checks.
