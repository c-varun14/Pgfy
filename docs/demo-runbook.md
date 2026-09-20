# Demo runbook: recording the three-minute video

One take per segment, recorded with OBS while sharing the whole screen and talking live; cut the segments together afterwards. Everything below was rehearsed on 2026-09-20 with release v0.3.0 (timings in [phase1-validation.md](phase1-validation.md)).

## State before recording

| Item | Value |
| --- | --- |
| Server A | `pgfy-a`, Lightsail $12 bundle, static IP `100.57.106.219`, dashboard `https://firstcommit.webbywasp.com`, v0.3.0 |
| Projects on A | Shop (3 orders), Blog (2), Quiz app (2), Client CRM (3); each backed up once to S3 |
| Shop allowlist | the workstation's public IPv4 only (`/32`); check it on the Access tab before recording — mobile ISPs change addresses |
| Backup bucket | `pgfy-backups-418638389257`, prefix `pgfy/demo`, SSE-S3, IAM user `pgfy-backups` (bucket-only policy) |
| Static IP for B | `pgfy-b-ip` = `184.192.127.28`, allocated and unattached |
| Credentials | `~/pgfy-a-admin.txt` on the workstation (mode 0600): dashboard login, storage keys, project URLs. Never on screen. |
| Demo app | `cd ~/pgfy/demo && export DATABASE_URL='…' && node demo.js read` / `node demo.js write "…"` |

Do this once, before recording:

1. Add a Cloudflare **A record** `pgfy-b.webbywasp.com → 184.192.127.28` (DNS only, no proxy). Let's Encrypt needs it resolvable when the installer runs.
2. Open a **clean browser profile** signed in to the AWS console (Lightsail + S3) and to the A dashboard. Hide the bookmarks bar, zoom 125 %.
3. Terminal: font 18–20 pt, dark theme, prompt shortened, `cd ~/pgfy/demo` and `export DATABASE_URL` for Shop already done in one tab; a second tab for SSH.
4. Confirm your public IP matches Shop's allowlist: `curl -4 https://checkip.amazonaws.com`.
5. Put the slides on the same screen (full-screen PDF or Keynote/PowerPoint); switch with Alt+Tab. Three windows only: slides, browser, terminal.

## Server B on camera (the guide)

Recorded in real time, sped up in the edit to about 15 seconds with the caption "sped up".

1. Lightsail console → **Create instance** → Linux/Unix → OS only → **Ubuntu 24.04 LTS** → $12 plan → key pair `firstcommit-lightsail` → name `pgfy-b` → Create. (About 20 s to "Running".)
2. Instance → **Networking** → attach static IP `pgfy-b-ip` → IPv4 firewall: add **HTTPS 443** and **Custom TCP 5432**; restrict SSH 22 to your IP if you like. (80 and 22 exist by default.)
3. Terminal tab 2:
   ```sh
   ssh -i ~/.ssh/firstcommit-lightsail-rsa ubuntu@184.192.127.28
   curl -fsSL https://github.com/c-varun14/Pgfy/releases/latest/download/bootstrap.sh -o bootstrap.sh
   sudo bash bootstrap.sh --hostname pgfy-b.webbywasp.com
   ```
   Measured: 63 s on a fresh instance. The last lines print the HTTPS address and the **setup token** — the token is a secret; on camera, scroll it off screen or cover it in the edit. It expires in 30 minutes and is consumed when the admin is created.
4. Browser: open `https://pgfy-b.webbywasp.com`, paste the token, create the administrator (any email, 15+ character password).
5. **Settings → Backup storage**: endpoint `https://s3.us-east-1.amazonaws.com`, region `us-east-1`, bucket `pgfy-backups-418638389257`, prefix `pgfy/demo`, access key and secret from the credentials file → Save → **Check storage** (four green steps, about 1 s).
6. **Recovery**: the backups of all four projects appear; pick the newest **Shop** → Restore as `Shop` → the job shows the checks: rows 3/3, ownership, permissions (about 1 s).
7. Project page → **Copy connection URL** → terminal tab 1: `export DATABASE_URL='<new url>'` → `node demo.js read` (three orders) → `node demo.js write "Order #1004 — placed on server B"`.
8. Back on A: **Resume writes** on Shop (or leave it frozen to show the old copy is untouched — say which).

If anything fails: `sudo /opt/firstcommit/pgfyctl setup-token` reissues a token only after the previous one expires; the storage check names the failing step; the restore job records the failing check. Rehearse once without recording.

## Segment list, script and timings (2:55)

Read the lines; the on-screen action is in brackets. Word count fits 150–160 words per minute without rushing.

**S1 — 0:00–0:12 · Slide 1 (hook)**
"Fifty small databases. One twelve-dollar server on AWS. Backups to S3, and restores that verify themselves. This is Pgfy."

**S2 — 0:12–0:50 · Slide 2 (story)**
"I run a small agency. A client told me hosting was on us — and even a two-hundred-forty-dollar month hurt. Dokploy spoiled me with one-click self-hosting, but its databases weren't managed the way I needed. RDS's own docs say: run one Postgres, many databases, manage the credentials yourself. I wanted that — with one click, and real backups. Neon? My apps run workers every ten seconds, so the database never sleeps: nineteen dollars per project, always on. And in the AI era we all ship niche apps for small audiences — many small databases, none of them big. So I built Pgfy."

**S3 — 0:50–1:05 · AWS console, then slide 3 (architecture)**
[Lightsail: `pgfy-a` running; S3: the bucket, `pgfy/demo/backups/…/manifest.json`; IAM user `pgfy-backups`.]
"Built on AWS: Lightsail for compute, an S3 bucket holding the backups and their manifests, an IAM user scoped to that bucket. The stack: one Go binary with an embedded React dashboard, Caddy, Postgres 18 — Docker Compose on Ubuntu."

**S4 — 1:05–1:25 · Dashboard A + terminal**
[Databases list: four projects. Open Shop → Access tab shows one allowed address. Terminal: `node demo.js read` → three orders. Access tab: replace the address with `203.0.113.0/24` → Save → `node demo.js read` → `pg_hba.conf rejects connection…` → put your address back.]
"Server A, live: four projects, each its own database, restricted role, TLS-only. This app's address is allowlisted — it reads its three orders. Take the address off the list — Postgres itself refuses it."

**S5 — 1:25–1:40 · Lightsail console + terminal (sped up, caption "63 s, sped up")**
[Steps 1–4 of the guide.]
"One client's project took off; it deserves its own box. Ubuntu, twelve-dollar plan, one command. A minute later: a dashboard with a real certificate."

**S6 — 1:40–1:55 · Dashboard A + terminal**
[Shop → **Freeze writes** → confirm. Terminal: `node demo.js write "Order #1004"` → `Failed: cannot execute INSERT in a read-only transaction`; `node demo.js read` → three orders. **Back up now** → Backups tab: succeeded, SHA-256.]
"Freeze writes on that project. The app still reads — writes are refused. Back up: two seconds; archive, manifest, checksum in S3."

**S7 — 1:55–2:15 · Dashboard B** (the winning beat — big fonts, slow cursor)
[Steps 5–6 of the guide.]
"Server B is empty; it gets only the bucket credentials. Storage check: four steps green. It discovers the backups — Shop, seconds old. Restore: one second — and it doesn't just say success: row counts three of three, ownership, permissions, verified."

**S8 — 2:15–2:25 · Terminal**
[Step 7 of the guide.]
"New connection string. Three orders back, a fourth written. Under five minutes; the other three projects never noticed. Server dies at two a.m.? Identical flow."

**S9 — 2:25–2:40 · Slide 4 (scale and limits)**
"How far does it go? Fifty databases, a hundred pooled connections, under five hundred megabytes; all fifty writing at once, about a thousand transactions a second. One node, up to a day at risk. RDS is the industry-grade answer — point-in-time recovery, failover, patching — for workloads that need it. Pgfy is the first rung; the backup is a plain pg_dump, so RDS is one pg_restore away."

**S10 — 2:40–2:50 · Slide 5 (learning)**
"My first S3 backup failed: the app image had no CA bundle. Minimal images hide what's missing — the release pipeline now runs a real S3 check before publishing."

**S11 — 2:50–2:55 · Slide 6 (close)**
"Pgfy. Lightsail, S3, IAM. Cheap enough to experiment, honest about its limits, a way out when you win."

## Slides (six)

1. **Hook** — `50 databases · $12/month · restores you can verify` (numbers appear one by one).
2. **Story** — `$240/month · "hosting is on you" · workers every 10 s never sleep` and the Neon arithmetic `0.25 CU × 730 h × $0.106 = $19.34/project`.
3. **Architecture** — Lightsail box (Caddy → Go app + React → Postgres 18, SQLite state) → S3 bucket (archive + manifest + SHA-256) ← IAM user; `pg_restore` arrow to RDS as the exit.
4. **Scale and limits** — table: 50 DBs / 100 idle connections / 490 MiB; 10 DBs loaded 943 tx/s and 8,866 reads/s; all 50 writing 962 tx/s; `one node · 24 h RPO · 150 pooled connections`; beside it RDS: `PITR · Multi-AZ · patching · compliance`; cost row: RDS one-per-project $699 · RDS one shared $25.66 · Lightsail managed 2 GB $30 · Pgfy $12.23 (Single-AZ, us-east-1, Sep 2026).
5. **Learning** — `No CA bundle → TLS to S3 failed → release pipeline runs a real S3 check`.
6. **Close** — repo URL, `pgfy-a` dashboard URL, "Lightsail · S3 · IAM".

## OBS

- Scene "Screen": Display Capture of the recording monitor; Audio Input Capture for the microphone; no webcam needed.
- Settings → Output: recording format **MKV** (survives a crash), remux to MP4 afterwards (File → Remux Recordings); 1920×1080, 30 fps, CRF/CQ 20.
- Hotkeys: Start/Stop recording on one key; pause between segments rather than stopping.
- Before pressing record: notifications off, second monitor off or excluded, terminal cleared, browser on the first tab, slide 1 up.
- Record each segment separately (S1–S11), leave two seconds of silence at both ends, and cut in any editor (Kdenlive, Shotcut, DaVinci). Add captions "sped up" on S5 and "real time" on S6–S8. Keep the final cut under 3:00; the target is 2:55.
- Never show: the setup token, the storage secret key, the credentials file, or a connection URL long enough to read (the app reads it from the environment).
