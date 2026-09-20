import { useCallback, useEffect, useState } from "react";
import { api, APIError, setCSRF, type Session, type Settings, type Status } from "./api";
import { Shell } from "./components/Shell";
import { BrandMark } from "./components/Sidebar";
import { ErrorNotice } from "./components/ui/banner";
import { Skeleton } from "./components/ui/skeleton";
import { useRoute } from "./router";
import { AuthPage } from "./pages/Auth";
import { DatabasesPage } from "./pages/Databases";
import { DatabasePage } from "./pages/Database";
import { BackupsPage } from "./pages/BackupsPage";
import { SettingsPage } from "./pages/Settings";

export function App() {
  const { path, params, navigate, redirect } = useRoute();
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

  if (loading) return <div className="app-loading"><BrandMark size={40} /><span className="loading-text">Connecting to your server…</span></div>;
  if (!session) return <AuthPage setup={setup} error={error} onRetry={() => void load()} onSuccess={load} />;

  const projectMatch = path.match(/^\/projects\/([A-Za-z0-9_-]+)$/);
  const area = projectMatch ? "project" : path === "/backups" || path === "/recovery" ? "backups" : path === "/settings" ? "settings" : "databases";
  return <Shell path={path} session={session} status={status} settings={settings} navigate={navigate} onLogout={() => void logout()}>
    {error && <ErrorNotice message={error} />}
    {area === "databases" && <DatabasesPage status={status} navigate={navigate} />}
    {area === "project" && <DatabasePage id={projectMatch![1]} session={session} tab={params.get("tab")} navigate={navigate} />}
    {area === "backups" && <BackupsPage navigate={navigate} />}
    {area === "settings" && <>{settings ? <SettingsPage settings={settings} status={status} /> : !error && <Skeleton />}</>}
  </Shell>;
}
