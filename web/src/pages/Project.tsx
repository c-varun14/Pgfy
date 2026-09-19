import { useEffect, useState } from "react";
import {
  ArrowLeft,
  Check,
  Eye,
  EyeOff,
  Globe,
  KeyRound,
  Plug,
  RefreshCw,
  ShieldCheck,
  TriangleAlert,
  X,
} from "lucide-react";
import { Button } from "../components/ui/button";
import {
  api,
  formatBytes,
  type ConnectionCheck,
  type Credentials,
  type DatabaseAccess,
  type PolicyState,
  type Project,
  type Session,
} from "../api";
import { CodeBlock, ErrorNotice, Notice, Pill } from "../ui";
import { stageLabel } from "./Projects";
import { BackupsPanel } from "./Backups";

const STAGES: Project["stage"][] = ["identity_persisted", "role_created", "database_created", "ready"];
const STAGE_TEXT: Record<Project["stage"], string> = {
  identity_persisted: "Saving project identity",
  role_created: "Creating a dedicated database user",
  database_created: "Creating the database",
  ready: "Ready",
};
const ANYWHERE = ["0.0.0.0/0", "::/0"];

export function ProjectPage({ id, session, navigate }: { id: string; session: Session; navigate: (to: string) => void }) {
  const [project, setProject] = useState<Project | null>(null);
  const [access, setAccess] = useState<DatabaseAccess | null>(null);
  const [error, setError] = useState("");
  const [notFound, setNotFound] = useState(false);

  async function load() {
    try {
      const body = await api<{ project: Project; database_access: DatabaseAccess }>(`/projects/${id}`);
      setProject(body.project);
      setAccess(body.database_access);
      setError("");
    } catch (e) {
      if ((e as { status?: number }).status === 404) setNotFound(true);
      else setError((e as Error).message);
    }
  }
  useEffect(() => {
    void load();
  }, [id]);
  const ready = project?.stage === "ready";
  useEffect(() => {
    if (!project || project.failed) return;
    const timer = setInterval(() => void load(), ready ? 10000 : 2000);
    return () => clearInterval(timer);
  }, [project?.stage, project?.failed]);

  if (notFound)
    return (
      <section className="panel empty">
        <h2>Project not found</h2>
        <Button variant="ghost" onClick={() => navigate("/")}>
          <ArrowLeft size={15} /> Back to projects
        </Button>
      </section>
    );
  if (!project || !access) return error ? <ErrorNotice message={error} /> : <p role="status">Loading project…</p>;

  const stage = stageLabel(project);
  const connected = (project.connections_now?.length ?? 0) > 0;
  return (
    <>
      <Button variant="ghost" className="back" onClick={() => navigate("/")}>
        <ArrowLeft size={15} /> All projects
      </Button>
      {error && <ErrorNotice message={error} />}
      <div className="status-row">
        <div className="status-item">
          <span className={`status-dot ${ready ? "" : "bad"}`} />
          <div>
            <strong>{ready ? "Database ready" : project.failed ? "Creation failed" : "Creating database"}</strong>
            <small>{ready ? `${project.db_name} · ${formatBytes(project.size_bytes)}` : STAGE_TEXT[project.stage]}</small>
          </div>
        </div>
        <div className="status-item">
          <span className={`status-dot ${connected ? "" : "idle"}`} />
          <div>
            <strong>{connected ? "Application connected" : "No application connected"}</strong>
            <small>
              {connected
                ? `${project.connections_now!.length} live session${project.connections_now!.length === 1 ? "" : "s"} observed just now`
                : "Observed live sessions appear here"}
            </small>
          </div>
        </div>
        <Pill tone={stage.tone}>{stage.text}</Pill>
      </div>
      {!ready && <ProvisioningPanel project={project} onRetry={load} />}
      {ready && (
        <>
          <ConnectionPanel project={project} access={access} />
          <BackupsPanel project={project} navigate={navigate} />
          <AccessPanel project={project} session={session} onChange={load} />
          <CheckPanel project={project} />
        </>
      )}
    </>
  );
}

function ProvisioningPanel({ project, onRetry }: { project: Project; onRetry: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = STAGES.indexOf(project.stage);
  async function retry() {
    setBusy(true);
    setError("");
    try {
      await api(`/projects/${project.id}/retry`, { method: "POST", body: "{}" });
      await onRetry();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>{project.failed ? "Something went wrong" : "Setting things up"}</h2>
        {!project.failed && <RefreshCw size={15} className="spin muted" />}
      </div>
      <ol className="steps">
        {STAGES.slice(1).map((s, i) => {
          const done = current > i;
          const active = !project.failed && current === i;
          return (
            <li key={s} className={done ? "done" : active ? "active" : project.failed && current === i ? "failed" : ""}>
              {done ? <Check size={14} /> : project.failed && current === i ? <TriangleAlert size={14} /> : <span className="step-dot" />}
              {STAGE_TEXT[s]}
            </li>
          );
        })}
      </ol>
      {project.failed && (
        <>
          <ErrorNotice message={project.stage_error} />
          <p className="muted small">Retrying resumes from the last completed step with the same identity; nothing is duplicated.</p>
          <Button onClick={() => void retry()} disabled={busy}>
            <RefreshCw size={15} /> {busy ? "Retrying…" : "Retry"}
          </Button>
        </>
      )}
      {error && <ErrorNotice message={error} />}
    </section>
  );
}

function ConnectionPanel({ project, access }: { project: Project; access: DatabaseAccess }) {
  const [credentials, setCredentials] = useState<Credentials | null>(null);
  const [show, setShow] = useState(false);
  const [error, setError] = useState("");
  const [snippet, setSnippet] = useState<"psql" | "node" | "python">("psql");
  async function reveal() {
    setError("");
    try {
      setCredentials(await api<Credentials>(`/projects/${project.id}/credentials`));
    } catch (e) {
      setError((e as Error).message);
    }
  }
  const masked = credentials ? credentials.url.replace(credentials.password, "••••••••") : "";
  const url = credentials ? (show ? credentials.url : masked) : "";
  const trusted = access.certificate.state === "trusted";
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Connect your application</h2>
        {access.mode === "direct" ? (
          <Pill tone={trusted ? "good" : "wait"}>
            <ShieldCheck size={12} /> {trusted ? "TLS · verified certificate" : "TLS · certificate pending"}
          </Pill>
        ) : (
          <Pill tone="neutral">SSH tunnel</Pill>
        )}
      </div>
      {access.mode === "tunnel" && (
        <>
          <p className="muted small">This installation has no public address. Forward the database port through SSH first, then connect to localhost.</p>
          <CodeBlock label="On your computer" value="ssh -N -L 5432:127.0.0.1:5432 user@your-server" />
        </>
      )}
      {access.mode === "direct" && !trusted && (
        <Notice>
          <TriangleAlert size={18} />
          <span>
            The database certificate is not issued yet, so clients cannot verify the server identity. Run <code>sudo pgfyctl sync-db-cert</code> on the host after the dashboard certificate exists, or use <code>sslmode=require</code> until then.
          </span>
        </Notice>
      )}
      {!credentials ? (
        <div className="reveal">
          <p className="muted small">Credentials are stored encrypted on your server and shown only to you.</p>
          <Button variant="outline" onClick={() => void reveal()}>
            <KeyRound size={15} /> Show connection details
          </Button>
        </div>
      ) : (
        <>
          <div className="code-block">
            <span className="code-label">DATABASE_URL</span>
            <pre>
              <code>{url}</code>
            </pre>
            <div className="code-actions">
              <Button variant="outline" className="copy" type="button" onClick={() => setShow(!show)}>
                {show ? <EyeOff size={14} /> : <Eye size={14} />} {show ? "Hide" : "Show"} password
              </Button>
              <CopyValue value={credentials.url} />
            </div>
          </div>
          <dl className="details compact">
            {(
              [
                ["Host", credentials.host],
                ["Port", String(credentials.port)],
                ["Database", credentials.database],
                ["User", credentials.user],
                ["Password", show ? credentials.password : "••••••••"],
                ["SSL mode", credentials.sslmode],
              ] as const
            ).map(([k, v]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd className="mono">{v}</dd>
              </div>
            ))}
          </dl>
          <div className="tabs" role="tablist">
            {(["psql", "node", "python"] as const).map((t) => (
              <button key={t} role="tab" aria-selected={snippet === t} className={snippet === t ? "active" : ""} onClick={() => setSnippet(t)}>
                {t === "psql" ? "psql" : t === "node" ? "Node.js" : "Python"}
              </button>
            ))}
          </div>
          {snippet === "psql" && <CodeBlock value={show ? credentials.psql : credentials.psql.replace(credentials.password, "••••••••")} />}
          {snippet === "node" && (
            <CodeBlock
              value={`// npm install pg\nimport pg from "pg";\nconst client = new pg.Client({ connectionString: process.env.DATABASE_URL });\nawait client.connect();\nconsole.log((await client.query("select now()")).rows[0]);`}
            />
          )}
          {snippet === "python" && (
            <CodeBlock
              value={`# pip install "psycopg[binary]"\nimport os, psycopg\nwith psycopg.connect(os.environ["DATABASE_URL"]) as conn:\n    print(conn.execute("select now()").fetchone())`}
            />
          )}
          {access.mode === "direct" && (
            <p className="muted small">
              <code>sslmode=verify-full</code> checks the certificate against your system’s trusted authorities (libpq 16+ uses them automatically; older clients need <code>sslrootcert=system</code>). Set <code>DATABASE_URL</code> in your app’s environment; never commit it.
            </p>
          )}
        </>
      )}
      {error && <ErrorNotice message={error} />}
    </section>
  );
}

function CopyValue({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="outline"
      className="copy"
      type="button"
      onClick={() => void navigator.clipboard?.writeText(value).then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })}
    >
      {copied ? <Check size={14} /> : null} {copied ? "Copied" : "Copy URL"}
    </Button>
  );
}

function AccessPanel({ project, session, onChange }: { project: Project; session: Session; onChange: () => Promise<void> }) {
  const policy = project.policy as PolicyState;
  const [addresses, setAddresses] = useState<string[]>(policy.addresses);
  const [revision, setRevision] = useState(policy.current_revision);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (policy.current_revision !== revision) {
      setAddresses(policy.addresses);
      setRevision(policy.current_revision);
    }
  }, [policy.current_revision]);
  const anywhere = addresses.some((a) => ANYWHERE.includes(a));
  const dirty = JSON.stringify(addresses) !== JSON.stringify(policy.addresses);
  const myIP = session.client_ip && !session.client_ip.startsWith("127.") ? session.client_ip : "";

  function add(value: string) {
    const v = value.trim();
    if (!v || addresses.includes(v)) return;
    setAddresses(addresses.filter((a) => !ANYWHERE.includes(a)).concat(v));
    setInput("");
  }
  async function save(next = addresses) {
    setBusy(true);
    setError("");
    try {
      const result = await api<PolicyState>(`/projects/${project.id}/access`, {
        method: "PUT",
        body: JSON.stringify({ revision, addresses: next }),
      });
      setAddresses(result.addresses);
      setRevision(result.current_revision);
      if (result.state === "failed") setError(result.last_error || "The rules could not be applied.");
      await onChange();
    } catch (e) {
      setError((e as Error).message);
      if ((e as { status?: number }).status === 409) await onChange();
    } finally {
      setBusy(false);
    }
  }
  const tone = policy.state === "applied" ? "good" : policy.state === "failed" ? "bad" : "wait";
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Who can connect</h2>
        <Pill tone={tone}>
          {policy.state === "applied" ? "Rules active" : policy.state === "failed" ? "Not applied" : "Applying…"}
        </Pill>
      </div>
      <p className="muted small">
        {anywhere
          ? "Open to the internet: any address can connect with TLS and this project’s password. Restrict it to your application’s server for defence in depth."
          : "Only the addresses below can reach this database. Your provider’s firewall must also allow port 5432. Removing an address does not end sessions that are already open."}
      </p>
      <div className="chips">
        {addresses.map((a) => (
          <span key={a} className="chip mono">
            {ANYWHERE.includes(a) ? <Globe size={12} /> : null}
            {a === "0.0.0.0/0" ? "Anywhere (IPv4)" : a === "::/0" ? "Anywhere (IPv6)" : a}
            <button type="button" aria-label={`Remove ${a}`} onClick={() => setAddresses(addresses.filter((x) => x !== a))}>
              <X size={12} />
            </button>
          </span>
        ))}
        {addresses.length === 0 && <span className="muted small">No addresses: nothing can connect remotely until you add one.</span>}
      </div>
      <div className="chip-input">
        <input
          aria-label="IP address or CIDR"
          placeholder="Server IP or CIDR, e.g. 203.0.113.10 or 10.0.0.0/24"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add(input);
            }
          }}
        />
        <Button variant="outline" type="button" onClick={() => add(input)} disabled={!input.trim()}>
          Add
        </Button>
        {myIP && (
          <Button variant="outline" type="button" onClick={() => add(myIP)}>
            Add my IP ({myIP})
          </Button>
        )}
        {!anywhere && (
          <Button variant="outline" type="button" onClick={() => setAddresses(ANYWHERE)}>
            <Globe size={14} /> Allow anywhere
          </Button>
        )}
      </div>
      <p className="muted small">
        Note: your current IP is where <em>you</em> browse from. Your application’s server usually has a different outbound IP; add that one for production.
      </p>
      {error && <ErrorNotice message={error} />}
      <div className="actions">
        <Button onClick={() => void save()} disabled={busy || !dirty}>
          {busy ? "Applying…" : "Apply rules"}
        </Button>
        {dirty && (
          <Button variant="ghost" type="button" onClick={() => setAddresses(policy.addresses)}>
            Discard changes
          </Button>
        )}
      </div>
    </section>
  );
}

function CheckPanel({ project }: { project: Project }) {
  const [check, setCheck] = useState<ConnectionCheck | null>(null);
  const [error, setError] = useState("");
  async function start() {
    setError("");
    try {
      setCheck(await api<ConnectionCheck>(`/projects/${project.id}/connection-checks`, { method: "POST", body: "{}" }));
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    if (!check || check.state !== "pending") return;
    const timer = setInterval(() => {
      void api<ConnectionCheck>(`/projects/${project.id}/connection-checks/${check.id}`)
        .then((latest) => setCheck({ ...check, ...latest }))
        .catch((e) => setError((e as Error).message));
    }, 2000);
    return () => clearInterval(timer);
  }, [check?.id, check?.state]);
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Test from your application’s environment</h2>
        {check && (
          <Pill tone={check.state === "successful" ? "good" : check.state === "pending" ? "wait" : "bad"}>
            {check.state === "successful" ? "Connection observed" : check.state === "pending" ? "Waiting…" : "Expired"}
          </Pill>
        )}
      </div>
      <p className="muted small">
        Run the command below where your application runs. It opens a short TLS session; Pgfy watches for it and shows exactly what the server saw.
      </p>
      {!check ? (
        <Button variant="outline" onClick={() => void start()}>
          <Plug size={15} /> Start a connection test
        </Button>
      ) : (
        <>
          {check.command && <CodeBlock label="Run within 10 minutes" value={check.command} />}
          {check.state === "successful" && check.evidence && (
            <Notice>
              <Check size={18} />
              <span>
                Seen from <strong className="mono">{check.evidence.client_addr || "the SSH tunnel"}</strong>
                {check.evidence.tls ? " over TLS" : " without TLS"} at {new Date(check.evidence.observed_at * 1000).toLocaleTimeString()}. Your application can reach this database.
              </span>
            </Notice>
          )}
          {check.state === "expired" && (
            <Button variant="outline" onClick={() => void start()}>
              Start again
            </Button>
          )}
        </>
      )}
      {error && <ErrorNotice message={error} />}
    </section>
  );
}
