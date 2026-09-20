import { Archive, RotateCcw } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type BackupRecord, type Job, type Project } from "../../api";
import { formatBytes, formatDate, formatDateTime, formatDuration, relativeTime, STAGE_LABELS } from "../../lib/format";
import { Banner, ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { Card, CardHeader } from "../../components/ui/card";
import { Pill } from "../../components/ui/pill";
import { Progress } from "../../components/ui/progress";
import { EmptyArt } from "../../components/ui/empty-art";
import { EmptyState } from "../../components/ui/empty-state";
import { Tooltip } from "../../components/ui/tooltip";
import { useToast } from "../../components/ui/toast";
import { RestoreDialog } from "../backups/RestoreDialog";

export type History = { backups: BackupRecord[]; jobs: Job[]; storage_configured: boolean; next_scheduled_at: number };
export function jobTone(job: Job) { return job.state === "succeeded" ? "good" as const : job.state === "failed" || job.state === "interrupted" ? "bad" as const : "wait" as const; }
export function jobLabel(job: Job) { if (job.state === "succeeded") return job.kind === "restore" ? (job.result.verified ? "Restore verified" : "Restored") : "Backup completed"; if (job.state === "failed") return "Failed"; if (job.state === "interrupted") return "Interrupted"; return STAGE_LABELS[job.stage] || job.stage; }

export function BackupsTab({ project, navigate }: { project: Project; navigate: (to: string) => void }) {
  const [history, setHistory] = useState<History | null>(null); const [error, setError] = useState(""); const [busy, setBusy] = useState(false); const [selected, setSelected] = useState<BackupRecord | null>(null); const { showToast } = useToast();
  async function load() { try { setHistory(await api<History>(`/projects/${project.id}/backups`)); setError(""); } catch (failure) { setError((failure as Error).message); } }
  useEffect(() => { void load(); }, [project.id]); const active = history?.jobs.some((job) => job.state === "queued" || job.state === "running");
  useEffect(() => { if (!active) return; const timer = setInterval(() => void load(), 2000); return () => clearInterval(timer); }, [active]);
  async function backupNow() { setBusy(true); setError(""); try { await api(`/projects/${project.id}/backups`, { method: "POST", body: "{}" }); showToast("Backup started"); await load(); } catch (failure) { setError((failure as Error).message); } finally { setBusy(false); } }
  const last = history?.backups[0];
  return <div className="tab-stack">
    <Card><div className="backup-status"><div>{history?.storage_configured === false ? <><strong>Backups are off</strong><span> — </span><button className="link" onClick={() => navigate("/backups")}>Set up storage →</button></> : last ? <>Daily backups <strong>on</strong> · last {relativeTime(last.created_at)}{history?.next_scheduled_at ? ` · next ${relativeTime(history.next_scheduled_at)}` : ""}</> : history ? <>Daily backups <strong>on</strong> · first backup scheduled within a few minutes</> : "Checking backup status…"}</div><Tooltip text={history?.storage_configured === false ? "Set up backup storage first" : "Create a backup now"}><Button onClick={() => void backupNow()} loading={busy} disabled={!history?.storage_configured || active}><Archive size={15} />{active ? "Running…" : "Back up now"}</Button></Tooltip></div>{error && <ErrorNotice message={error} />}</Card>
    <Card><CardHeader title="Backups" />{active && <Progress />}{history && history.backups.length === 0 ? <EmptyState icon={<EmptyArt />} title="No backups yet" /> : <div className="backup-rows">{history?.backups.map((backup) => <div className="backup-row" key={backup.id} title={`sha256 ${backup.manifest.sha256}`}><span><strong><time title={formatDate(backup.created_at)}>{relativeTime(backup.created_at)}</time></strong><small>{formatDateTime(backup.created_at)} · {formatBytes(backup.size_bytes)} · {backup.manifest.tables.length} table{backup.manifest.tables.length === 1 ? "" : "s"}</small></span><Button variant="secondary" size="sm" onClick={() => setSelected(backup)}><RotateCcw size={14} />Restore</Button></div>)}</div>}</Card>
    {history && history.jobs.length > 0 && <details className="activity" open={active}><summary>Activity</summary><Card><div className="job-rows">{history.jobs.slice(0, 5).map((job) => <div className="job-row" key={job.id} title={job.result.sha256 ? `sha256 ${job.result.sha256}` : undefined}><Pill tone={jobTone(job)}>{jobLabel(job)}</Pill><span><strong>{job.kind === "backup" ? "Backup" : "Restore"} · {formatDateTime(job.created_at)}</strong><small>{job.state === "running" || job.state === "queued" ? `${STAGE_LABELS[job.stage] || job.stage} · ${formatDuration(job.elapsed_seconds)} elapsed` : job.state === "failed" || job.state === "interrupted" ? job.error : `${formatBytes(job.result.size_bytes)} · ${formatDuration(job.elapsed_seconds)}`}</small></span></div>)}</div></Card></details>}
    <RestoreDialog manifest={selected?.manifest || null} open={!!selected} onClose={() => setSelected(null)} navigate={navigate} onComplete={() => void load()} />
  </div>;
}
