import { useEffect, useState, type FormEvent } from "react";
import { ArrowRight, Database, Plus, TriangleAlert } from "lucide-react";
import { api, type Manifest, type Project, type Status, type StorageSettings } from "../api";
import { formatBytes, formatDate, relativeTime } from "../lib/format";
import { PageHeader } from "../components/PageHeader";
import { Banner, ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Dialog } from "../components/ui/dialog";
import { EmptyState } from "../components/ui/empty-state";
import { Field } from "../components/ui/field";
import { Pill } from "../components/ui/pill";
import { Skeleton } from "../components/ui/skeleton";
import { EmptyArt } from "../components/ui/empty-art";

export function databaseStage(project: Project) {
  if (project.failed) return { tone: "bad" as const, label: "Needs attention" };
  if (project.stage === "ready" && project.frozen_at) return { tone: "neutral" as const, label: "Writes frozen" };
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
  useEffect(() => { const onKey = (event: KeyboardEvent) => { if (event.key === "n" && !event.metaKey && !event.ctrlKey && !event.altKey && !(event.target instanceof HTMLInputElement) && !(event.target instanceof HTMLTextAreaElement) && !creating) { event.preventDefault(); setCreating(true); } }; addEventListener("keydown", onKey); return () => removeEventListener("keydown", onKey); }, [creating]);

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
    <PageHeader title="Databases" actions={<Button onClick={() => setCreating(true)}><Plus size={16} />New database<kbd>N</kbd></Button>} />
    {status && !status.ready && <Banner tone="warn"><TriangleAlert size={18} /><div><h2>Your server needs attention</h2><p>PostgreSQL isn't responding. Run <code>pgfyctl diagnostics</code> on the server.</p></div></Banner>}
    {projects && projects.length > 0 && storageConfigured === false && <Banner><span>Backups are off — your databases aren't protected against server loss. <button className="link" onClick={() => navigate("/backups")}>Set up backups →</button></span></Banner>}
    {error && <ErrorNotice message={error} />}
    {projects === null ? !error && <Skeleton lines={3} className="skeleton-cards" /> : projects.length === 0 ? <EmptyState icon={<EmptyArt />} title="No databases yet" action={<Button onClick={() => setCreating(true)}><Plus size={16} />New database</Button>}>Create your first database. It will be ready in a few seconds.</EmptyState> :
      <div className="project-grid" role="list" aria-label="Databases">
        {projects.map((project) => { const stage = databaseStage(project); const backup = latest.get(project.id); const connections = project.connections_now?.length || 0; return <button type="button" role="listitem" className="project-card" key={project.id} onClick={() => navigate(`/projects/${project.id}`)}>
          <div className="project-card-top"><span className="section-icon"><Database size={20} /></span><Pill tone={stage.tone}>{stage.label}</Pill></div>
          <h2>{project.name}</h2>
          <p className="mono">{project.db_name}</p>
          <dl className="project-card-stats">
            <div><dt>Size</dt><dd>{project.stage === "ready" ? formatBytes(project.size_bytes) : "—"}</dd></div>
            <div><dt>Last backup</dt><dd>{storageConfigured === false ? "Off" : manifests === null ? "—" : backup ? <time title={formatDate(Date.parse(backup.created_at) / 1000)}>{relativeTime(Date.parse(backup.created_at) / 1000)}</time> : "Never"}</dd></div>
            <div><dt>Connections</dt><dd>{connections ? `${connections} live` : "—"}</dd></div>
          </dl>
          <div className="project-card-meta"><span>Created <time title={formatDate(project.created_at)}>{relativeTime(project.created_at)}</time></span><ArrowRight size={15} aria-hidden="true" /></div>
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
