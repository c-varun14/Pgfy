import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type APIError, type BackupHistory, type Project } from "../../api";
import { relativeTime } from "../../lib/format";
import { ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { Dialog } from "../../components/ui/dialog";
import { Field } from "../../components/ui/field";

/** Deleting asks for the exact name, and for a recent backup or an explicit acknowledgment that newer data is lost. */
export function DeleteDialog({ project, open, onClose, onDeleted }: { project: Project; open: boolean; onClose: () => void; onDeleted: () => void }) {
  const [name, setName] = useState(""); const [acknowledged, setAcknowledged] = useState(false); const [history, setHistory] = useState<BackupHistory | null>(null); const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const [needsAck, setNeedsAck] = useState(false);
  useEffect(() => { if (!open) return; setName(""); setAcknowledged(false); setError(""); setNeedsAck(false); if (project.stage === "ready") void api<BackupHistory>(`/projects/${project.id}/backups`).then(setHistory).catch(() => setHistory(null)); }, [open, project.id]);
  const hadData = project.stage === "ready";
  const newest = history?.newest_backup_at || 0;
  const recent = !!newest && Date.now() / 1000 - newest < (history?.target_interval_hours ?? 24) * 3600;
  const askAck = hadData && (needsAck || (history !== null && !recent));
  async function remove() {
    setBusy(true); setError("");
    try { await api(`/projects/${project.id}`, { method: "DELETE", body: JSON.stringify({ confirm_name: name, acknowledge_no_recent_backup: acknowledged }) }); onDeleted(); }
    catch (failure) { if ((failure as APIError).code === "backup_required") setNeedsAck(true); setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  return <Dialog open={open} onClose={onClose} title={`Delete ${project.name}?`}>
    <div className="dialog-form">
      <p>The database and its user are removed from PostgreSQL: logins are disabled, open sessions are ended, and the connection URL stops working. This cannot be undone from the dashboard.</p>
      {hadData && <p>{newest ? <>The newest recoverable backup is from <strong>{relativeTime(newest)}</strong>.</> : history?.storage_configured === false ? <>Backups are off, so <strong>nothing can be restored</strong> after this.</> : <>There is <strong>no recoverable backup</strong> of this database.</>} Backups already in the bucket are kept under the retention policy and can be restored from Backups.</p>}
      {askAck && <label className="check-field"><input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />{newest ? "I accept losing everything written since that backup" : "I accept losing this data"}</label>}
      <Field label={`Type ${project.name} to confirm`} htmlFor="delete-confirm"><input id="delete-confirm" value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" /></Field>
      {error && <ErrorNotice message={error} />}
      <div className="actions"><Button variant="danger" loading={busy} disabled={name !== project.name || (askAck && !acknowledged)} onClick={() => void remove()}><Trash2 size={15} />{busy ? "Deleting…" : "Delete database"}</Button><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button></div>
    </div>
  </Dialog>;
}
