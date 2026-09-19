import { useCallback, useEffect, useState } from "react";
import { Check, Database, LifeBuoy, LogOut, RefreshCw, Server, Settings2, TriangleAlert } from "lucide-react";
import { Button } from "./components/ui/button";
import { api, APIError, setCSRF, type Session, type Settings, type Status } from "./api";
import { useRoute } from "./router";
import { Brand, ErrorNotice } from "./ui";
import { AuthPage } from "./pages/Auth";
import { ProjectsPage } from "./pages/Projects";
import { ProjectPage } from "./pages/Project";
import { RecoveryPage } from "./pages/Recovery";
import { SettingsPage } from "./pages/Settings";

type Area = { key: "projects" | "recovery" | "settings"; label: string; path: string; icon: React.ReactNode };
const AREAS: Area[] = [
  { key: "projects", label: "Projects", path: "/", icon: <Database size={18} /> },
  { key: "recovery", label: "Recovery", path: "/recovery", icon: <LifeBuoy size={18} /> },
  { key: "settings", label: "Settings", path: "/settings", icon: <Settings2 size={18} /> },
];
const COPY = {
  projects: { title: "A place to build.", text: "Create a database, connect your app, and keep it safe." },
  project: { title: "", text: "" },
  recovery: { title: "Recover from backups.", text: "Bring a database back on this server, from your own storage." },
  settings: { title: "Installation settings.", text: "The essentials, managed from your host." },
};

export function App() {
  const { path, navigate } = useRoute();
  const [session, setSession] = useState<Session | null>(null);
  const [setup, setSetup] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [status, setStatus] = useState<Status | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    setError("");
    setRefreshing(true);
    try {
      const current = await api<Session>("/auth/session");
      setCSRF(current.csrf_token);
      setSession(current);
      const [health, installation] = await Promise.all([api<Status>("/system/status"), api<Settings>("/settings")]);
      setStatus(health);
      setSettings(installation);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) {
        setSession(null);
        setStatus(null);
        setSettings(null);
        try {
          setSetup((await api<{ available: boolean }>("/setup")).available);
        } catch (failure) {
          setError((failure as Error).message);
        }
      } else {
        setError((e as Error).message);
      }
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    if (!session) return;
    const timer = setInterval(() => void load(), 30000);
    return () => clearInterval(timer);
  }, [session?.email, load]);

  async function logout() {
    try {
      await api("/auth/logout", { method: "POST", body: "{}" });
      setSession(null);
      setStatus(null);
      setSettings(null);
      navigate("/");
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }
  if (loading)
    return (
      <div className="loading">
        <Brand />
        <p role="status">Connecting to your server…</p>
      </div>
    );
  if (!session) return <AuthPage setup={setup} error={error} onRetry={() => void load()} onSuccess={load} />;

  const projectMatch = path.match(/^\/projects\/([A-Za-z0-9_-]+)$/);
  const area: keyof typeof COPY = projectMatch ? "project" : path === "/recovery" ? "recovery" : path === "/settings" ? "settings" : "projects";
  const activeNav = area === "project" ? "projects" : area;
  const heading = COPY[area];
  return (
    <div className="shell">
      <aside className="sidebar">
        <Brand />
        <div className="workspace">
          <span className="workspace-avatar">P</span>
          <div>
            Your installation<small>Single administrator</small>
          </div>
        </div>
        <span className="nav-label">WORKSPACE</span>
        <nav>
          {AREAS.map((a) => (
            <button key={a.key} className={activeNav === a.key ? "active" : ""} onClick={() => navigate(a.path)}>
              {a.icon}
              {a.label}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <span className="server-label">
            <span className={`dot ${status && !status.ready ? "bad" : ""}`} />
            {status ? (status.ready ? "All services responding" : "Needs attention") : "Self-hosted installation"}
          </span>
          <span className="email">{session.email}</span>
          <Button variant="ghost" onClick={() => void logout()}>
            <LogOut size={16} />
            Sign out
          </Button>
        </div>
      </aside>
      <div className="main-wrap">
        <header className="topbar">
          <span>
            Workspace <span className="slash">/</span> {AREAS.find((a) => a.key === activeNav)?.label}
          </span>
          <span className="topbar-host">
            <Server size={14} />
            {settings?.hostname || "SSH tunnel"}
          </span>
          <Button className="mobile-signout" variant="ghost" onClick={() => void logout()}>
            <LogOut size={15} />
            Sign out
          </Button>
        </header>
        <main className="content">
          {area !== "project" && (
            <div className="page-heading">
              <div>
                <span className="eyebrow">YOUR POSTGRESQL FOUNDATION</span>
                <h1>{heading.title}</h1>
                <p className="muted">{heading.text}</p>
              </div>
              <Button variant="outline" disabled={refreshing} onClick={() => void load()}>
                <RefreshCw size={15} className={refreshing ? "spin" : ""} />
                {refreshing ? "Refreshing" : "Refresh"}
              </Button>
            </div>
          )}
          {error && <ErrorNotice message={error} />}
          {status && !status.ready && area === "projects" && (
            <div className="health-banner degraded">
              <span className="health-icon">
                <TriangleAlert size={22} />
              </span>
              <div>
                <h2>Your server needs attention</h2>
                <p>
                  {status.postgres.status !== "available" ? "PostgreSQL is not responding. " : "Management storage is not responding. "}
                  The dashboard stays available; run <code>pgfyctl diagnostics</code> on the host.
                </p>
              </div>
              <span className="badge">Degraded</span>
            </div>
          )}
          {status?.ready && area === "projects" && (
            <div className="health-banner slim">
              <span className="health-icon">
                <Check size={18} />
              </span>
              <div>
                <h2>Your foundation is ready</h2>
                <p>PostgreSQL {status.postgres.version} · application {status.versions.application}</p>
              </div>
              <span className="badge">Operational</span>
            </div>
          )}
          {area === "projects" && <ProjectsPage navigate={navigate} />}
          {area === "project" && <ProjectPage id={projectMatch![1]} session={session} navigate={navigate} />}
          {area === "recovery" && <RecoveryPage navigate={navigate} />}
          {area === "settings" && (settings ? <SettingsPage settings={settings} status={status} /> : !error && <p role="status">Loading installation details…</p>)}
          <footer className="footer">
            Pgfy <span>Small foundation. Room to grow.</span>
          </footer>
        </main>
      </div>
    </div>
  );
}
