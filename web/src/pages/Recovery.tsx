import { useEffect, useState } from "react";
import { ArrowRight, Check, Database, LifeBuoy, RefreshCw, TriangleAlert } from "lucide-react";
import { Button } from "../components/ui/button";
import { api, formatBytes, formatDate, formatDuration, STAGE_LABELS, type Job, type Manifest } from "../api";
import { ErrorNotice, Pill } from "../ui";
import { jobLabel, jobTone } from "./Backups";

type Discovery = { state: "ok" | "storage_not_configured" | "storage_error"; error?: string; backups: Manifest[]; installation_id?: string; busy?: boolean; restores?: Job[] };

export function RecoveryPage({ navigate }: { navigate: (to: string) => void }) {
  const [discovery, setDiscovery] = useState<Discovery | null>(null);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Manifest | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [job, setJob] = useState<Job | null>(null);
  const [projectID, setProjectID] = useState("");
  async function load() {
    try {
      setDiscovery(await api<Discovery>("/recovery/backups"));
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    if (!job || (job.state !== "queued" && job.state !== "running")) return;
    const timer = setInterval(() => {
      void api<Job>(`/jobs/${job.id}`).then(setJob).catch((e) => setError((e as Error).message));
    }, 2000);
    return () => clearInterval(timer);
  }, [job?.id, job?.state]);
  async function restore() {
    if (!selected) return;
    setBusy(true);
    setError("");
    try {
      const body = await api<{ project: { id: string }; job: Job }>("/recovery/restores", { method: "POST", body: JSON.stringify({ manifest_key: selected.manifest_key, name: name.trim() }) });
      setProjectID(body.project.id);
      setJob(body.job);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (!discovery) return error ? <ErrorNotice message={error} /> : <p role="status">Looking for backups…</p>;
  if (discovery.state === "storage_not_configured")
    return (
      <section className="panel empty">
        <span className="section-icon">
          <LifeBuoy size={23} />
        </span>
        <h2>Connect your backup storage first</h2>
        <p className="muted">On a new server, enter the same bucket and keys in Settings. Every backup that was published there appears here, ready to restore.</p>
        <Button variant="outline" onClick={() => navigate("/settings")}>
          Open Settings <ArrowRight size={15} />
        </Button>
      </section>
    );
  return (
    <>
      {discovery.state === "storage_error" && <ErrorNotice message={`Backups could not be listed: ${discovery.error}`} />}
      {error && <ErrorNotice message={error} />}
      {job && (
        <section className="panel">
          <div className="panel-heading">
            <h2>{job.state === "succeeded" ? "Database recovered" : job.state === "failed" ? "Restore did not complete" : job.state === "interrupted" ? "Restore interrupted" : "Restoring…"}</h2>
            <Pill tone={jobTone(job)}>{jobLabel(job)}</Pill>
          </div>
          {(job.state === "queued" || job.state === "running") && (
            <p className="muted small">
              <RefreshCw size={13} className="spin" /> {STAGE_LABELS[job.stage] ?? job.stage} · {formatDuration(job.elapsed_seconds)} elapsed. You can close this page; the restore continues on the server.
            </p>
          )}
          {(job.state === "failed" || job.state === "interrupted") && <ErrorNotice message={`${job.error} (stopped at: ${STAGE_LABELS[job.stage] ?? job.stage})`} />}
          {job.result.checks && (
            <ul className="steps check-steps">
              {job.result.checks.map((c) => (
                <li key={c.name} className={c.ok ? "done" : "failed"}>
                  {c.ok ? <Check size={14} /> : <TriangleAlert size={14} />}
                  {c.name}
                  {c.detail && <span className="muted"> — {c.detail}</span>}
                </li>
              ))}
            </ul>
          )}
          {job.result.warnings && <p className="muted small">pg_restore reported: {job.result.warnings}</p>}
          {job.state === "succeeded" && (
            <>
              <p className="muted small">
                Verification covered the checks listed above (row counts recorded at backup time, ownership, permissions). It does not prove application-level correctness. The new database has new credentials: open the project, copy its connection URL into your application, and run a read and a write.
              </p>
              <Button onClick={() => navigate(`/projects/${projectID}`)}>
                Open the recovered project <ArrowRight size={15} />
              </Button>
            </>
          )}
          {(job.state === "failed" || job.state === "interrupted") && (
            <p className="muted small">Nothing existing was modified. You can start the restore again; it creates another fresh project.</p>
          )}
        </section>
      )}
      <section className="panel">
        <div className="panel-heading">
          <h2>Backups found in your storage</h2>
          <Button variant="outline" onClick={() => void load()}>
            <RefreshCw size={14} /> Refresh
          </Button>
        </div>
        {discovery.backups.length === 0 ? (
          <p className="muted">No completed backups yet. Backups appear here once a project has been backed up.</p>
        ) : (
          <ul className="backup-list">
            {discovery.backups.map((m) => (
              <li key={m.manifest_key} className={selected?.manifest_key === m.manifest_key ? "selected" : ""}>
                <button type="button" onClick={() => { setSelected(m); setName(name || `${m.project_name} (recovered)`); }}>
                  <span className="section-icon">
                    <Database size={18} />
                  </span>
                  <div>
                    <strong>{m.project_name}</strong>
                    <small>
                      {formatDate(Date.parse(m.created_at) / 1000)} · {formatBytes(m.size_bytes)} · PostgreSQL {m.postgres_version.split(" ")[0]} · {m.tables.length} table{m.tables.length === 1 ? "" : "s"}
                      {discovery.installation_id && m.installation_id !== discovery.installation_id ? " · from another server" : ""}
                    </small>
                  </div>
                  <ArrowRight size={15} />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
      {selected && !(job && (job.state === "queued" || job.state === "running")) && (
        <section className="panel">
          <div className="panel-heading">
            <h2>Restore “{selected.project_name}” from {formatDate(Date.parse(selected.created_at) / 1000)}</h2>
          </div>
          <p className="muted small">
            The backup is restored into a <strong>new</strong> project with fresh credentials; nothing existing is changed. Changes made after the backup time are not in it. Archive checksum: <span className="mono">{selected.sha256.slice(0, 16)}…</span>
          </p>
          <div className="field">
            <label htmlFor="restore-name">Name for the new project</label>
            <input id="restore-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} required />
          </div>
          <div className="actions">
            <Button onClick={() => void restore()} disabled={busy || !name.trim() || discovery.busy}>
              <LifeBuoy size={15} /> {busy ? "Starting…" : discovery.busy ? "Another job is running" : "Restore into a new project"}
            </Button>
          </div>
        </section>
      )}
      {discovery.restores && discovery.restores.length > 0 && !job && (
        <section className="panel">
          <div className="panel-heading">
            <h2>Previous restores</h2>
          </div>
          <ul className="job-list">
            {discovery.restores.map((j) => (
              <li key={j.id}>
                <Pill tone={jobTone(j)}>{jobLabel(j)}</Pill>
                <div>
                  <strong>{formatDate(j.created_at)}</strong>
                  <small>{j.error || (j.result.verified ? "All verification checks passed" : STAGE_LABELS[j.stage] ?? j.stage)}</small>
                </div>
                {j.result.project_id && j.state === "succeeded" && (
                  <Button variant="ghost" onClick={() => navigate(`/projects/${j.result.project_id}`)}>
                    Open <ArrowRight size={14} />
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}
