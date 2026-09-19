# Phase 1 release and Lightsail handoff

Target: publish `v0.1.0` of the existing foundation and validate it on `firstcommit-phase1`, using `firstcommit.webbywasp.com`.
Second-host portability validation is deferred. Do not declare full Phase 1 deployment acceptance or production readiness from Lightsail alone.

## Ready for review

- The standalone bootstrap accepts `--hostname`, `--tunnel`, `--dir`, and `--version`. Fresh installs select latest stable; reruns retain their
  recorded release and image digests. Verified archives are extracted into a private temporary directory before invoking the bundled installer.
- Packaging publishes the same bootstrap inside and outside the bundle, with checksums. CI tests the release image, requires anonymous GHCR
  pull access, publishes release assets, and checks anonymous bootstrap/bundle downloads. Release notes identify later-phase features as absent.
- Existing setup, credentials, persistence, dashboard, and host recovery behavior remains in the original installer/application.

## Publish after review

Local bootstrap/installer tests, Go checks, frontend build, browser flow, and Docker integration passed; see the dated validation evidence.
The release is not yet published. AWS authentication and SSH access were restored on 2026-09-19. Read-only preflight confirmed the documented
Lightsail host meets the initial OS/resource/filesystem requirements, DNS resolves correctly, and dashboard ports are unoccupied. Docker and
Pgfy are not installed. The historical inbound 3000 rule remains to be removed during the approved deployment work.

1. Review the Phase 1 source, tests, documentation, and workflow together. Include the currently untracked application/deployment files in the
   release commit. Exclude caches, generated artifacts, credentials, and the unrelated `.commandcode/taste/taste.md` modification.
2. With approval for remote writes, push the reviewed commit to `main` and wait for its verification workflow to pass. Tag that exact commit
   `v0.1.0` and push the tag. Do not move or reuse an already-published tag.
3. Ensure `ghcr.io/c-varun14/pgfy` is publicly pullable. If the workflow stops at anonymous pull, correct package visibility and rerun the failed
   job; do not publish a bundle pointing to an inaccessible image.
4. Require successful release-image integration, packaging, and public asset verification. Run
   `python3 scripts/check-release.py --version v0.1.0` to independently repeat the anonymous download/checksum checks without installing.
5. Record the tag, commit, bundle checksum, image digests, and workflow URL in the validation evidence. Remove publication-pending wording only
   after these checks pass; keep deployment-pending wording until the VPS checks pass.

The outer checksum and bootstrap are delivered by the same trusted HTTPS release channel; this is integrity checking, not artifact signing.

## Deploy and collect evidence

Use the SSH identity and server details in [deployment access](deployment-access.md). Recheck the live instance before changes; never recreate
infrastructure because a profile cannot see it. No additional server purchase or destructive reset is part of this work.

1. Check OS/resources, existing data/installation, Docker, DNS, and listeners. Check the owning AWS profile and actual firewall; remove the
   historical inbound 3000 rule if present, preserve narrowly allowed SSH, and allow 80/443 for dashboard HTTPS.
2. During an approved installation/service-interruption window, use the [one-command installer](installation.md#one-command-installation)
   with `--hostname firstcommit.webbywasp.com`. Record fresh Docker installation only if Docker was absent; otherwise record the preserved
   compatible installation. Do not overwrite an existing incompatible release to manufacture a fresh-install result.
3. Verify trusted HTTPS, setup, login/logout, settings/status, denied unauthorized requests, and actual PostgreSQL/client versions. Keep setup
   tokens, passwords, and keys out of recordings and shared logs.
4. Create a recognizable host-side SQL test record. Check it and installation/volume identities after container restart, bootstrap rerun, and
   approved host reboot. Verify PostgreSQL outage leaves dashboard/login available with degraded readiness.
5. Exercise explicit SSH-tunnel access, hostname change, and rollback. Restore the intended HTTPS configuration. Keep missing-state and
   interrupted-initialization fault injection in disposable fixtures unless separately authorized on this host.
6. Probe closed ports from an external client and a private-network peer; cover IPv6 if enabled. Record any unavailable probe source as pending.
   Capture install duration, CPU/RAM/disk measurements, and exact evidence for each applicable acceptance item.
7. Record a short Phase 1 demo showing HTTPS, setup/status/settings, and dashboard availability during a PostgreSQL outage. This foundation
   demo does not demonstrate project provisioning or disaster recovery.

Use [the acceptance checklist](phase1-validation.md) as the record of truth. Label the outcome “implementation and Lightsail acceptance
complete; second-host portability acceptance pending” only once the relevant checks actually pass.

Remote pushes and privileged host actions require approval under the workspace's `AGENTS.md`; prepare and review the local changes first.
