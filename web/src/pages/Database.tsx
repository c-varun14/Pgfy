import { ArrowLeft, Archive, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type Credentials, type DatabaseAccess, type Project, type Session } from "../api";
import { formatBytes, relativeTime } from "../lib/format";
import { PageHeader } from "../components/PageHeader";
import { ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { EmptyState } from "../components/ui/empty-state";
import { Pill } from "../components/ui/pill";
import { Skeleton } from "../components/ui/skeleton";
import { Tabs } from "../components/ui/tabs";
import { Tooltip } from "../components/ui/tooltip";
import { useToast } from "../components/ui/toast";
import { databaseStage } from "./Databases";
import { AccessTab } from "./database/AccessTab";
import { BackupsTab } from "./database/BackupsTab";
import { ConnectTab } from "./database/ConnectTab";
import { OverviewTab } from "./database/OverviewTab";
import { Provisioning } from "./database/Provisioning";

type DatabaseTab = "overview" | "connect" | "backups" | "access";
const tabs: { value: DatabaseTab; label: string }[] = [{ value: "overview", label: "Overview" }, { value: "connect", label: "Connect" }, { value: "backups", label: "Backups" }, { value: "access", label: "Access" }];
export function DatabasePage({ id, session, tab, navigate }: { id: string; session: Session; tab: string | null; navigate: (to: string) => void }) {
  const [project, setProject] = useState<Project | null>(null); const [access, setAccess] = useState<DatabaseAccess | null>(null); const [error, setError] = useState(""); const [notFound, setNotFound] = useState(false);
  const [credentials, setCredentials] = useState<Credentials | null>(null); const [revealed, setRevealed] = useState(false); const [credentialBusy, setCredentialBusy] = useState(false); const [credentialError, setCredentialError] = useState(""); const [storage, setStorage] = useState<boolean | null>(null); const [backupBusy, setBackupBusy] = useState(false); const { showToast } = useToast();
  const activeTab: DatabaseTab = tabs.some((item) => item.value === tab) ? tab as DatabaseTab : "overview";
  async function load() { try { const body = await api<{ project: Project; database_access: DatabaseAccess }>(`/projects/${id}`); setProject(body.project); setAccess(body.database_access); setError(""); } catch (failure) { if ((failure as { status?: number }).status === 404) setNotFound(true); else setError((failure as Error).message); } }
  useEffect(() => { setProject(null); void load(); void api<{ storage_configured: boolean }>(`/projects/${id}/backups`).then((body) => setStorage(body.storage_configured)).catch(() => setStorage(null)); }, [id]);
  useEffect(() => { if (!project || project.failed) return; const timer = setInterval(() => void load(), project.stage === "ready" ? 10000 : 2000); return () => clearInterval(timer); }, [project?.stage, project?.failed, id]);
  async function getCredentials() { if (credentials) return credentials; setCredentialBusy(true); setCredentialError(""); try { const value = await api<Credentials>(`/projects/${id}/credentials`); setCredentials(value); return value; } catch (failure) { setCredentialError((failure as Error).message); return null; } finally { setCredentialBusy(false); } }
  async function reveal() { const value = await getCredentials(); if (value) setRevealed(true); }
  async function copyUrl() { const value = await getCredentials(); if (value) { await navigator.clipboard.writeText(value.url); showToast("Connection URL copied"); } }
  async function backupNow() { setBackupBusy(true); setError(""); try { await api(`/projects/${id}/backups`, { method: "POST", body: "{}" }); showToast("Backup started"); navigate(`/projects/${id}?tab=backups`); } catch (failure) { setError((failure as Error).message); } finally { setBackupBusy(false); } }
  if (notFound) return <EmptyState title="Database not found" action={<Button variant="secondary" onClick={() => navigate("/")}><ArrowLeft size={15} />Back to databases</Button>}>This database may have been removed.</EmptyState>;
  if (!project || !access) return error ? <ErrorNotice message={error} /> : <Skeleton lines={4} />;
  const stage = databaseStage(project); const ready = project.stage === "ready" && !project.failed; const connections = project.connections_now?.length || 0; const sslmode = access.mode === "tunnel" ? "disable" : access.certificate.state === "trusted" ? "verify-full" : "require";
  const actions = ready ? <><Button onClick={() => void copyUrl()} loading={credentialBusy}><Copy size={15} />Copy connection URL</Button><Tooltip text={storage === false ? "Set up backup storage first" : "Create a backup now"}><Button variant="secondary" onClick={() => void backupNow()} loading={backupBusy} disabled={storage !== true}><Archive size={15} />Back up now</Button></Tooltip></> : undefined;
  return <>
    <PageHeader eyebrow={<div className="breadcrumb"><button type="button" onClick={() => navigate("/")}>Databases</button><span>/</span><span>{project.name}</span></div>} title={project.name} status={<Pill tone={stage.tone}>{stage.label}</Pill>} description={`${ready ? formatBytes(project.size_bytes) : "Setting up"} · created ${relativeTime(project.created_at)} · ${connections ? `${connections} live connection${connections === 1 ? "" : "s"}` : "no app connected"}`} actions={actions} />
    {error && <ErrorNotice message={error} />}
    {!ready ? <Provisioning project={project} onRetry={load} /> : <><Tabs value={activeTab} options={tabs} onChange={(next) => navigate(`/projects/${id}?tab=${next}`)} label="Database sections" /><div className="tab-panel" role="tabpanel">
      {activeTab === "overview" && <OverviewTab project={project} host={access.host} sslmode={sslmode} credentials={credentials} revealed={revealed} credentialBusy={credentialBusy} credentialError={credentialError} onReveal={() => void reveal()} onHide={() => setRevealed(false)} onCopy={() => void copyUrl()} onTab={(next) => navigate(`/projects/${id}?tab=${next}`)} />}
      {activeTab === "connect" && <ConnectTab project={project} access={access} credentials={credentials} revealed={revealed} credentialBusy={credentialBusy} credentialError={credentialError} onReveal={() => void reveal()} onHide={() => setRevealed(false)} onCopy={() => void copyUrl()} />}
      {activeTab === "backups" && <BackupsTab project={project} navigate={navigate} />}
      {activeTab === "access" && <AccessTab project={project} session={session} onChange={load} />}
    </div></>}
  </>;
}
