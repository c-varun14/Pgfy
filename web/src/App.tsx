import { useCallback, useEffect, useState } from "react";
import { TriangleAlert } from "lucide-react";
import { api, APIError, setCSRF, type Session, type Settings, type Status } from "./api";
import { Shell } from "./components/Shell";
import { PageHeader } from "./components/PageHeader";
import { Banner, ErrorNotice } from "./components/ui/banner";
import { Skeleton } from "./components/ui/skeleton";
import { useRoute } from "./router";
import { AuthPage } from "./pages/Auth";
import { ProjectsPage } from "./pages/Projects";
import { ProjectPage } from "./pages/Project";
import { RecoveryPage } from "./pages/Recovery";
import { SettingsPage } from "./pages/Settings";

export function App() {
  const { path, navigate, redirect } = useRoute();
  const [session, setSession] = useState<Session | null>(null);
  const [setup, setSetup] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [status, setStatus] = useState<Status | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);

  const load = useCallback(async () => {
    setError("");
    try {
      const current = await api<Session>("/auth/session");
      setCSRF(current.csrf_token);
      setSession(current);
      const [health, installation] = await Promise.all([api<Status>("/system/status"), api<Settings>("/settings")]);
      setStatus(health);
      setSettings(installation);
    } catch (failure) {
      if (failure instanceof APIError && failure.status === 401) {
        setSession(null); setStatus(null); setSettings(null);
        try { setSetup((await api<{ available: boolean }>("/setup")).available); } catch (setupFailure) { setError((setupFailure as Error).message); }
      } else setError((failure as Error).message);
    } finally { setLoading(false); }
  }, []);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => { if (!session) return; const timer = setInterval(() => void load(), 30000); return () => clearInterval(timer); }, [session?.email, load]);
  useEffect(() => { if (path === "/recovery") redirect("/backups"); }, [path, redirect]);

  async function logout() {
    try { await api("/auth/logout", { method: "POST", body: "{}" }); setSession(null); setStatus(null); setSettings(null); navigate("/"); await load(); }
    catch (failure) { setError((failure as Error).message); }
  }

  if (loading) return <div className="app-loading"><strong>pgfy.</strong><Skeleton lines={2} /></div>;
  if (!session) return <AuthPage setup={setup} error={error} onRetry={() => void load()} onSuccess={load} />;

  const projectMatch = path.match(/^\/projects\/([A-Za-z0-9_-]+)$/);
  const area = projectMatch ? "project" : path === "/backups" || path === "/recovery" ? "backups" : path === "/settings" ? "settings" : "databases";
  return <Shell path={path} session={session} status={status} settings={settings} navigate={navigate} onLogout={() => void logout()}>
    {error && <ErrorNotice message={error} />}
    {area === "databases" && <>
      <PageHeader title="Databases" />
      {status && !status.ready && <Banner tone="warn"><TriangleAlert size={18} /><div><h2>Your server needs attention</h2><p>PostgreSQL isn't responding. Run <code>pgfyctl diagnostics</code> on the server.</p></div></Banner>}
      <ProjectsPage navigate={navigate} />
    </>}
    {area === "project" && <ProjectPage id={projectMatch![1]} session={session} navigate={navigate} />}
    {area === "backups" && <><PageHeader title="Backups" description="Daily backups of every database to a bucket you own. Restore any of them here — on this server or a new one." /><RecoveryPage navigate={navigate} /></>}
    {area === "settings" && <><PageHeader title="Settings" />{settings ? <SettingsPage settings={settings} status={status} /> : !error && <Skeleton />}</>}
  </Shell>;
}
