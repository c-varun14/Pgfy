import { Archive, ChevronDown, Database, RotateCcw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, type BucketBackup, type DatabaseBackups, type Discovery, type Manifest, type Project, type StorageSettings } from "../api";
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

type Group = { key: string; name: string; dbName: string; project: Project | null; foreign: boolean; discovered: DatabaseBackups | null; backups: BucketBackup[]; latest: BucketBackup | null; totalBytes: number };

/** A discovered folder belongs to a local database only when the installation,
 *  the project and the database name all agree; anything else stays separate. */
function matches(discovered: DatabaseBackups, project: Project, installationID?: string) {
  return discovered.db_name === project.db_name && !discovered.mixed && discovered.project_id === project.id &&
    (!discovered.installation_id || discovered.installation_id === installationID);
}

function buildGroups(projects: Project[] | null, discovery: Discovery | null): { local: Group[]; foreign: Group[] } {
  const toGroup = (key: string, name: string, dbName: string, project: Project | null, foreign: boolean, discovered: DatabaseBackups | null): Group => {
    const backups = discovered?.backups || [];
    return { key, name, dbName, project, foreign, discovered, backups, latest: backups[0] || null, totalBytes: discovered?.total_bytes || 0 };
  };
  const local: Group[] = [];
  const foreign: Group[] = [];
  const claimed = new Set<string>();
  // Databases with no backups yet still get a card: those need attention most.
  projects?.forEach((project) => {
    const discovered = discovery?.databases.find((item) => matches(item, project, discovery.installation_id)) || null;
    if (discovered) claimed.add(discovered.db_name);
    local.push(toGroup(project.id, project.name, project.db_name, project, false, discovered));
  });
  discovery?.databases.forEach((item) => {
    if (claimed.has(item.db_name) || item.count === 0) return;
    foreign.push(toGroup(item.db_name, item.project_name || item.db_name, item.db_name, null, true, item));
  });
  const latestFirst = (a: Group, b: Group) => (b.latest?.taken_at || 0) - (a.latest?.taken_at || 0) || a.name.localeCompare(b.name);
  local.sort(latestFirst); foreign.sort(latestFirst);
  return { local, foreign };
}

/** Manifests are re-read when a restore starts; the list only needs identity. */
function asManifest(backup: BucketBackup): Manifest {
  return { version: 1, installation_id: backup.installation_id, project_id: backup.project_id, project_name: backup.project_name, db_name: backup.db_name, postgres_version: backup.postgres_version, created_at: new Date(backup.taken_at * 1000).toISOString(), archive_key: backup.archive_key, sha256: "", size_bytes: backup.size_bytes, tables: [], manifest_key: backup.manifest_key };
}

export function BackupsPage({ navigate }: { navigate: (to: string) => void }) {
  const [storage, setStorage] = useState<{ configured: boolean; settings: StorageSettings } | null>(null); const [discovery, setDiscovery] = useState<Discovery | null>(null); const [projects, setProjects] = useState<Project[] | null>(null);
  const [policy, setPolicy] = useState<{ target_interval_hours: number } | null>(null);
  const [error, setError] = useState(""); const [editing, setEditing] = useState(false); const [selected, setSelected] = useState<Manifest | null>(null); const [expanded, setExpanded] = useState<string | null>(null);
  const [running, setRunning] = useState<Map<string, number>>(new Map()); const [cardErrors, setCardErrors] = useState<Map<string, string>>(new Map()); const { showToast } = useToast();
  async function load() {
    const results = await Promise.allSettled([api<{ configured: boolean; settings: StorageSettings }>("/settings/storage"), api<Discovery>("/recovery/backups"), api<{ projects: Project[] }>("/projects"), api<{ target_interval_hours: number }>("/settings/backups")]);
    if (results[0].status === "fulfilled") setStorage(results[0].value); else { setStorage({ configured: false, settings: EMPTY_STORAGE }); setError(results[0].reason instanceof Error ? results[0].reason.message : "Backup storage is unavailable."); }
    if (results[1].status === "fulfilled") setDiscovery(results[1].value); else { setDiscovery({ state: "ok", storage_error: results[1].reason instanceof Error ? results[1].reason.message : "Backups could not be listed.", databases: [] }); if (results[0].status === "fulfilled") setError(results[1].reason instanceof Error ? results[1].reason.message : "Backups could not be listed."); }
    if (results[2].status === "fulfilled") setProjects(results[2].value.projects); else setProjects((current) => current || []);
    if (results[3].status === "fulfilled") setPolicy(results[3].value);
  }
  useEffect(() => { void load(); }, []);
  // Poll while a job is active anywhere, or while a "Back up now" we started hasn't produced a backup yet.
  const polling = !!discovery?.busy || running.size > 0 || discovery?.state === "checking";
  useEffect(() => { if (!polling) return; const timer = setInterval(() => void load(), 2000); return () => clearInterval(timer); }, [polling]);
  useEffect(() => {
    if (!discovery || running.size === 0) return;
    const next = new Map(running);
    running.forEach((startedAt, projectId) => { const fresh = discovery.databases.some((group) => group.project_id === projectId && group.newest_at >= startedAt); if (fresh || !discovery.busy) next.delete(projectId); });
    if (next.size !== running.size) setRunning(next);
  }, [discovery]);

  async function backupNow(project: Project) {
    setCardErrors((current) => { const next = new Map(current); next.delete(project.id); return next; });
    try { await api(`/projects/${project.id}/backups`, { method: "POST", body: "{}" }); setRunning((current) => new Map(current).set(project.id, Math.floor(Date.now() / 1000) - 5)); showToast(`Backing up ${project.name}`); await load(); }
    catch (failure) { setCardErrors((current) => new Map(current).set(project.id, (failure as Error).message)); }
  }

  const groups = useMemo(() => buildGroups(projects, discovery), [projects, discovery]);
  const showForm = storage && (!storage.configured || editing || !!discovery?.storage_error);
  const hasAnything = groups.local.length > 0 || groups.foreign.length > 0;
  const interval = policy?.target_interval_hours ?? 24;
  const intervalLabel = interval === 1 ? "Hourly" : interval === 24 ? "Daily" : `Every ${interval} hours`;

  function ageLabel(group: Group) {
    if (!group.latest) return null;
    const stale = Date.now() / 1000 - group.latest.taken_at > interval * 3600 * 1.5;
    return <span className={stale ? "backup-age stale" : "backup-age"}>Newest recoverable backup <time title={formatDateTime(group.latest.taken_at)}>{relativeTime(group.latest.taken_at)}</time>{stale ? ` · older than the ${intervalLabel.toLowerCase()} target` : ""}</span>;
  }

  function renderGroup(group: Group) {
    const isRunning = running.has(group.key); const cardError = cardErrors.get(group.key); const discovered = group.discovered;
    return <details className="backup-card" key={group.key} open={expanded === group.key}>
      <summary onClick={(event) => { event.preventDefault(); if (group.backups.length > 0) setExpanded(expanded === group.key ? null : group.key); }} aria-expanded={expanded === group.key}>
        <span className="section-icon"><Database size={20} /></span>
        <span className="backup-card-title"><strong>{group.name}</strong><small className="mono">{group.dbName}</small></span>
        <span className="backup-card-summary">
          {group.latest ? <>{ageLabel(group)}<span>· {discovered?.count ?? group.backups.length} backup{(discovered?.count ?? group.backups.length) === 1 ? "" : "s"}</span><span>· {formatBytes(group.totalBytes)}</span></> : group.project ? (group.project.failed ? "Database needs attention" : group.project.stage === "ready" ? `No backups yet · ${intervalLabel.toLowerCase()} backups start within a few minutes` : "Database is still being set up") : "No backups"}
          {group.foreign && <Pill tone="neutral">From another server</Pill>}
          {discovered?.mixed && <Pill tone="neutral">Several servers use this folder</Pill>}
          {!!discovered?.manifest_only && <Pill tone="wait">{discovered.manifest_only} incomplete</Pill>}
          {!!discovered?.damaged && <Pill tone="bad">{discovered.damaged} unreadable</Pill>}
          {isRunning && <Pill tone="wait">Backing up…</Pill>}
        </span>
        <span className="backup-card-actions" onClick={(event) => event.stopPropagation()}>
          {group.project && group.project.stage === "ready" && !group.project.failed && <Button size="sm" variant="secondary" loading={isRunning} disabled={!!discovery?.busy && !isRunning} onClick={() => void backupNow(group.project!)}><Archive size={14} />Back up now</Button>}
          {group.latest && <Button size="sm" variant="secondary" onClick={() => setSelected(asManifest(group.latest!))}><RotateCcw size={14} />Restore latest</Button>}
        </span>
        {group.backups.length > 0 ? <ChevronDown size={16} className="backup-card-chevron" aria-hidden="true" /> : <span />}
      </summary>
      {cardError && <p className="field-error" role="alert">{cardError}</p>}
      {group.backups.length > 0 && <div className="backup-rows">{group.backups.map((backup) => <div className="backup-row" key={backup.manifest_key}><span><strong><time title={formatDate(backup.taken_at)}>{relativeTime(backup.taken_at)}</time></strong><small>{formatDateTime(backup.taken_at)} · {formatBytes(backup.size_bytes)} · {backup.table_count} table{backup.table_count === 1 ? "" : "s"} · PostgreSQL {backup.postgres_version.split(" ")[0]}{discovery?.installation_id && backup.installation_id !== discovery.installation_id ? " · from another server" : ""}</small></span><Button variant="secondary" size="sm" onClick={() => setSelected(asManifest(backup))}><RotateCcw size={14} />Restore</Button></div>)}
        {discovered?.has_more && <p className="caption">Showing the newest {group.backups.length} of {discovered.count} backups of this database.</p>}
      </div>}
    </details>;
  }

  return <>
    <PageHeader title="Backups" description="Backups of every database to a bucket you own. Restore any of them here — on this server or a new one." />
    {error && !discovery?.storage_error && <ErrorNotice message={error} />}
    {!storage || !discovery || projects === null ? !error && <Skeleton lines={4} /> : <>
      <Card><CardHeader title={storage.configured ? "Backup storage" : "Set up backup storage"} aside={storage.configured && <><Pill tone="good">Storage connected · {storage.settings.bucket}</Pill><Button size="sm" variant="secondary" onClick={() => setEditing(!editing)}>{editing ? "Close" : "Edit storage"}</Button></>} />
        {!storage.configured && <><h2>Backup storage</h2><ul className="feature-list"><li>Runs on a schedule for every database</li><li>Uses your own S3-compatible bucket</li><li>Restores on this server or a new one</li></ul></>}
        {discovery.storage_error && <ErrorNotice message={`Backups could not be listed: ${discovery.storage_error}`} />}
        {showForm && <StorageForm initial={storage.settings} configured={storage.configured} onSaved={(settings) => { setStorage({ configured: true, settings }); setEditing(false); setError(""); void load(); }} />}
      </Card>
      {storage.configured && discovery.state === "checking" && <Card><p>Reading your bucket to see which backups can be restored…</p></Card>}
      {storage.configured && discovery.state !== "storage_not_configured" && (!hasAnything ? <EmptyState icon={<EmptyArt />} title="No backups yet">Your first backup will appear here.</EmptyState> : <>
        {groups.local.length > 0 && <div className="backup-cards" aria-label="Backups by database">{groups.local.map(renderGroup)}</div>}
        {groups.foreign.length > 0 && <><h2 className="backup-section-title">From other servers</h2><div className="backup-cards" aria-label="Backups from other servers">{groups.foreign.map(renderGroup)}</div></>}
        {!!discovery.reconciled_at && <p className="caption">Bucket last read <time title={formatDateTime(discovery.reconciled_at)}>{relativeTime(discovery.reconciled_at)}</time>.</p>}
      </>)}
      {discovery.restores && discovery.restores.length > 0 && <details className="previous-restores"><summary>Previous restores</summary><Card><div className="job-rows">{discovery.restores.map((job) => <div className="job-row" key={job.id}><Pill tone={jobTone(job)}>{jobLabel(job)}</Pill><span><strong>{formatDateTime(job.created_at)}</strong><small>{job.error || job.result.summary || STAGE_LABELS[job.stage] || job.stage}</small></span>{job.result.project_id && job.state === "succeeded" && <Button variant="ghost" size="sm" onClick={() => navigate(`/projects/${job.result.project_id}`)}>Open</Button>}</div>)}</div></Card></details>}
    </>}
    <RestoreDialog manifest={selected} open={!!selected} busy={discovery?.busy} onClose={() => setSelected(null)} navigate={navigate} onComplete={() => void load()} />
  </>;
}
