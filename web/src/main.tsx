import {
  StrictMode,
  useCallback,
  useEffect,
  useState,
  type FormEvent,
} from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowRight,
  Check,
  Database,
  ExternalLink,
  LogOut,
  RefreshCw,
  Server,
  Settings2,
  ShieldCheck,
  Terminal,
  TriangleAlert,
} from "lucide-react";
import { Button } from "./components/ui/button";
import { api, APIError, type Session, type Settings, type Status } from "./api";
import "./style.css";

function Brand() {
  return (
    <a href="/" className="brand" aria-label="Pgfy home">
      <span className="brand-icon">
        <Database size={22} />
      </span>
      pgfy<span className="brand-dot">.</span>
    </a>
  );
}
function ErrorNotice({ message }: { message: string }) {
  return (
    <div className="notice error" role="alert">
      <TriangleAlert size={18} />
      <span>{message}</span>
    </div>
  );
}
function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [setup, setSetup] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [page, setPage] = useState<"status" | "settings">("status");
  const [status, setStatus] = useState<Status | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    setError("");
    setRefreshing(true);
    try {
      const current = await api<Session>("/auth/session");
      setSession(current);
      const [health, installation] = await Promise.all([
        api<Status>("/system/status"),
        api<Settings>("/settings"),
      ]);
      setStatus(health);
      setSettings(installation);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) {
        setSession(null);
        setPage("status");
        setStatus(null);
        setSettings(null);
        try {
          setSetup((await api<{ available: boolean }>("/setup")).available);
        } catch (failure) {
          setError((failure as Error).message);
        }
      } else {
        setError((e as Error).message);
        setStatus(null);
        setSettings(null);
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
      await api("/auth/logout", {
        method: "POST",
        headers: { "X-CSRF-Token": session!.csrf_token },
        body: "{}",
      });
      setSession(null);
      setPage("status");
      setStatus(null);
      setSettings(null);
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
  if (!session)
    return (
      <div className="auth-layout">
        <aside className="auth-aside">
          <Brand />
          <div>
            <span className="eyebrow">A home for your data</span>
            <h1>
              Your server.
              <br />
              Your databases.
              <br />
              <em>Your control.</em>
            </h1>
            <p>
              A small, deliberate foundation for the things you’re building.
            </p>
          </div>
          <span className="aside-footer">
            <Database size={16} />
            Self-hosted PostgreSQL
          </span>
        </aside>
        <main className="auth-main">
          <div className="auth-card">
            <span className="eyebrow">
              {setup ? "Welcome to Pgfy" : "Welcome back"}
            </span>
            <h2>
              {setup ? "Make yourself at home." : "Sign in to your server."}
            </h2>
            <p className="muted">
              {setup
                ? "Create the administrator account for this installation."
                : "Your databases stay on the server you control."}
            </p>
            {error && (
              <>
                <ErrorNotice message={error} />
                <Button variant="outline" onClick={() => void load()}>
                  Retry connection
                </Button>
              </>
            )}
            <AuthForm setup={setup} onSuccess={load} />
            <p className="fine-print">
              <ShieldCheck size={15} />
              {location.protocol === "https:"
                ? "Connected over HTTPS"
                : "HTTP access — use only through your SSH tunnel"}
            </p>
          </div>
        </main>
      </div>
    );

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
          <button
            className={page === "status" ? "active" : ""}
            onClick={() => setPage("status")}
          >
            <Activity size={18} />
            Overview
          </button>
          <button
            className={page === "settings" ? "active" : ""}
            onClick={() => setPage("settings")}
          >
            <Settings2 size={18} />
            Settings
          </button>
        </nav>
        <div className="sidebar-bottom">
          <span className="server-label">
            <span className="dot" />
            Self-hosted installation
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
            Workspace <span className="slash">/</span>{" "}
            {page === "status" ? "Overview" : "Settings"}
          </span>
          <span className="topbar-host">
            <Server size={14} />
            {settings?.hostname || "SSH tunnel"}
          </span>
          <Button
            className="mobile-signout"
            variant="ghost"
            onClick={() => void logout()}
          >
            <LogOut size={15} />
            Sign out
          </Button>
        </header>
        <main className="content">
          <div className="page-heading">
            <div>
              <span className="eyebrow">YOUR POSTGRESQL FOUNDATION</span>
              <h1>
                {page === "status"
                  ? "A place to build."
                  : "Installation settings."}
              </h1>
              <p className="muted">
                {page === "status"
                  ? "A clear view of your server and its core services."
                  : "The essentials, managed from your host."}
              </p>
            </div>
            <Button
              variant="outline"
              disabled={refreshing}
              onClick={() => void load()}
            >
              <RefreshCw size={15} className={refreshing ? "spin" : ""} />
              {refreshing ? "Refreshing" : "Refresh"}
            </Button>
          </div>
          {error && <ErrorNotice message={error} />}
          {page === "status" && status ? (
            <>
              <div
                className={`health-banner ${status.ready ? "" : "degraded"}`}
              >
                <span className="health-icon">
                  {status.ready ? (
                    <Check size={22} />
                  ) : (
                    <TriangleAlert size={22} />
                  )}
                </span>
                <div>
                  <h2>
                    {status.ready
                      ? "Your foundation is ready"
                      : "Your server needs attention"}
                  </h2>
                  <p>
                    {status.ready
                      ? "The application, management storage, and PostgreSQL are responding."
                      : "The dashboard is available. Check the service status below and run host diagnostics."}
                  </p>
                </div>
                <span className="badge">
                  {status.ready ? "Operational" : "Degraded"}
                </span>
              </div>
              <div className="service-grid">
                <Service
                  title="Application"
                  detail="Dashboard & authentication"
                  value={status.versions.application}
                  good
                  icon={<Server size={20} />}
                />
                <Service
                  title="PostgreSQL"
                  detail="Internal database service"
                  value={status.postgres.version || "Connection unavailable"}
                  good={status.postgres.status === "available"}
                  icon={<Database size={20} />}
                />
                <Service
                  title="Management storage"
                  detail="Persistent SQLite metadata"
                  value={status.sqlite.version}
                  good={status.sqlite.status === "available"}
                  icon={<ShieldCheck size={20} />}
                />
              </div>
              <section className="panel next-panel">
                <div>
                  <span className="section-icon">
                    <Database size={23} />
                  </span>
                  <h2>Your foundation comes first.</h2>
                  <p>
                    This release provides administration and service health.
                    Project management and backups are not available yet.
                  </p>
                  <span className="subtle-tag">Backups not configured</span>
                </div>
                <div className="next-steps">
                  <h3>Installation defaults</h3>
                  <p>
                    <Check size={16} />
                    Private PostgreSQL networking
                  </p>
                  <p>
                    <Check size={16} />
                    Single-admin authentication
                  </p>
                  <p>
                    <Check size={16} />
                    Persistent installation state
                  </p>
                  <Button variant="ghost" onClick={() => setPage("settings")}>
                    View installation settings <ArrowRight size={15} />
                  </Button>
                </div>
              </section>
              <section className="panel">
                <div className="panel-heading">
                  <h2>Runtime versions</h2>
                  <span className="muted">Reported by the application</span>
                </div>
                <dl className="details">
                  {Object.entries(status.versions).map(([key, value]) => (
                    <div key={key}>
                      <dt>{key}</dt>
                      <dd>{value}</dd>
                    </div>
                  ))}
                </dl>
              </section>
            </>
          ) : page === "settings" && settings ? (
            <>
              <section className="panel">
                <div className="panel-heading">
                  <h2>Connection & installation</h2>
                  <span className="subtle-tag">Read-only</span>
                </div>
                <dl className="details">
                  <div>
                    <dt>Dashboard address</dt>
                    <dd>
                      <a href={settings.origin}>
                        {settings.origin}
                        <ExternalLink size={13} />
                      </a>
                    </dd>
                  </div>
                  <div>
                    <dt>Access mode</dt>
                    <dd>
                      {settings.mode === "https"
                        ? "Public HTTPS"
                        : "Loopback · SSH tunnel only"}
                    </dd>
                  </div>
                  <div>
                    <dt>Installation ID</dt>
                    <dd>{settings.id}</dd>
                  </div>
                  <div>
                    <dt>Installed release</dt>
                    <dd>{settings.release}</dd>
                  </div>
                </dl>
                <div className="notice">
                  <Terminal size={18} />
                  <span>
                    Change the hostname or recover access with{" "}
                    <code>pgfyctl</code> on your server. Changes require signing
                    in again.
                  </span>
                </div>
              </section>
              <section className="panel">
                <div className="panel-heading">
                  <h2>Host components</h2>
                  <span className="muted">Recorded during installation</span>
                </div>
                <dl className="details">
                  <div>
                    <dt>Caddy</dt>
                    <dd>{settings.caddy_version}</dd>
                  </div>
                  <div>
                    <dt>Docker Engine</dt>
                    <dd>{settings.docker_version}</dd>
                  </div>
                  <div>
                    <dt>Docker Compose</dt>
                    <dd>{settings.compose_version}</dd>
                  </div>
                </dl>
              </section>
              <section className="panel">
                <h2>Backups not configured</h2>
                <p className="muted">
                  External backups are not available in this release. This
                  installation does not yet protect data against server loss.
                </p>
              </section>
            </>
          ) : (
            !error && <p role="status">Loading installation details…</p>
          )}
          <footer className="footer">
            Pgfy <span>Small foundation. Room to grow.</span>
          </footer>
        </main>
      </div>
    </div>
  );
}
function Service({
  title,
  detail,
  value,
  good,
  icon,
}: {
  title: string;
  detail: string;
  value: string;
  good: boolean;
  icon: React.ReactNode;
}) {
  return (
    <section className="panel service">
      <div className="service-top">
        <span className="section-icon">{icon}</span>
        <span className={`status-dot ${good ? "" : "bad"}`} />
      </div>
      <h2>{title}</h2>
      <p>{detail}</p>
      <div className="service-value">
        {value || "Unavailable"}
        <span>{good ? "Responding" : "Needs attention"}</span>
      </div>
    </section>
  );
}
function AuthForm({
  setup,
  onSuccess,
}: {
  setup: boolean;
  onSuccess: () => Promise<void>;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(event.currentTarget);
    try {
      await api(setup ? "/setup" : "/auth/login", {
        method: "POST",
        body: JSON.stringify({
          email: data.get("email"),
          password: data.get("password"),
          ...(setup ? { token: data.get("token") } : {}),
        }),
      });
      await onSuccess();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)}>
      {setup && (
        <div className="field">
          <label htmlFor="setup-token">Setup token</label>
          <input
            id="setup-token"
            name="token"
            type="password"
            autoComplete="off"
            required
            maxLength={43}
            aria-describedby="token-hint"
          />
          <small id="token-hint">
            Copy the 30-minute token from your installer terminal.
          </small>
        </div>
      )}
      <div className="field">
        <label htmlFor="email">Email address</label>
        <input
          id="email"
          name="email"
          type="email"
          placeholder="you@example.com"
          autoComplete="username"
          required
          maxLength={254}
        />
      </div>
      <div className="field">
        <label htmlFor="password">Password</label>
        <input
          id="password"
          name="password"
          type="password"
          autoComplete={setup ? "new-password" : "current-password"}
          required
          minLength={setup ? 15 : undefined}
          maxLength={256}
          aria-describedby={setup ? "password-hint" : undefined}
        />
        {setup && (
          <small id="password-hint">
            Use at least 15 characters. A passphrase works well.
          </small>
        )}
      </div>
      {error && <ErrorNotice message={error} />}
      <Button disabled={busy} type="submit">
        {busy ? "Please wait…" : setup ? "Create administrator" : "Sign in"}
        <ArrowRight size={17} />
      </Button>
    </form>
  );
}
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
