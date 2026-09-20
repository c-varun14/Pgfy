# Demo runbook: recording the three-minute video

One take per segment, recorded with OBS while sharing the whole screen and talking live; cut the segments together afterwards. Everything below was rehearsed on 2026-09-20 with release v0.3.0 (timings in [phase1-validation.md](phase1-validation.md)).

## State before recording

| Item | Value |
| --- | --- |
| Server A | `pgfy-a`, Lightsail $12 bundle, static IP `100.57.106.219`, dashboard `https://firstcommit.webbywasp.com`, v0.3.0 |
| Projects on A | Shop (3 orders), Blog (2), Quiz app (2), Client CRM (3); each backed up once to S3 |
| Shop allowlist | the workstation's public IPv4 only (`/32`); check it on the Access tab before recording — mobile ISPs change addresses |
| Backup bucket | `pgfy-backups-418638389257`, prefix `pgfy/demo`, SSE-S3, IAM user `pgfy-backups` (bucket-only policy) |
| Server B | `pgfy-b`, Lightsail $12 bundle, Ubuntu 24.04, static IP `184.192.127.28` attached, firewall 80/443/5432 open — **Pgfy not installed**; the installer runs on camera (Docker engine is already present, so the run is a little quicker than a bare box) |
| Credentials | `~/pgfy-a-admin.txt` on the workstation (mode 0600): dashboard login, storage keys, project URLs. Never on screen. |
| Demo app | `cd ~/pgfy/demo && export DATABASE_URL='…' && node demo.js read` / `node demo.js write "…"` |

Do this once, before recording:

1. Add a Cloudflare **A record** `pgfy-b.webbywasp.com → 184.192.127.28` (DNS only, grey cloud). Do it at least a few minutes before recording: Let's Encrypt must resolve it when the installer runs. Check with `getent hosts pgfy-b.webbywasp.com`.
2. Open a **clean browser profile** signed in to the AWS console (Lightsail + S3) and to the A dashboard. Hide the bookmarks bar, zoom 125 %.
3. Terminal: font 18–20 pt, dark theme, prompt shortened, `cd ~/pgfy/demo` and `export DATABASE_URL` for Shop already done in one tab; a second tab for SSH.
4. Confirm your public IP matches Shop's allowlist: `curl -4 https://checkip.amazonaws.com`.
5. Put the slides on the same screen (full-screen PDF or Keynote/PowerPoint); switch with Alt+Tab. Three windows only: slides, browser, terminal.

## Server B on camera (the guide)

The instance exists (created in the console before recording, static IP attached, firewall open). Pgfy is **not** installed on it. On camera the flow is exactly the real one: DNS record → SSH → one command → token → dashboard. Full generic instructions: [installation.md](installation.md); Lightsail specifics: [lightsail.md](lightsail.md).

1. **Cloudflare → DNS**: show the A record `pgfy-b.webbywasp.com → 184.192.127.28` (DNS only).
2. **Lightsail console → Instances**: `pgfy-a` and `pgfy-b`, both $12 Ubuntu 24.04; open `pgfy-b` → Networking: static IP attached, firewall 80/443/5432 open, 22 restricted.
3. **Terminal tab 2** — SSH in and run the installer (about a minute; cut or speed up in the edit with the caption "≈1 min"):
   ```sh
   ssh -i ~/.ssh/firstcommit-lightsail-rsa ubuntu@184.192.127.28
   curl -fsSL https://github.com/c-varun14/Pgfy/releases/latest/download/bootstrap.sh -o bootstrap.sh
   sudo bash bootstrap.sh --hostname pgfy-b.webbywasp.com
   ```
   It pulls the pinned images, starts Caddy + Pgfy + PostgreSQL, obtains the Let's Encrypt certificate, delivers it to PostgreSQL, and ends with `Installation verified: https://pgfy-b.webbywasp.com` followed by the **setup token** on its own line. The token is a secret: keep it as the last line of the terminal and cover it in the edit, or copy it with the terminal off screen. It expires in 30 minutes and is consumed when the admin is created.
4. **Browser**: `https://pgfy-b.webbywasp.com` → paste the token, email, a 15+ character password → Create administrator.
5. **Settings → Backup storage**: endpoint `https://s3.us-east-1.amazonaws.com`, region `us-east-1`, bucket `pgfy-backups-418638389257`, prefix `pgfy/demo`, access key and secret from the credentials file → Save → **Check storage** (four green steps, about 1 s).
6. **Recovery**: backups of all four projects appear; pick the newest **Shop** (the one taken while frozen) → Restore as `Shop` → the job shows rows 3/3, ownership, permissions (about 1 s).
7. Project page → **Copy connection URL** → terminal tab 1: `export DATABASE_URL='<new url>'` → `node demo.js read` (three orders) → `node demo.js write "Order #1004 — placed on server B"`.
8. Back on A: **Resume writes** on Shop, or leave it frozen and say the old copy is untouched.

If the certificate step fails, the installer says so: check the DNS record resolves to `184.192.127.28` and that 80/443 are open, then re-run the same command (reruns are safe). If the token expires before you use it: `sudo /opt/firstcommit/pgfyctl setup-token`. The storage check names a failing step; the restore job records a failing check; `sudo /opt/firstcommit/pgfyctl diagnostics` for the host. Rehearse once without recording — to reset B to bare again afterwards, ask me (containers, volumes, images and `/opt/firstcommit` are removed; the instance, IP and firewall stay).

## Segment list, script and timings (2:55)

Read the lines; the on-screen action is in brackets. 403 words ≈ 156 s of speech at 155 words per minute; the demo segments (S4, S6, S7) are spoken over the clicks, which is where the remaining time lives.

**S1 — 0:00–0:10 · Slide 1 (hook)**
"Fifty small databases. One twelve-dollar server on AWS. Backups to S3, and restores that verify themselves. This is Pgfy."

**S2 — 0:10–0:42 · Slide 2 (story)**
"I run a small agency. A client told me hosting was on us: twenty dollars a month for one Postgres — two-forty a year — on top of AI and VPS bills. Dokploy gave me one-click self-hosting, but not managed databases. RDS says run many databases in one instance, then leaves every role, password and backup to you. Neon never sleeps when your workers run every ten seconds. I wanted one server I own, a database per project in one click, backups that prove they restore. So I built Pgfy."

**S3 — 0:42–0:55 · AWS console, then slide 3 (architecture)**
[Lightsail: `pgfy-a` and `pgfy-b`; S3: the bucket, `pgfy/demo/backups/…/manifest.json`; IAM user `pgfy-backups`.]
"Built on AWS: Lightsail for compute, S3 for the backups and their manifests, an IAM user scoped to that bucket. One Go binary with an embedded React dashboard, Caddy, Postgres 18 — Compose on Ubuntu."

**S4 — 0:55–1:15 · Dashboard A + terminal**
[Databases list: four projects. Open Shop → Access tab shows one allowed address. Terminal: `node demo.js read` → three orders. Access tab: replace the address with `203.0.113.0/24` → Save → `node demo.js read` → `pg_hba.conf rejects connection…` → put your address back.]
"Server A, live: four projects, each its own database, restricted role, TLS-only. This app's address is allowlisted — it reads its three orders. Take the address off the list — Postgres itself refuses it."

**S5 — 1:15–1:30 · Cloudflare DNS → Lightsail console → terminal (install cut to ~10 s in the edit, caption "≈1 min")**
[Guide steps 1–3: the A record, the two instances, the install command running to `Installation verified`.]
"One client's project took off; it deserves its own box. A DNS record, a twelve-dollar Lightsail instance, and one command — about a minute later, a dashboard with a real certificate."

**S6 — 1:30–1:45 · Dashboard A + terminal**
[Shop → **Freeze writes** → confirm. Terminal: `node demo.js write "Order #1004"` → `Failed: cannot execute INSERT in a read-only transaction`; `node demo.js read` → three orders. **Back up now** → Backups tab: succeeded, SHA-256.]
"Freeze writes on that project. The app still reads — writes are refused. Back up: two seconds; archive, manifest, checksum in S3."

**S7 — 1:45–2:05 · Dashboard B** (the winning beat — big fonts, slow cursor)
[Guide steps 4–6.]
"Server B is empty; it gets only the bucket credentials. Storage check: four steps green. It discovers the backups — Shop, seconds old. Restore: one second — and it doesn't just say success: row counts three of three, ownership, permissions, verified."

**S8 — 2:05–2:15 · Terminal**
[Guide step 7.]
"New connection string. Three orders back, a fourth written. Under five minutes; the other three projects never noticed. Server dies at two a.m.? Identical flow."

**S9 — 2:15–2:37 · Slide 4 (scale and growth)**
"How far does twelve dollars go? Measured on this twelve-dollar box: fifty databases, a hundred pooled connections, half a gigabyte of memory, over a thousand writes a second with all fifty busy. And it grows with you: when an app takes off it gets its own Pgfy box — twenty-four dollars for four gigs, forty-four for eight — restored in five minutes. Want managed failover? One pg_restore into RDS."

**S10 — 2:37–2:47 · Slide 5 (learning)**
"My first S3 backup failed: the app image had no CA bundle. Minimal images hide what's missing — so the release pipeline now runs a real S3 check."

**S11 — 2:47–2:55 · Slides 6–7 (next, close)** — slide 6 shows for a beat, then the close.
"Pgfy: every project's database on one server you own, with backups that prove they restore. Repo in the description."

## Slides (seven)

1. **Hook** — `50 databases · $12/month · restores you can verify` (numbers appear one by one).
2. **Story** — three numbered columns: `01 The bill` (“Hosting is on you.” · $20/month, $240/year for one Postgres on top of AI and VPS bills) → `02 What I tried` (Dokploy · RDS · Neon with `0.25 CU × 730 h × $0.106 = $19.34`) → `03 What I wanted` (one server I own · a database per project in one click · backups that prove they restore).
3. **Architecture** — Lightsail box (Caddy → Go app + React → Postgres 18, SQLite state) → S3 bucket (archive + manifest + SHA-256) ← IAM user; `pg_restore` arrow to RDS as the exit.
4. **Scale and growth** — stats (measured on the $12 Lightsail instance): 50 DBs / 100 idle connections / 508 MiB; 10 DBs loaded 1,431 tx/s and 6,942 reads/s; all 50 writing 1,252 tx/s; pills `daily backups, verified · 150 pooled connections · your box, your data`; the ladder: 1 many apps on one box ($12 · 2 GB) → 2 one app takes off, its own Pgfy box restored in under 5 min ($24 · 4 GB, $44 · 8 GB) → 3 managed HA/PITR via `pg_restore` into RDS ($116 · 8 GB db.m6g.large); cost table: RDS one-per-project $699 · RDS one shared $25.66 · Lightsail managed 2 GB $30 · Pgfy $12.23 (Single-AZ, us-east-1, Sep 2026).
5. **Learning** — `No CA bundle → TLS to S3 failed → release pipeline runs a real S3 check`.
6. **What's next** — production gate before real workloads; project deletion with a final backup; backup retention cleanup; PgBouncer; alerts; one-click updates; team permissions and a SQL editor; IPv6 and DNS-01 certificates.
7. **Close** — `pgfy.` · “Every project’s database on one server you own. Backups that prove they restore.” · stack line · repo and dashboard URLs · Lightsail · S3 · IAM.

## OBS

- Scene "Screen": Display Capture of the recording monitor; Audio Input Capture for the microphone; no webcam needed.
- Settings → Output: recording format **MKV** (survives a crash), remux to MP4 afterwards (File → Remux Recordings); 1920×1080, 30 fps, CRF/CQ 20.
- Hotkeys: Start/Stop recording on one key; pause between segments rather than stopping.
- Before pressing record: notifications off, second monitor off or excluded, terminal cleared, browser on the first tab, slide 1 up.
- Record each segment separately (S1–S11), leave two seconds of silence at both ends, and cut in any editor (Kdenlive, Shotcut, DaVinci). Only the install in S5 is cut down (caption "≈1 min"); caption "real time" on S6–S8. Keep the final cut under 3:00; the target is 2:55.
- Never show: the setup token, the storage secret key, the credentials file, or a connection URL long enough to read (the app reads it from the environment).
