import { Archive, Database, RotateCcw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, type Job, type Manifest, type StorageSettings } from "../api";
import { formatBytes, formatDate, formatDateTime, relativeTime, STAGE_LABELS } from "../lib/format";
import { PageHeader } from "../components/PageHeader";
import { ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Card, CardHeader } from "../components/ui/card";
import { EmptyState } from "../components/ui/empty-state";
import { EmptyArt } from "../components/ui/empty-art";
import { Pill } from "../components/ui/pill";
import { Skeleton } from "../components/ui/skeleton";
import { StorageForm, EMPTY_STORAGE } from "./backups/StorageForm";
import { RestoreDialog } from "./backups/RestoreDialog";
import { jobLabel, jobTone } from "./database/BackupsTab";

type Discovery = { state: "ok" | "storage_not_configured" | "storage_error"; error?: string; backups: Manifest[]; installation_id?: string; busy?: boolean; restores?: Job[] };
export function BackupsPage({ navigate }: { navigate: (to: string) => void }) {
  const [storage, setStorage] = useState<{ configured: boolean; settings: StorageSettings } | null>(null); const [discovery, setDiscovery] = useState<Discovery | null>(null); const [error, setError] = useState(""); const [editing, setEditing] = useState(false); const [selected, setSelected] = useState<Manifest | null>(null);
  async function load() {
    const results = await Promise.allSettled([api<{ configured: boolean; settings: StorageSettings }>("/settings/storage"), api<Discovery>("/recovery/backups")]);
    if (results[0].status === "fulfilled") setStorage(results[0].value); else { setStorage({ configured: false, settings: EMPTY_STORAGE }); setError(results[0].reason instanceof Error ? results[0].reason.message : "Backup storage is unavailable."); }
    if (results[1].status === "fulfilled") setDiscovery(results[1].value); else { setDiscovery({ state: "storage_error", error: results[1].reason instanceof Error ? results[1].reason.message : "Backups could not be listed.", backups: [] }); if (results[0].status === "fulfilled") setError(results[1].reason instanceof Error ? results[1].reason.message : "Backups could not be listed."); }
  }
  useEffect(() => { void load(); }, []);
  const groups = useMemo(() => { const value = new Map<string, Manifest[]>(); discovery?.backups.forEach((backup) => value.set(backup.project_name, [...(value.get(backup.project_name) || []), backup])); return value; }, [discovery]);
  const showForm = storage && (!storage.configured || editing || discovery?.state === "storage_error");
  return <>
    <PageHeader title="Backups" description="Daily backups of every database to a bucket you own. Restore any of them here — on this server or a new one." />
    {error && discovery?.state !== "storage_error" && <ErrorNotice message={error} />}
    {!storage || !discovery ? !error && <Skeleton lines={4} /> : <>
      <Card><CardHeader title={storage.configured ? "Backup storage" : "Set up backup storage"} aside={storage.configured && <><Pill tone="good">Storage connected · {storage.settings.bucket}</Pill><Button size="sm" variant="secondary" onClick={() => setEditing(!editing)}>{editing ? "Close" : "Edit storage"}</Button></>} />
        {!storage.configured && <><h2>Backup storage</h2><ul className="feature-list"><li>Runs daily for every database</li><li>Uses your own S3-compatible bucket</li><li>Restores on this server or a new one</li></ul></>}
        {discovery.state === "storage_error" && <ErrorNotice message={`Backups could not be listed: ${discovery.error || "Check your storage settings."}`} />}
        {showForm && <StorageForm initial={storage.settings} configured={storage.configured} onSaved={(settings) => { setStorage({ configured: true, settings }); setEditing(false); setError(""); void load(); }} />}
      </Card>
      {storage.configured && discovery.state !== "storage_not_configured" && <Card><CardHeader title="All backups" />{discovery.backups.length === 0 ? <EmptyState icon={<EmptyArt />} title="No backups yet">Your first daily backup will appear here.</EmptyState> : <div className="backup-groups">{[...groups.entries()].map(([name, backups]) => <section key={name} className="backup-group"><h3><Database size={16} />{name}</h3><div className="backup-rows">{backups.map((backup) => { const seconds = Date.parse(backup.created_at) / 1000; return <div className="backup-row" key={backup.manifest_key} title={`sha256 ${backup.sha256}`}><span><strong><time title={formatDate(seconds)}>{relativeTime(seconds)}</time></strong><small>{formatDateTime(seconds)} · {formatBytes(backup.size_bytes)} · {backup.tables.length} table{backup.tables.length === 1 ? "" : "s"} · PostgreSQL {backup.postgres_version.split(" ")[0]}{discovery.installation_id && backup.installation_id !== discovery.installation_id ? " · from another server" : ""}</small></span><Button variant="secondary" size="sm" onClick={() => setSelected(backup)}><RotateCcw size={14} />Restore</Button></div>; })}</div></section>)}</div>}</Card>}
      {discovery.restores && discovery.restores.length > 0 && <details className="previous-restores"><summary>Previous restores</summary><Card><div className="job-rows">{discovery.restores.map((job) => <div className="job-row" key={job.id}><Pill tone={jobTone(job)}>{jobLabel(job)}</Pill><span><strong>{formatDate(job.created_at)}</strong><small>{job.error || (job.result.verified ? "All verification checks passed" : STAGE_LABELS[job.stage] || job.stage)}</small></span>{job.result.project_id && job.state === "succeeded" && <Button variant="ghost" size="sm" onClick={() => navigate(`/projects/${job.result.project_id}`)}>Open</Button>}</div>)}</div></Card></details>}
    </>}
    <RestoreDialog manifest={selected} open={!!selected} busy={discovery?.busy} onClose={() => setSelected(null)} navigate={navigate} onComplete={() => void load()} />
  </>;
}
