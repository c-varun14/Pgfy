import { Check, Circle, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type BackupHistory, type Credentials, type Project } from "../../api";
import { relativeTime } from "../../lib/format";
import { Card, CardHeader } from "../../components/ui/card";
import { ConnectionUrl } from "./ConnectionUrl";

export function OverviewTab({ project, host, sslmode, credentials, revealed, credentialBusy, credentialError, onReveal, onHide, onCopy, onTab }: { project: Project; host: string; sslmode: string; credentials: Credentials | null; revealed: boolean; credentialBusy: boolean; credentialError: string; onReveal: () => void; onHide: () => void; onCopy: () => void; onTab: (tab: string) => void }) {
  const [history, setHistory] = useState<BackupHistory | null>(null);
  useEffect(() => { void api<BackupHistory>(`/projects/${project.id}/backups`).then(setHistory).catch(() => setHistory(null)); }, [project.id]);
  const sessions = project.connections_now?.length || 0; const newest = history?.newest_backup_at || 0;
  return <div className="tab-stack"><Card><CardHeader title="Connection URL" /><ConnectionUrl project={project} host={host} sslmode={sslmode} credentials={credentials} revealed={revealed} busy={credentialBusy} error={credentialError} onReveal={onReveal} onHide={onHide} onCopy={onCopy} /></Card>
    <Card><CardHeader title="Checklist" /><div className="checklist">
      <button type="button" onClick={() => onTab("overview")}><Check size={17} className="ok" /><span><strong>Database ready</strong></span></button>
      <button type="button" onClick={() => onTab("connect")}>{sessions ? <Check size={17} className="ok" /> : <Circle size={17} />}<span><strong>{sessions ? `${sessions} live session${sessions === 1 ? "" : "s"}` : "No app connected yet"}</strong>{!sessions && <small>Connect →</small>}</span></button>
      <button type="button" onClick={() => onTab("backups")}>{history?.storage_configured === false ? <TriangleAlert size={17} className="warn" /> : newest ? <Check size={17} className="ok" /> : <Circle size={17} />}<span><strong>{history?.storage_configured === false ? "Backups are off" : newest ? `Newest recoverable backup ${relativeTime(newest)}` : history ? `First backup ${history.next_scheduled_at ? relativeTime(history.next_scheduled_at) : "within a few minutes"}` : "Checking backups…"}</strong>{history?.storage_configured === false && <small>Backups →</small>}</span></button>
    </div></Card>
  </div>;
}
