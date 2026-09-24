import type { Plugin } from "vite";
import type { IncomingMessage, ServerResponse } from "node:http";

const now = () => Math.floor(Date.now() / 1000);
const direct = process.env.VITE_MOCK_MODE !== "tunnel";
let signedIn = true;
let storageConfigured = process.env.VITE_MOCK_STORAGE !== "0";
let storage = { endpoint: "https://s3.us-east-1.amazonaws.com", region: "us-east-1", bucket: "pgfy-demo-backups", prefix: "pgfy", access_key: "••••••••", secret_key: "••••••••", session_token: "", path_style: false, private_endpoint: false, bucket_protection: "versioning", protection_state: "enabled", protection_checked_at: now() };
let policy = { target_interval_hours: 24, retention_daily: 14, retention_weekly: 8, updated_at: now() };
/** The mock bucket says versioning is off until the operator acknowledges it,
 *  so the refusal the real API performs can be exercised in the browser. */
let bucketVersioning = process.env.VITE_MOCK_VERSIONING !== "0";

type MockProject = Record<string, unknown> & { id: string; name: string; stage: string; failed: boolean; created_at: number; ready_at: number; policy: { current_revision: number; applied_revision: number; state: string; last_error: string; addresses: string[] } };
const project = (id: string, name: string, stage: string, failed = false, offset = 86400): MockProject => ({ id, name, db_name: `app_${id.slice(4)}`, role_name: `app_${id.slice(4)}`, stage, failed, stage_error: failed ? "PostgreSQL stopped while the database was being created." : "", created_at: now() - offset, ready_at: stage === "ready" ? now() - offset + 8 : 0, size_bytes: stage === "ready" ? 48_340_992 : null, connections_now: name === "shop" ? [{ client_addr: "203.0.113.42", tls: true, application_name: "storefront", since: new Date().toISOString() }] : [], policy: { current_revision: 1, applied_revision: 1, state: "applied", last_error: "", addresses: ["0.0.0.0/0", "::/0"] }, open_to_internet: stage === "ready", rotation_pending: false, limits: { ...DEFAULT_LIMITS, revision: 1, applied_revision: 1 } });
const DEFAULT_LIMITS = { statement_timeout_ms: 60000, idle_in_transaction_ms: 300000, temp_file_limit_kb: 1048576, lock_timeout_ms: 10000, connection_limit: 25 };
let rotations = 0;
let alertSettings = { configured: false, url: "", has_secret: false, private_endpoint: false };
/** The host report: ok (default), low-disk, expiring, stale or missing; tests switch it with PUT /__mock/host. */
let hostScenario = process.env.VITE_MOCK_HOST || "ok";
function hostStatus() {
  const scenario = hostScenario;
  if (scenario === "missing") return { state: "unknown", written_at: 0, disks: [], ntp_synchronized: null, certificate: { state: "trusted", expires_at: null, expiring: false } };
  const disk = (name: string, freePercent: number) => ({ name, device: 2049, total_bytes: 80e9, free_bytes: 80e9 * freePercent / 100, free_percent: freePercent, low: freePercent < 15 });
  const expires = now() + (scenario === "expiring" ? 9 : 62) * 86400;
  return { state: scenario === "stale" ? "stale" : "ok", written_at: now() - (scenario === "stale" ? 3600 : 120), disks: [disk("postgres", scenario === "low-disk" ? 9 : 61), disk("workspace", scenario === "low-disk" ? 9 : 61), disk("root", scenario === "low-disk" ? 9 : 61)], ntp_synchronized: true,
    certificate: { state: direct ? "trusted" : "not_used", issuer: "Let's Encrypt", expires_at: direct ? expires : null, expiring: direct && scenario === "expiring", last_sync: direct ? { at: new Date().toISOString(), ok: scenario !== "expiring", message: scenario === "expiring" ? "Caddy has not obtained a certificate for the dashboard hostname yet." : "" } : undefined } };
}
let projects = [project("prj_shop", "shop", "ready", false, 86400 * 23), project("prj_blog", "blog", "role_created", false, 15), project("prj_analytics", "analytics", "role_created", true, 86400 * 4)];
const boot = Date.now();

/** One backup as the bucket holds it, which is what discovery now returns. */
type BucketBackup = { manifest_key: string; archive_key: string; db_name: string; taken_at: number; state: string; installation_id: string; project_id: string; project_name: string; postgres_version: string; table_count: number; size_bytes: number };
const backup = (projectId: string, projectName: string, hours: number, installation = "demo-installation"): BucketBackup => {
  const takenAt = now() - hours * 3600;
  const directory = `pgfy/backups/app_${projectName}/${new Date(takenAt * 1000).toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z")}`;
  return { manifest_key: `${directory}/manifest.json`, archive_key: `${directory}/archive.dump`, db_name: `app_${projectName}`, taken_at: takenAt, state: "complete", installation_id: installation, project_id: projectId, project_name: projectName, postgres_version: "17.6", table_count: 2, size_bytes: 12_840_192 + hours * 1000 };
};
let backups = [backup("prj_shop", "shop", 2), backup("prj_shop", "shop", 26), backup("prj_shop", "shop", 74), backup("prj_legacy", "legacy", 90, "another-installation")];
function databaseGroups() {
  const byDatabase = new Map<string, BucketBackup[]>();
  backups.forEach((item) => byDatabase.set(item.db_name, [...(byDatabase.get(item.db_name) || []), item]));
  return [...byDatabase.entries()].map(([dbName, rows]) => {
    const sorted = [...rows].sort((a, b) => b.taken_at - a.taken_at);
    const newest = sorted[0];
    return { db_name: dbName, project_id: newest.project_id, project_name: newest.project_name, installation_id: newest.installation_id, mixed: false,
      foreign: newest.installation_id !== "demo-installation", newest_at: newest.taken_at, count: sorted.length,
      total_bytes: sorted.reduce((sum, item) => sum + item.size_bytes, 0), has_more: false, manifest_only: 0, damaged: 0,
      reconciled_at: now() - 60, backups: sorted.slice(0, 20) };
  });
}
type MockJob = Record<string, unknown> & { id: string; kind: "backup" | "restore"; project_id: string; state: string; stage: string; created_at: number; started_at: number; finished_at: number; stage_at: number; elapsed_seconds: number; error: string; result: Record<string, unknown> };
const jobs = new Map<string, MockJob>();

function send(res: ServerResponse, status: number, body?: unknown) { res.statusCode = status; res.setHeader("Content-Type", "application/json"); res.end(body === undefined ? undefined : JSON.stringify(body)); }
function failure(res: ServerResponse, status: number, code: string, message: string) { send(res, status, { error: { code, message } }); }
async function body(req: IncomingMessage) { const chunks: Buffer[] = []; for await (const chunk of req) chunks.push(Buffer.from(chunk)); try { return JSON.parse(Buffer.concat(chunks).toString() || "{}"); } catch { return {}; } }
function updateProjectStates() { if (Date.now() - boot > 6000) projects = projects.map((item) => item.id === "prj_blog" ? { ...item, stage: "ready", ready_at: now(), size_bytes: 2_420_000 } : item); }
function tick(job: MockJob) {
  const elapsed = Math.floor((Date.now() - job.started_at * 1000) / 1000); job.elapsed_seconds = elapsed;
  const stages = job.kind === "backup" ? ["preparing", "snapshot", "dump", "checksum", "upload_archive", "upload_manifest", "done"] : ["download", "verify_archive", "create_project", "restore", "verify", "done"];
  const index = Math.min(stages.length - 1, Math.floor(elapsed / 1.5)); job.stage = stages[index]; job.state = index === stages.length - 1 ? "succeeded" : "running";
  if (job.state === "succeeded" && !job.finished_at) {
    job.finished_at = now();
    if (job.kind === "restore") job.result = process.env.VITE_MOCK_RESTORE === "errors"
      ? { verified: false, verification: "failed", restore_errors: 3, stderr: "pg_restore: error: could not execute query", summary: "Completed with 3 restore errors (reported by the tool). The database exists and can be inspected, but it is not verified.", project_id: job.project_id, checks: [{ name: "rows in public.orders", ok: false, detail: "840 restored, 842 in backup" }] }
      : { verified: true, verification: "verified", summary: "Restored and verified against the baselines recorded at backup time.", project_id: job.project_id, checks: [{ name: "Archive checksum", ok: true, detail: "Matched" }, { name: "Table row counts", ok: true, detail: "Matched" }] };
    else {
      job.result = { size_bytes: 13_200_000, sha256: "d56fc82bb2477cddd0f1" };
      const owner = projects.find((item) => item.id === job.project_id);
      if (owner) backups = [backup(owner.id, owner.name, 0), ...backups];
    }
  }
  return job;
}

export function mockApi(): Plugin {
  return { name: "pgfy-mock-api", configureServer(server) { server.middlewares.use("/api/v1", async (req, res) => {
    const path = (req.url || "/").split("?")[0]; const method = req.method || "GET";
    if (path === "/__mock/host" && method === "PUT") { hostScenario = String((await body(req) as { scenario?: string }).scenario || "ok"); return send(res, 204); }
    if (path === "/setup" && method === "GET") return send(res, 200, { available: true });
    if ((path === "/setup" || path === "/auth/login") && method === "POST") { signedIn = true; return send(res, 204); }
    if (path === "/auth/logout" && method === "POST") { signedIn = false; return send(res, 204); }
    if (path === "/auth/session") return signedIn ? send(res, 200, { email: "admin@example.com", expires_at: now() + 86400, csrf_token: "mock-csrf", client_ip: "198.51.100.18" }) : failure(res, 401, "unauthorized", "Sign in to continue.");
    if (!signedIn) return failure(res, 401, "unauthorized", "Sign in to continue.");
    if (path === "/system/status") return send(res, 200, { ready: true, maintenance: false, host: hostStatus(), sqlite: { status: "available", version: "3.49" }, postgres: { status: "available", version: "17.6" }, versions: { application: "0.1.0", worker: "0.1.0" }, backups: storageConfigured ? "ok" : "not_configured", database_access: { mode: direct ? "direct" : "tunnel", host: direct ? "db.demo.pgfy.dev" : "127.0.0.1", port: 5432, certificate: { state: direct ? "trusted" : "placeholder", issuer: direct ? "Let's Encrypt" : "", not_after: "2026-12-31" } } });
    if (path === "/settings") return send(res, 200, { id: "demo-installation", hostname: direct ? "demo.pgfy.dev" : "127.0.0.1", origin: direct ? "https://demo.pgfy.dev" : "http://127.0.0.1:8080", mode: direct ? "https" : "tunnel", release: "v0.1.0", caddy_version: "2.10.2", docker_version: "28.3.3", compose_version: "2.39.2" });
    if (path === "/settings/storage" && method === "GET") return send(res, 200, { configured: storageConfigured, settings: storage });
    if (path === "/settings/storage" && method === "PUT") {
      const input = await body(req) as Record<string, unknown>;
      // A bucket that reports versioning off is refused whichever protection is chosen.
      if (!bucketVersioning && input.bucket_protection !== "acknowledged") return failure(res, 400, "invalid_storage", "This bucket does not keep versions of deleted objects. Enable versioning on the bucket, then save again.");
      storage = { ...storage, ...input, protection_state: bucketVersioning ? "enabled" : "unsupported", protection_checked_at: now() };
      storageConfigured = true; return send(res, 200, { configured: true, settings: storage });
    }
    if (path === "/settings/backups" && method === "GET") return send(res, 200, policy);
    if (path === "/settings/backups" && method === "PUT") { const input = await body(req) as { target_interval_hours?: number }; if (![1, 6, 12, 24].includes(Number(input.target_interval_hours))) return failure(res, 400, "invalid_policy", "The backup target interval must be 1, 6, 12 or 24 hours."); policy = { ...policy, ...input, updated_at: now() }; return send(res, 200, policy); }
    if (path === "/settings/storage/check" && method === "POST") return send(res, 200, { ok: bucketVersioning, steps: [...["upload", "list", "download", "cleanup"].map((name) => ({ name, ok: true })), bucketVersioning ? { name: "protection", ok: true, detail: "the bucket keeps versions of deleted objects" } : { name: "protection", ok: false, error: "versioning is off for this bucket; a deleted backup cannot be recovered" }] });
    updateProjectStates();
    if (path === "/projects" && method === "GET") return send(res, 200, { projects, database_access: {} });
    if (path === "/projects" && method === "POST") { const input = await body(req) as { name?: string }; const next = project(`prj_${Math.random().toString(36).slice(2, 10)}`, input.name || "new database", "identity_persisted", false, 0); projects = [next, ...projects]; return send(res, 202, next); }
    const retry = path.match(/^\/projects\/([^/]+)\/retry$/); if (retry && method === "POST") { projects = projects.map((item) => item.id === retry[1] ? { ...item, failed: false, stage_error: "", stage: "ready", ready_at: now(), size_bytes: 0 } : item); return send(res, 204); }
    const credentials = path.match(/^\/projects\/([^/]+)\/credentials$/); if (credentials) { const item = projects.find((p) => p.id === credentials[1]); if (!item) return failure(res, 404, "not_found", "Database not found."); const password = "demo-password-not-for-production"; const host = direct ? "db.demo.pgfy.dev" : "127.0.0.1"; const sslmode = direct ? "verify-full" : "disable"; const url = `postgresql://app_${item.name}:${password}@${host}:5432/app_${item.name}?sslmode=${sslmode}`; return send(res, 200, { host, port: 5432, database: `app_${item.name}`, user: `app_${item.name}`, password, sslmode, url, psql: `psql \"${url}${direct ? "&sslrootcert=system" : ""}\"` }); }
    const access = path.match(/^\/projects\/([^/]+)\/access$/); if (access && method === "PUT") { const input = await body(req) as { revision: number; addresses: string[] }; const item = projects.find((p) => p.id === access[1]); if (!item) return failure(res, 404, "not_found", "Database not found."); if (input.revision !== item.policy.current_revision) return failure(res, 409, "revision_conflict", "The access rules changed elsewhere. Reload and try again."); item.policy = { ...item.policy, current_revision: input.revision + 1, addresses: input.addresses, state: "pending" }; item.open_to_internet = input.addresses.some((a) => a === "0.0.0.0/0" || a === "::/0"); setTimeout(() => { item.policy.state = "applied"; item.policy.applied_revision = item.policy.current_revision; }, 2000); return send(res, 200, item.policy); }
    const checks = path.match(/^\/projects\/([^/]+)\/connection-checks(?:\/([^/]+))?$/); if (checks && method === "POST") { const check = { id: `chk_${Date.now()}`, state: "pending", expires_at: now() + 600, created_at: now(), command: "psql \"postgresql://…\" -c \"select pg_sleep(20)\"" }; return send(res, 201, check); } if (checks && method === "GET") { const id = checks[2] || "check"; const created = Number(id.split("_")[1] || Date.now()); const success = Date.now() - created > 5000; return send(res, 200, { id, state: success ? "successful" : "pending", expires_at: now() + 590, created_at: Math.floor(created / 1000), evidence: success ? { client_addr: "203.0.113.42", tls: true, observed_at: now() } : undefined }); }
    const projectBackups = path.match(/^\/projects\/([^/]+)\/backups$/); if (projectBackups && method === "GET") { const id = projectBackups[1]; const rows = backups.filter((item) => item.project_id === id).map((item, index) => ({ id: `backup_${index}`, project_id: id, job_id: `old_${index}`, object_key: item.manifest_key, manifest: { version: 1, ...item, created_at: new Date(item.taken_at * 1000).toISOString(), tables: [{ schema: "public", name: "users", rows: 120 }, { schema: "public", name: "orders", rows: 842 }] }, size_bytes: item.size_bytes, created_at: item.taken_at })); const newest = rows[0] ? rows[0].created_at : 0; return send(res, 200, { backups: rows, jobs: [...jobs.values()].filter((j) => j.project_id === id).map(tick), storage_configured: storageConfigured, next_scheduled_at: newest ? newest + policy.target_interval_hours * 3600 : 0, newest_backup_at: newest, target_interval_hours: policy.target_interval_hours, failures: 0, last_attempt_at: newest }); }
    if (projectBackups && method === "POST") { if (!storageConfigured) return failure(res, 409, "storage_not_configured", "Configure backup storage first."); const job: MockJob = { id: `job_${Date.now()}`, kind: "backup", project_id: projectBackups[1], state: "queued", stage: "queued", created_at: now(), started_at: now(), finished_at: 0, stage_at: now(), elapsed_seconds: 0, error: "", result: {} }; jobs.set(job.id, job); return send(res, 202, job); }
    const limits = path.match(/^\/projects\/([^/]+)\/limits$/); if (limits && method === "PUT") { const input = await body(req) as Record<string, number>; const item = projects.find((p) => p.id === limits[1]); if (!item) return failure(res, 404, "not_found", "Database not found."); const current = item.limits as { revision: number }; if (input.revision !== current.revision) return failure(res, 409, "revision_conflict", "The limits changed elsewhere. Reload and try again."); if (input.temp_file_limit_kb === 0) return failure(res, 400, "invalid_limits", "temporary file limit must be unlimited or between 1 MB and 1 TB"); const { revision, ...values } = input; item.limits = { ...values, revision: revision + 1, applied_revision: revision + 1 }; return send(res, 200, item.limits); }
    const rotate = path.match(/^\/projects\/([^/]+)\/credentials\/rotate$/); if (rotate && method === "POST") { const item = projects.find((p) => p.id === rotate[1]); if (!item) return failure(res, 404, "not_found", "Database not found."); rotations += 1; const password = `rotated-password-${rotations}`; const host = direct ? "db.demo.pgfy.dev" : "127.0.0.1"; const sslmode = direct ? "verify-full" : "disable"; const url = `postgresql://app_${item.name}:${password}@${host}:5432/app_${item.name}?sslmode=${sslmode}`; return send(res, 200, { credentials: { host, port: 5432, database: `app_${item.name}`, user: `app_${item.name}`, password, sslmode, url, psql: `psql "${url}"` }, rotated_at: now() }); }
    if (path === "/settings/alerts" && method === "GET") return send(res, 200, alertSettings);
    if (path === "/settings/alerts" && method === "PUT") { const input = await body(req) as { url: string; secret: string; private_endpoint: boolean }; if (input.url && !input.url.startsWith("https://") && !input.private_endpoint) return failure(res, 400, "invalid_webhook", "the webhook must use https unless it is a private endpoint on this network"); const parsed = input.url ? new URL(input.url) : null; alertSettings = { configured: !!input.url, url: parsed ? `${parsed.protocol}//${parsed.host}${parsed.pathname.length > 1 ? "/…" : ""}` : "", has_secret: !!input.secret || (alertSettings.has_secret && !!input.url), private_endpoint: input.private_endpoint }; return send(res, 200, alertSettings); }
    if (path === "/settings/alerts/test" && method === "POST") return send(res, 200, { ok: true });
    if (path === "/alerts") return send(res, 200, { conditions: [{ key: "backup_stale:prj_shop", kind: "backup_stale", summary: "The newest recoverable backup of shop is older than its target", detail: "Older than 1.5 times the backup target interval.", active: true, first_seen_at: now() - 7200, last_fired_at: now() - 7200, sent_state: "firing" }], delivery: { last_ok_at: now() - 7200, last_error: "" } });
    if (path === "/system/connections") { const roles = projects.filter((p) => p.stage === "ready").map((p) => ({ role: `app_${p.name}`, project: p.name, limit: (p.limits as { connection_limit: number }).connection_limit, connections: p.name === "shop" ? 21 : 0, warning: p.name === "shop" })); return send(res, 200, { max_connections: 150, superuser_reserved: 3, reserved: 10, available: 137, projects_used: 21, projects_limit: roles.reduce((sum, r) => sum + r.limit, 0), other_used: 0, warning: false, overcommitted: false, roles, system: [{ role: "pgfy_mgmt", limit: 8, connections: 2, warning: false }, { role: "pgfy_health", limit: 5, connections: 1, warning: false }] }); }
    const detail = path.match(/^\/projects\/([^/]+)$/); if (detail) { const item = projects.find((p) => p.id === detail[1]); return item ? send(res, 200, { project: item, database_access: { mode: direct ? "direct" : "tunnel", host: direct ? "db.demo.pgfy.dev" : "127.0.0.1", port: 5432, certificate: { state: direct ? "trusted" : "placeholder", issuer: direct ? "Let's Encrypt" : "" } } }) : failure(res, 404, "not_found", "Database not found."); }
    if (path === "/recovery/backups" && method === "GET") return send(res, 200, storageConfigured ? { state: "ok", databases: databaseGroups(), installation_id: "demo-installation", reconciled_at: now() - 60, busy: [...jobs.values()].some((j) => tick(j).state === "running"), restores: [...jobs.values()].filter((j) => j.kind === "restore").map(tick) } : { state: "storage_not_configured", databases: [], restores: [] });
    if (path === "/recovery/restores" && method === "POST") { const input = await body(req) as { manifest_key: string; name: string }; const restored = project(`prj_${Math.random().toString(36).slice(2, 10)}`, input.name, "ready", false, 0); projects = [restored, ...projects]; const job: MockJob = { id: `job_${Date.now()}`, kind: "restore", project_id: restored.id, state: "queued", stage: "queued", created_at: now(), started_at: now(), finished_at: 0, stage_at: now(), elapsed_seconds: 0, error: "", result: {} }; jobs.set(job.id, job); return send(res, 202, { project: restored, job }); }
    const jobPath = path.match(/^\/jobs\/([^/]+)$/); if (jobPath) { const job = jobs.get(jobPath[1]); return job ? send(res, 200, tick(job)) : failure(res, 404, "not_found", "Job not found."); }
    return failure(res, 404, "not_found", `No mock route for ${method} ${path}`);
  }); } };
}
