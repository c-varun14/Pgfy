import { useEffect, useState, type FormEvent } from "react";
import { ChevronRight, Database, Plus, TriangleAlert } from "lucide-react";
import { api, type Manifest, type Project, type Status, type StorageSettings } from "../api";
import { formatBytes, relativeTime } from "../lib/format";
import { PageHeader } from "../components/PageHeader";
import { Banner, ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Dialog } from "../components/ui/dialog";
import { EmptyState } from "../components/ui/empty-state";
import { Field } from "../components/ui/field";
import { Pill } from "../components/ui/pill";
import { Skeleton } from "../components/ui/skeleton";

export function databaseStage(project: Project) {
  if (project.failed) return { tone: "bad" as const, label: "Needs attention" };
  if (project.stage === "ready") return { tone: "good" as const, label: "Ready" };
  return { tone: "wait" as const, label: "Setting up…" };
}

export function DatabasesPage({ status, navigate }: { status: Status | null; navigate: (to: string) => void }) {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [storageConfigured, setStorageConfigured] = useState<boolean | null>(null);
  const [manifests, setManifests] = useState<Manifest[] | null>(null);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function loadProjects() {
    try { setProjects((await api<{ projects: Project[] }>("/projects")).projects); setError(""); }
    catch (failure) { setError((failure as Error).message); }
  }
  useEffect(() => {
    void loadProjects();
    void api<{ configured: boolean; settings: StorageSettings }>("/settings/storage").then((body) => setStorageConfigured(body.configured)).catch(() => setStorageConfigured(null));
    void api<{ backups: Manifest[] }>("/recovery/backups").then((body) => setManifests(body.backups || [])).catch(() => setManifests(null));
  }, []);
  const pending = projects?.some((project) => !project.failed && project.stage !== "ready");
  useEffect(() => { if (!pending) return; const timer = setInterval(() => void loadProjects(), 2000); return () => clearInterval(timer); }, [pending]);

  async function create(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      const created = await api<Project>("/projects", { method: "POST", body: JSON.stringify({ name: name.trim(), idempotency_key: crypto.randomUUID() }) });
      setCreating(false); setName(""); navigate(`/projects/${created.id}`);
    } catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }

  const latest = new Map<string, Manifest>();
  manifests?.forEach((item) => { const current = latest.get(item.project_id); if (!current || Date.parse(item.created_at) > Date.parse(current.created_at)) latest.set(item.project_id, item); });
  return <>
    <PageHeader title="Databases" actions={<Button onClick={() => setCreating(true)}><Plus size={16} />New database</Button>} />
    {status && !status.ready && <Banner tone="warn"><TriangleAlert size={18} /><div><h2>Your server needs attention</h2><p>PostgreSQL isn't responding. Run <code>pgfyctl diagnostics</code> on the server.</p></div></Banner>}
    {projects && projects.length > 0 && storageConfigured === false && <Banner><span>Backups are off — your databases aren't protected against server loss. <button className="link" onClick={() => navigate("/backups")}>Set up backups →</button></span></Banner>}
    {error && <ErrorNotice message={error} />}
    {projects === null ? !error && <Skeleton lines={4} /> : projects.length === 0 ? <EmptyState icon={<Database size={28} />} title="No databases yet" action={<Button onClick={() => setCreating(true)}><Plus size={16} />New database</Button>}>Create your first database. It will be ready in a few seconds.</EmptyState> :
      <div className="database-table" role="table" aria-label="Databases">
        <div className="database-row database-head" role="row"><span>Name</span><span>Status</span><span>Size</span><span>Last backup</span><span>Connections</span><span>Created</span><span /></div>
        {projects.map((project) => { const stage = databaseStage(project); const backup = latest.get(project.id); const connections = project.connections_now?.length || 0; return <button type="button" className="database-row" role="row" key={project.id} onClick={() => navigate(`/projects/${project.id}`)}>
          <strong role="cell">{project.name}</strong><span role="cell"><Pill tone={stage.tone}>{stage.label}</Pill></span><span role="cell" data-label="Size">{project.stage === "ready" ? formatBytes(project.size_bytes) : "—"}</span><span role="cell" data-label="Last backup">{storageConfigured === false ? "Off" : manifests === null ? "—" : backup ? relativeTime(Date.parse(backup.created_at) / 1000) : "Never"}</span><span role="cell" data-label="Connections">{connections ? `${connections} live` : "—"}</span><span role="cell" data-label="Created">{relativeTime(project.created_at)}</span><ChevronRight size={16} aria-hidden="true" />
        </button>; })}
      </div>}
    <Dialog open={creating} onClose={() => setCreating(false)} title="New database">
      <form className="dialog-form" onSubmit={(event) => void create(event)}>
        <Field label="Name" htmlFor="database-name" hint="Pgfy creates the database, a dedicated user and a strong password."><input id="database-name" value={name} onChange={(event) => setName(event.target.value)} placeholder="e.g. Shop" maxLength={64} autoFocus required /></Field>
        <div className="actions"><Button type="submit" loading={busy} disabled={!name.trim()}>{busy ? "Creating…" : "Create database"}</Button><Button type="button" variant="ghost" onClick={() => setCreating(false)}>Cancel</Button></div>
      </form>
    </Dialog>
  </>;
}
