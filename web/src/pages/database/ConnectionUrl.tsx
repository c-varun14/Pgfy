import { Eye, EyeOff } from "lucide-react";
import type { Credentials, Project } from "../../api";
import { Button } from "../../components/ui/button";
import { CodeBlock } from "../../components/ui/code-block";
import { ErrorNotice } from "../../components/ui/banner";

export function maskedUrl(project: Project, host: string, sslmode: string) { return `postgresql://${project.role_name}:••••••••@${host}:5432/${project.db_name}?sslmode=${sslmode}`; }
export function ConnectionUrl({ project, host, sslmode, credentials, revealed, busy, error, onReveal, onHide, onCopy }: { project: Project; host: string; sslmode: string; credentials: Credentials | null; revealed: boolean; busy: boolean; error: string; onReveal: () => void; onHide: () => void; onCopy: () => void }) {
  const value = credentials && revealed ? credentials.url : maskedUrl(project, host, sslmode);
  return <div className="connection-url"><CodeBlock value={value} label="Connection URL" copy={false} /><div className="connection-actions">{revealed ? <Button variant="secondary" size="sm" onClick={onHide}><EyeOff size={14} />Hide password</Button> : <Button variant="secondary" size="sm" loading={busy} onClick={onReveal}><Eye size={14} />Reveal</Button>}<Button size="sm" loading={busy} onClick={onCopy}>Copy URL</Button></div>{error && <ErrorNotice message={error} />}</div>;
}
