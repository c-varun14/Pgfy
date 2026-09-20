import { ArrowRight, Check, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type Job, type Manifest } from "../../api";
import { formatDate, formatDuration, STAGE_LABELS } from "../../lib/format";
import { ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { Dialog } from "../../components/ui/dialog";
import { Field } from "../../components/ui/field";
import { Pill } from "../../components/ui/pill";

export function RestoreDialog({ manifest, open, busy = false, onClose, navigate, onComplete }: { manifest: Manifest | null; open: boolean; busy?: boolean; onClose: () => void; navigate: (to: string) => void; onComplete?: () => void }) {
  const [name, setName] = useState(""); const [job, setJob] = useState<Job | null>(null); const [projectID, setProjectID] = useState(""); const [starting, setStarting] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (manifest) { setName(`${manifest.project_name} (recovered)`); setJob(null); setError(""); setProjectID(""); } }, [manifest?.manifest_key]);
  useEffect(() => { if (!job || (job.state !== "queued" && job.state !== "running")) return; const timer = setInterval(() => void api<Job>(`/jobs/${job.id}`).then((next) => { setJob(next); if (next.state === "succeeded" || next.state === "failed" || next.state === "interrupted") onComplete?.(); }).catch((failure) => setError((failure as Error).message)), 2000); return () => clearInterval(timer); }, [job?.id, job?.state]);
  async function restore() { if (!manifest) return; setStarting(true); setError(""); try { const body = await api<{ project: { id: string }; job: Job }>("/recovery/restores", { method: "POST", body: JSON.stringify({ manifest_key: manifest.manifest_key, name: name.trim() }) }); setProjectID(body.project.id); setJob(body.job); } catch (failure) { setError((failure as Error).message); } finally { setStarting(false); } }
  if (!manifest) return null;
  const running = job?.state === "queued" || job?.state === "running"; const failed = job?.state === "failed" || job?.state === "interrupted";
  return <Dialog open={open} onClose={onClose} title={job ? job.state === "succeeded" ? "Restore complete" : failed ? "Restore did not complete" : "Restoring database" : `Restore ${manifest.project_name}`}>
    {!job ? <><p>Restores this backup into a <strong>new</strong> database named below.</p><Field label="Database name" htmlFor="restore-name" hint={`Nothing existing changes. Changes made after ${formatDate(Date.parse(manifest.created_at) / 1000)} are not included.`}><input id="restore-name" value={name} onChange={(event) => setName(event.target.value)} maxLength={64} required /></Field>{error && <ErrorNotice message={error} />}<div className="actions dialog-actions"><Button loading={starting} disabled={busy || !name.trim()} onClick={() => void restore()}>{starting ? "Starting…" : busy ? "Another job is running" : "Restore"}</Button><Button variant="ghost" onClick={onClose}>Cancel</Button></div></> : <div className="restore-progress"><Pill tone={job.state === "succeeded" ? "good" : failed ? "bad" : "wait"}>{job.state === "succeeded" ? "Restored" : failed ? "Failed" : STAGE_LABELS[job.stage] || job.stage}</Pill>{running && <><p>{STAGE_LABELS[job.stage] || job.stage} · {formatDuration(job.elapsed_seconds)} elapsed</p><p className="caption">You can close this — it continues on the server.</p></>}{job.result.checks && <ul className="check-steps">{job.result.checks.map((check) => <li key={check.name} className={check.ok ? "ok" : "bad"}>{check.ok ? <Check size={14} /> : <TriangleAlert size={14} />}{check.name}{check.detail && <small>{check.detail}</small>}</li>)}</ul>}{failed && <><ErrorNotice message={job.error || "The restore did not complete."} /><p>Nothing existing was modified.</p></>}{error && <ErrorNotice message={error} />}{job.state === "succeeded" && <Button onClick={() => navigate(`/projects/${projectID}`)}>Open database <ArrowRight size={15} /></Button>}</div>}
  </Dialog>;
}
