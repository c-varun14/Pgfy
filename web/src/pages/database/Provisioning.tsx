import { useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, type Project } from "../../api";
import { ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { Card, CardHeader } from "../../components/ui/card";
import { Stepper } from "../../components/ui/stepper";

const stages: Project["stage"][] = ["role_created", "database_created", "ready"];
export const STAGE_TEXT: Record<Project["stage"], string> = { identity_persisted: "Saving database identity", role_created: "Creating a dedicated database user", database_created: "Creating the database", ready: "Database ready" };

export function Provisioning({ project, onRetry }: { project: Project; onRetry: () => Promise<void> }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const current = stages.indexOf(project.stage);
  async function retry() { setBusy(true); setError(""); try { await api(`/projects/${project.id}/retry`, { method: "POST", body: "{}" }); await onRetry(); } catch (failure) { setError((failure as Error).message); } finally { setBusy(false); } }
  return <Card><CardHeader title={project.failed ? "Database setup stopped" : "Setting up your database"} aside={!project.failed && <RefreshCw className="spin" size={16} />} />
    <Stepper steps={stages.map((stage, index) => ({ label: STAGE_TEXT[stage], state: index < current ? "done" : index === current ? (project.failed ? "failed" : "active") : "pending" }))} />
    {project.failed && <><ErrorNotice message={project.stage_error || "Database setup did not complete."} /><p className="caption">Retrying resumes from the last completed step with the same identity; nothing is duplicated.</p><Button loading={busy} onClick={() => void retry()}><RefreshCw size={15} />{busy ? "Retrying…" : "Retry"}</Button></>}
    {error && <ErrorNotice message={error} />}
  </Card>;
}
