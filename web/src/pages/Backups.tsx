import { useEffect, useState } from "react";
import { Archive, Check, TriangleAlert } from "lucide-react";
import { Button } from "../components/ui/button";
import { api, formatBytes, formatDate, formatDuration, STAGE_LABELS, type BackupRecord, type Job, type Project } from "../api";
import { ErrorNotice, Pill } from "../ui";

type History = { backups: BackupRecord[]; jobs: Job[]; storage_configured: boolean; next_scheduled_at: number };

export function jobTone(j: Job) {
  return j.state === "succeeded" ? "good" : j.state === "failed" || j.state === "interrupted" ? "bad" : "wait";
}
export function jobLabel(j: Job) {
  if (j.state === "succeeded") return j.kind === "restore" ? (j.result.verified ? "Restore verified" : "Restored") : "Backup completed";
  if (j.state === "failed") return "Failed";
  if (j.state === "interrupted") return "Interrupted";
  return STAGE_LABELS[j.stage] ?? j.stage;
}

export function BackupsPanel({ project, navigate }: { project: Project; navigate: (to: string) => void }) {
  const [history, setHistory] = useState<History | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function load() {
    try {
      setHistory(await api<History>(`/projects/${project.id}/backups`));
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    void load();
  }, [project.id]);
  const active = history?.jobs.some((j) => j.state === "queued" || j.state === "running");
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => void load(), 2000);
    return () => clearInterval(timer);
  }, [active]);
  async function backupNow() {
    setBusy(true);
    setError("");
    try {
      await api(`/projects/${project.id}/backups`, { method: "POST", body: "{}" });
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const last = history?.backups[0];
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Backups</h2>
        {history && !history.storage_configured ? (
          <Pill tone="bad">Storage not configured</Pill>
        ) : last ? (
          <Pill tone="good">Last backup {formatDate(last.created_at)}</Pill>
        ) : (
          history && <Pill tone="wait">No backup yet</Pill>
        )}
      </div>
      {history && !history.storage_configured && (
        <div className="notice">
          <TriangleAlert size={18} />
          <span>
            This database is not protected against server loss yet. Configure an S3-compatible bucket in{" "}
            <button className="link" onClick={() => navigate("/settings")}>
              Settings
            </button>{" "}
            to enable daily backups.
          </span>
        </div>
      )}
      {history?.storage_configured && (
        <p className="muted small">
          Backups run daily once storage is configured
          {history.next_scheduled_at ? ` — next around ${formatDate(history.next_scheduled_at)}` : " — the first one is scheduled within a few minutes"}. Retention is not automated: delete old backups in your bucket when you no longer need them.
        </p>
      )}
      <div className="actions">
        <Button onClick={() => void backupNow()} disabled={busy || active || !history?.storage_configured}>
          <Archive size={15} /> {active ? "Running…" : "Back up now"}
        </Button>
      </div>
      {error && <ErrorNotice message={error} />}
      {history && history.jobs.length > 0 && (
        <ul className="job-list">
          {history.jobs.map((j) => (
            <li key={j.id}>
              <Pill tone={jobTone(j)}>{jobLabel(j)}</Pill>
              <div>
                <strong>
                  {j.kind === "backup" ? "Backup" : "Restore"} · {formatDate(j.created_at)}
                </strong>
                <small>
                  {j.state === "running" || j.state === "queued" ? `${STAGE_LABELS[j.stage] ?? j.stage} · ${formatDuration(j.elapsed_seconds)} elapsed` : ""}
                  {j.state === "succeeded" && j.kind === "backup" ? `${formatBytes(j.result.size_bytes)} · sha256 ${String(j.result.sha256 ?? "").slice(0, 12)}… · ${formatDuration(j.elapsed_seconds)}` : ""}
                  {(j.state === "failed" || j.state === "interrupted") && `${j.error} (stopped at: ${STAGE_LABELS[j.stage] ?? j.stage})`}
                </small>
              </div>
              {j.state === "succeeded" && <Check size={15} className="ok" />}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
