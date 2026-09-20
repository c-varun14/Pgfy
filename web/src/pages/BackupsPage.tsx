import { Archive, ChevronDown, Database, RotateCcw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, type Job, type Manifest, type Project, type StorageSettings } from "../api";
import { formatBytes, formatDate, formatDateTime, relativeTime, STAGE_LABELS } from "../lib/format";
import { PageHeader } from "../components/PageHeader";
import { ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Card, CardHeader } from "../components/ui/card";
import { EmptyState } from "../components/ui/empty-state";
import { EmptyArt } from "../components/ui/empty-art";
import { Pill } from "../components/ui/pill";
import { Skeleton } from "../components/ui/skeleton";
import { useToast } from "../components/ui/toast";
import { StorageForm, EMPTY_STORAGE } from "./backups/StorageForm";
import { RestoreDialog } from "./backups/RestoreDialog";
import { jobLabel, jobTone } from "./database/BackupsTab";

type Discovery = { state: "ok" | "storage_not_configured" | "storage_error"; error?: string; backups: Manifest[]; installation_id?: string; busy?: boolean; restores?: Job[] };
type Group = { key: string; name: string; dbName: string; project: Project | null; foreign: boolean; backups: Manifest[]; latest: Manifest | null; totalBytes: number };

function buildGroups(projects: Project[] | null, discovery: Discovery | null): { local: Group[]; foreign: Group[] } {
  const byProject = new Map<string, Manifest[]>();
  discovery?.backups.forEach((backup) => byProject.set(backup.project_id, [...(byProject.get(backup.project_id) || []), backup]));
  const toGroup = (key: string, name: string, dbName: string, project: Project | null, foreign: boolean, backups: Manifest[]): Group => {
    const sorted = [...backups].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
    return { key, name, dbName, project, foreign, backups: sorted, latest: sorted[0] || null, totalBytes: sorted.reduce((sum, item) => sum + item.size_bytes, 0) };
  };
  const local: Group[] = [];
  const foreign: Group[] = [];
  const seen = new Set<string>();
  projects?.forEach((project) => { seen.add(project.id); local.push(toGroup(project.id, project.name, project.db_name, project, false, byProject.get(project.id) || [])); });
  byProject.forEach((backups, id) => { if (seen.has(id)) return; const first = backups[0]; foreign.push(toGroup(id, first.project_name, first.db_name, null, true, backups)); });
  const latestFirst = (a: Group, b: Group) => (b.latest ? Date.parse(b.latest.created_at) : 0) - (a.latest ? Date.parse(a.latest.created_at) : 0) || a.name.localeCompare(b.name);
  local.sort(latestFirst); foreign.sort(latestFirst);
  return { local, foreign };
}

export function BackupsPage({ navigate }: { navigate: (to: string) => void }) {
  const [storage, setStorage] = useState<{ configured: boolean; settings: StorageSettings } | null>(null); const [discovery, setDiscovery] = useState<Discovery | null>(null); const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState(""); const [editing, setEditing] = useState(false); const [selected, setSelected] = useState<Manifest | null>(null); const [expanded, setExpanded] = useState<string | null>(null);
  const [running, setRunning] = useState<Map<string, number>>(new Map()); const [cardErrors, setCardErrors] = useState<Map<string, string>>(new Map()); const { showToast } = useToast();
  async function load() {
    const results = await Promise.allSettled([api<{ configured: boolean; settings: StorageSettings }>("/settings/storage"), api<Discovery>("/recovery/backups"), api<{ projects: Project[] }>("/projects")]);
    if (results[0].status === "fulfilled") setStorage(results[0].value); else { setStorage({ configured: false, settings: EMPTY_STORAGE }); setError(results[0].reason instanceof Error ? results[0].reason.message : "Backup storage is unavailable."); }
    if (results[1].status === "fulfilled") setDiscovery(results[1].value); else { setDiscovery({ state: "storage_error", error: results[1].reason instanceof Error ? results[1].reason.message : "Backups could not be listed.", backups: [] }); if (results[0].status === "fulfilled") setError(results[1].reason instanceof Error ? results[1].reason.message : "Backups could not be listed."); }
    if (results[2].status === "fulfilled") setProjects(results[2].value.projects); else setProjects((current) => current || []);
  }
  useEffect(() => { void load(); }, []);
  // Poll while a job is active anywhere, or while a "Back up now" we started hasn't produced a manifest yet.
  const polling = !!discovery?.busy || running.size > 0;
  useEffect(() => { if (!polling) return; const timer = setInterval(() => void load(), 2000); return () => clearInterval(timer); }, [polling]);
  useEffect(() => {
    if (!discovery || running.size === 0) return;
    const next = new Map(running);
    running.forEach((startedAt, projectId) => { const fresh = discovery.backups.some((backup) => backup.project_id === projectId && Date.parse(backup.created_at) / 1000 >= startedAt); if (fresh || !discovery.busy) next.delete(projectId); });
    if (next.size !== running.size) setRunning(next);
  }, [discovery]);

  async function backupNow(project: Project) {
    setCardErrors((current) => { const next = new Map(current); next.delete(project.id); return next; });
    try { await api(`/projects/${project.id}/backups`, { method: "POST", body: "{}" }); setRunning((current) => new Map(current).set(project.id, Math.floor(Date.now() / 1000) - 5)); showToast(`Backing up ${project.name}`); await load(); }
    catch (failure) { setCardErrors((current) => new Map(current).set(project.id, (failure as Error).message)); }
  }

  const groups = useMemo(() => buildGroups(projects, discovery), [projects, discovery]);
  const showForm = storage && (!storage.configured || editing || discovery?.state === "storage_error");
  const hasAnything = groups.local.length > 0 || groups.foreign.length > 0;

  function renderGroup(group: Group) {
    const latestSeconds = group.latest ? Date.parse(group.latest.created_at) / 1000 : 0; const isRunning = running.has(group.key); const cardError = cardErrors.get(group.key);
    return <details className="backup-card" key={group.key} open={expanded === group.key}>
      <summary onClick={(event) => { event.preventDefault(); if (group.backups.length > 0) setExpanded(expanded === group.key ? null : group.key); }} aria-expanded={expanded === group.key}>
        <span className="section-icon"><Database size={20} /></span>
        <span className="backup-card-title"><strong>{group.name}</strong><small className="mono">{group.dbName}</small></span>
        <span className="backup-card-summary">
          {group.latest ? <><span>Last backup <time title={formatDateTime(latestSeconds)}>{relativeTime(latestSeconds)}</time></span><span>· {group.backups.length} backup{group.backups.length === 1 ? "" : "s"}</span><span>· {formatBytes(group.totalBytes)}</span></> : group.project ? (group.project.failed ? "Database needs attention" : group.project.stage === "ready" ? "No backups yet · first daily backup within a few minutes" : "Database is still being set up") : "No backups"}
          {group.foreign && <Pill tone="neutral">From another server</Pill>}
          {isRunning && <Pill tone="wait">Backing up…</Pill>}
        </span>
        <span className="backup-card-actions" onClick={(event) => event.stopPropagation()}>
          {group.project && group.project.stage === "ready" && !group.project.failed && <Button size="sm" variant="secondary" loading={isRunning} disabled={!!discovery?.busy && !isRunning} onClick={() => void backupNow(group.project!)}><Archive size={14} />Back up now</Button>}
          {group.latest && <Button size="sm" variant="secondary" onClick={() => setSelected(group.latest)}><RotateCcw size={14} />Restore latest</Button>}
        </span>
        {group.backups.length > 0 ? <ChevronDown size={16} className="backup-card-chevron" aria-hidden="true" /> : <span />}
      </summary>
      {cardError && <p className="field-error" role="alert">{cardError}</p>}
      {group.backups.length > 0 && <div className="backup-rows">{group.backups.map((backup) => { const seconds = Date.parse(backup.created_at) / 1000; return <div className="backup-row" key={backup.manifest_key} title={`sha256 ${backup.sha256}`}><span><strong><time title={formatDate(seconds)}>{relativeTime(seconds)}</time></strong><small>{formatDateTime(seconds)} · {formatBytes(backup.size_bytes)} · {backup.tables.length} table{backup.tables.length === 1 ? "" : "s"} · PostgreSQL {backup.postgres_version.split(" ")[0]}{discovery?.installation_id && backup.installation_id !== discovery.installation_id ? " · from another server" : ""}</small></span><Button variant="secondary" size="sm" onClick={() => setSelected(backup)}><RotateCcw size={14} />Restore</Button></div>; })}</div>}
    </details>;
  }

  return <>
    <PageHeader title="Backups" description="Daily backups of every database to a bucket you own. Restore any of them here — on this server or a new one." />
    {error && discovery?.state !== "storage_error" && <ErrorNotice message={error} />}
    {!storage || !discovery || projects === null ? !error && <Skeleton lines={4} /> : <>
      <Card><CardHeader title={storage.configured ? "Backup storage" : "Set up backup storage"} aside={storage.configured && <><Pill tone="good">Storage connected · {storage.settings.bucket}</Pill><Button size="sm" variant="secondary" onClick={() => setEditing(!editing)}>{editing ? "Close" : "Edit storage"}</Button></>} />
        {!storage.configured && <><h2>Backup storage</h2><ul className="feature-list"><li>Runs daily for every database</li><li>Uses your own S3-compatible bucket</li><li>Restores on this server or a new one</li></ul></>}
        {discovery.state === "storage_error" && <ErrorNotice message={`Backups could not be listed: ${discovery.error || "Check your storage settings."}`} />}
        {showForm && <StorageForm initial={storage.settings} configured={storage.configured} onSaved={(settings) => { setStorage({ configured: true, settings }); setEditing(false); setError(""); void load(); }} />}
      </Card>
      {storage.configured && discovery.state !== "storage_not_configured" && (!hasAnything ? <EmptyState icon={<EmptyArt />} title="No backups yet">Your first daily backup will appear here.</EmptyState> : <>
        {groups.local.length > 0 && <div className="backup-cards" aria-label="Backups by database">{groups.local.map(renderGroup)}</div>}
        {groups.foreign.length > 0 && <><h2 className="backup-section-title">From other servers</h2><div className="backup-cards" aria-label="Backups from other servers">{groups.foreign.map(renderGroup)}</div></>}
      </>)}
      {discovery.restores && discovery.restores.length > 0 && <details className="previous-restores"><summary>Previous restores</summary><Card><div className="job-rows">{discovery.restores.map((job) => <div className="job-row" key={job.id}><Pill tone={jobTone(job)}>{jobLabel(job)}</Pill><span><strong>{formatDateTime(job.created_at)}</strong><small>{job.error || (job.result.verified ? "All verification checks passed" : STAGE_LABELS[job.stage] || job.stage)}</small></span>{job.result.project_id && job.state === "succeeded" && <Button variant="ghost" size="sm" onClick={() => navigate(`/projects/${job.result.project_id}`)}>Open</Button>}</div>)}</div></Card></details>}
    </>}
    <RestoreDialog manifest={selected} open={!!selected} busy={discovery?.busy} onClose={() => setSelected(null)} navigate={navigate} onComplete={() => void load()} />
  </>;
}
