import type { Plugin } from "vite";
import type { IncomingMessage, ServerResponse } from "node:http";

const now = () => Math.floor(Date.now() / 1000);
const direct = process.env.VITE_MOCK_MODE !== "tunnel";
let signedIn = true;
let storageConfigured = process.env.VITE_MOCK_STORAGE !== "0";
let storage = { endpoint: "https://s3.us-east-1.amazonaws.com", region: "us-east-1", bucket: "pgfy-demo-backups", prefix: "pgfy", access_key: "••••••••", secret_key: "••••••••", session_token: "", path_style: false };

type MockProject = Record<string, unknown> & { id: string; name: string; stage: string; failed: boolean; created_at: number; ready_at: number; policy: { current_revision: number; applied_revision: number; state: string; last_error: string; addresses: string[] } };
const project = (id: string, name: string, stage: string, failed = false, offset = 86400): MockProject => ({ id, name, db_name: `app_${id.slice(4)}`, role_name: `app_${id.slice(4)}`, stage, failed, stage_error: failed ? "PostgreSQL stopped while the database was being created." : "", created_at: now() - offset, ready_at: stage === "ready" ? now() - offset + 8 : 0, size_bytes: stage === "ready" ? 48_340_992 : null, connections_now: name === "shop" ? [{ client_addr: "203.0.113.42", tls: true, application_name: "storefront", since: new Date().toISOString() }] : [], policy: { current_revision: 1, applied_revision: 1, state: "applied", last_error: "", addresses: ["0.0.0.0/0", "::/0"] } });
let projects = [project("prj_shop", "shop", "ready", false, 86400 * 23), project("prj_blog", "blog", "role_created", false, 15), project("prj_analytics", "analytics", "role_created", true, 86400 * 4)];
const boot = Date.now();

type Manifest = { version: number; installation_id: string; project_id: string; project_name: string; db_name: string; postgres_version: string; created_at: string; archive_key: string; sha256: string; size_bytes: number; tables: { schema: string; name: string; rows: number }[]; manifest_key: string };
const manifest = (projectId: string, projectName: string, hours: number): Manifest => ({ version: 1, installation_id: hours > 50 ? "another-installation" : "demo-installation", project_id: projectId, project_name: projectName, db_name: `app_${projectName}`, postgres_version: "17.6", created_at: new Date(Date.now() - hours * 3600000).toISOString(), archive_key: `${projectName}/${hours}.dump`, sha256: "3f2a7095b815cabb0ae8e87ef883ff28", size_bytes: 12_840_192 + hours * 1000, tables: [{ schema: "public", name: "users", rows: 120 }, { schema: "public", name: "orders", rows: 842 }], manifest_key: `${projectName}/${hours}.json` });
let manifests = [manifest("prj_shop", "shop", 2), manifest("prj_shop", "shop", 26), manifest("prj_shop", "shop", 74)];
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
    if (job.kind === "restore") job.result = { verified: true, project_id: job.project_id, checks: [{ name: "Archive checksum", ok: true, detail: "Matched" }, { name: "Table row counts", ok: true, detail: "Matched" }] };
    else {
      job.result = { size_bytes: 13_200_000, sha256: "d56fc82bb2477cddd0f1" };
      const owner = projects.find((item) => item.id === job.project_id);
      if (owner) manifests = [manifest(owner.id, owner.name, 0), ...manifests];
    }
  }
  return job;
}

export function mockApi(): Plugin {
  return { name: "pgfy-mock-api", configureServer(server) { server.middlewares.use("/api/v1", async (req, res) => {
    const path = (req.url || "/").split("?")[0]; const method = req.method || "GET";
    if (path === "/setup" && method === "GET") return send(res, 200, { available: true });
    if ((path === "/setup" || path === "/auth/login") && method === "POST") { signedIn = true; return send(res, 204); }
    if (path === "/auth/logout" && method === "POST") { signedIn = false; return send(res, 204); }
    if (path === "/auth/session") return signedIn ? send(res, 200, { email: "admin@example.com", expires_at: now() + 86400, csrf_token: "mock-csrf", client_ip: "198.51.100.18" }) : failure(res, 401, "unauthorized", "Sign in to continue.");
    if (!signedIn) return failure(res, 401, "unauthorized", "Sign in to continue.");
    if (path === "/system/status") return send(res, 200, { ready: true, sqlite: { status: "available", version: "3.49" }, postgres: { status: "available", version: "17.6" }, versions: { application: "0.1.0", worker: "0.1.0" }, backups: storageConfigured ? "configured" : "not configured", database_access: { mode: direct ? "direct" : "tunnel", host: direct ? "db.demo.pgfy.dev" : "127.0.0.1", port: 5432, certificate: { state: direct ? "trusted" : "placeholder", issuer: direct ? "Let's Encrypt" : "", not_after: "2026-12-31" } } });
    if (path === "/settings") return send(res, 200, { id: "demo-installation", hostname: direct ? "demo.pgfy.dev" : "127.0.0.1", origin: direct ? "https://demo.pgfy.dev" : "http://127.0.0.1:8080", mode: direct ? "https" : "tunnel", release: "v0.1.0", caddy_version: "2.10.2", docker_version: "28.3.3", compose_version: "2.39.2" });
    if (path === "/settings/storage" && method === "GET") return send(res, 200, { configured: storageConfigured, settings: storage });
    if (path === "/settings/storage" && method === "PUT") { storage = { ...storage, ...(await body(req)) }; storageConfigured = true; return send(res, 200, { configured: true, settings: storage }); }
    if (path === "/settings/storage/check" && method === "POST") return send(res, 200, { ok: true, steps: ["Upload test file", "List files", "Download file", "Clean up"].map((name) => ({ name, ok: true })) });
    updateProjectStates();
    if (path === "/projects" && method === "GET") return send(res, 200, { projects, database_access: {} });
    if (path === "/projects" && method === "POST") { const input = await body(req) as { name?: string }; const next = project(`prj_${Math.random().toString(36).slice(2, 10)}`, input.name || "new database", "identity_persisted", false, 0); projects = [next, ...projects]; return send(res, 202, next); }
    const retry = path.match(/^\/projects\/([^/]+)\/retry$/); if (retry && method === "POST") { projects = projects.map((item) => item.id === retry[1] ? { ...item, failed: false, stage_error: "", stage: "ready", ready_at: now(), size_bytes: 0 } : item); return send(res, 204); }
    const credentials = path.match(/^\/projects\/([^/]+)\/credentials$/); if (credentials) { const item = projects.find((p) => p.id === credentials[1]); if (!item) return failure(res, 404, "not_found", "Database not found."); const password = "demo-password-not-for-production"; const host = direct ? "db.demo.pgfy.dev" : "127.0.0.1"; const sslmode = direct ? "verify-full" : "disable"; const url = `postgresql://app_${item.name}:${password}@${host}:5432/app_${item.name}?sslmode=${sslmode}`; return send(res, 200, { host, port: 5432, database: `app_${item.name}`, user: `app_${item.name}`, password, sslmode, url, psql: `psql \"${url}${direct ? "&sslrootcert=system" : ""}\"` }); }
    const access = path.match(/^\/projects\/([^/]+)\/access$/); if (access && method === "PUT") { const input = await body(req) as { revision: number; addresses: string[] }; const item = projects.find((p) => p.id === access[1]); if (!item) return failure(res, 404, "not_found", "Database not found."); if (input.revision !== item.policy.current_revision) return failure(res, 409, "revision_conflict", "The access rules changed elsewhere. Reload and try again."); item.policy = { ...item.policy, current_revision: input.revision + 1, addresses: input.addresses, state: "pending" }; setTimeout(() => { item.policy.state = "applied"; item.policy.applied_revision = item.policy.current_revision; }, 2000); return send(res, 200, item.policy); }
    const checks = path.match(/^\/projects\/([^/]+)\/connection-checks(?:\/([^/]+))?$/); if (checks && method === "POST") { const check = { id: `chk_${Date.now()}`, state: "pending", expires_at: now() + 600, created_at: now(), command: "psql \"postgresql://…\" -c \"select pg_sleep(20)\"" }; return send(res, 201, check); } if (checks && method === "GET") { const id = checks[2] || "check"; const created = Number(id.split("_")[1] || Date.now()); const success = Date.now() - created > 5000; return send(res, 200, { id, state: success ? "successful" : "pending", expires_at: now() + 590, created_at: Math.floor(created / 1000), evidence: success ? { client_addr: "203.0.113.42", tls: true, observed_at: now() } : undefined }); }
    const projectBackups = path.match(/^\/projects\/([^/]+)\/backups$/); if (projectBackups && method === "GET") { const id = projectBackups[1]; const backups = manifests.filter((m) => m.project_id === id).map((m, index) => ({ id: `backup_${index}`, project_id: id, job_id: `old_${index}`, object_key: m.archive_key, manifest: m, size_bytes: m.size_bytes, created_at: Date.parse(m.created_at) / 1000 })); return send(res, 200, { backups, jobs: [...jobs.values()].filter((j) => j.project_id === id).map(tick), storage_configured: storageConfigured, next_scheduled_at: backups[0] ? backups[0].created_at + 86400 : 0 }); }
    if (projectBackups && method === "POST") { if (!storageConfigured) return failure(res, 409, "storage_not_configured", "Configure backup storage first."); const job: MockJob = { id: `job_${Date.now()}`, kind: "backup", project_id: projectBackups[1], state: "queued", stage: "queued", created_at: now(), started_at: now(), finished_at: 0, stage_at: now(), elapsed_seconds: 0, error: "", result: {} }; jobs.set(job.id, job); return send(res, 202, job); }
    const detail = path.match(/^\/projects\/([^/]+)$/); if (detail) { const item = projects.find((p) => p.id === detail[1]); return item ? send(res, 200, { project: item, database_access: { mode: direct ? "direct" : "tunnel", host: direct ? "db.demo.pgfy.dev" : "127.0.0.1", port: 5432, certificate: { state: direct ? "trusted" : "placeholder", issuer: direct ? "Let's Encrypt" : "" } } }) : failure(res, 404, "not_found", "Database not found."); }
    if (path === "/recovery/backups" && method === "GET") return send(res, 200, storageConfigured ? { state: "ok", backups: manifests, installation_id: "demo-installation", busy: [...jobs.values()].some((j) => tick(j).state === "running"), restores: [...jobs.values()].filter((j) => j.kind === "restore").map(tick) } : { state: "storage_not_configured", backups: [], restores: [] });
    if (path === "/recovery/restores" && method === "POST") { const input = await body(req) as { manifest_key: string; name: string }; const restored = project(`prj_${Math.random().toString(36).slice(2, 10)}`, input.name, "ready", false, 0); projects = [restored, ...projects]; const job: MockJob = { id: `job_${Date.now()}`, kind: "restore", project_id: restored.id, state: "queued", stage: "queued", created_at: now(), started_at: now(), finished_at: 0, stage_at: now(), elapsed_seconds: 0, error: "", result: {} }; jobs.set(job.id, job); return send(res, 202, { project: restored, job }); }
    const jobPath = path.match(/^\/jobs\/([^/]+)$/); if (jobPath) { const job = jobs.get(jobPath[1]); return job ? send(res, 200, tick(job)) : failure(res, 404, "not_found", "Job not found."); }
    return failure(res, 404, "not_found", `No mock route for ${method} ${path}`);
  }); } };
}
