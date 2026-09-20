import { Check, Plug, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type ConnectionCheck, type Credentials, type DatabaseAccess, type Project } from "../../api";
import { CodeBlock } from "../../components/ui/code-block";
import { Card, CardHeader } from "../../components/ui/card";
import { Banner, ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { DetailsList } from "../../components/ui/details-list";
import { Pill } from "../../components/ui/pill";
import { Tabs } from "../../components/ui/tabs";
import { ConnectionUrl, maskedUrl } from "./ConnectionUrl";

type Snippet = "psql" | "node" | "python" | "prisma";
export function ConnectTab({ project, access, credentials, revealed, credentialBusy, credentialError, onReveal, onHide, onCopy }: { project: Project; access: DatabaseAccess; credentials: Credentials | null; revealed: boolean; credentialBusy: boolean; credentialError: string; onReveal: () => void; onHide: () => void; onCopy: () => void }) {
  const [snippet, setSnippet] = useState<Snippet>("psql"); const [check, setCheck] = useState<ConnectionCheck | null>(null); const [error, setError] = useState("");
  const sslmode = access.mode === "tunnel" ? "disable" : access.certificate.state === "trusted" ? "verify-full" : "require";
  const url = credentials ? (revealed ? credentials.url : credentials.url.replace(credentials.password, "••••••••")) : maskedUrl(project, access.host, sslmode);
  async function start() { setError(""); try { setCheck(await api<ConnectionCheck>(`/projects/${project.id}/connection-checks`, { method: "POST", body: "{}" })); } catch (failure) { setError((failure as Error).message); } }
  useEffect(() => { if (!check || check.state !== "pending") return; const timer = setInterval(() => void api<ConnectionCheck>(`/projects/${project.id}/connection-checks/${check.id}`).then((next) => setCheck({ ...check, ...next })).catch((failure) => setError((failure as Error).message)), 2000); return () => clearInterval(timer); }, [check?.id, check?.state]);
  const snippets: Record<Snippet, string> = {
    psql: credentials ? (revealed ? credentials.psql : credentials.psql.replace(credentials.password, "••••••••")) : `psql "${url}"`,
    node: `// npm install pg\nimport pg from "pg";\nconst client = new pg.Client({ connectionString: process.env.DATABASE_URL });\nawait client.connect();`,
    python: `# pip install "psycopg[binary]"\nimport os, psycopg\nwith psycopg.connect(os.environ["DATABASE_URL"]${access.mode === "direct" ? ', sslrootcert="system"' : ""}) as conn:\n    print(conn.execute("select now()").fetchone())`,
    prisma: `DATABASE_URL="${url}"\n\ndatasource db {\n  provider = "postgresql"\n  url      = env("DATABASE_URL")\n}`,
  };
  return <div className="tab-stack">
    {access.mode === "tunnel" && <Card><CardHeader title="Open an SSH tunnel first" /><CodeBlock value="ssh -N -L 5432:127.0.0.1:5432 user@your-server" /><p>The tunnel securely forwards your local PostgreSQL port to this server.</p></Card>}
    {access.mode === "direct" && access.certificate.state !== "trusted" && <Banner tone="warn"><TriangleAlert size={17} />Certificate not issued yet — connections work with <code>sslmode=require</code>; run <code>sudo pgfyctl sync-db-cert</code> on the server to fix.</Banner>}
    <Card><CardHeader title="Connection URL" /><ConnectionUrl project={project} host={access.host} sslmode={sslmode} credentials={credentials} revealed={revealed} busy={credentialBusy} error={credentialError} onReveal={onReveal} onHide={onHide} onCopy={onCopy} />
      <details><summary>Details</summary>{credentials ? <DetailsList items={[{ label: "Host", value: credentials.host, copy: credentials.host }, { label: "Port", value: credentials.port, copy: String(credentials.port) }, { label: "Database", value: credentials.database, copy: credentials.database }, { label: "User", value: credentials.user, copy: credentials.user }, { label: "Password", value: revealed ? credentials.password : "••••••••", copy: credentials.password }, { label: "SSL mode", value: credentials.sslmode, copy: credentials.sslmode }]} /> : <p>Reveal the connection URL to load these details.</p>}</details>
    </Card>
    <Card><CardHeader title="Code examples" /><Tabs value={snippet} onChange={setSnippet} options={[{ value: "psql", label: "psql" }, { value: "node", label: "Node.js" }, { value: "python", label: "Python" }, { value: "prisma", label: "Prisma" }]} /><CodeBlock value={snippets[snippet]} /><details><summary>About TLS verification</summary><p><code>verify-full</code> checks the server certificate and hostname. Libpq-based tools also use the system certificate store.</p></details></Card>
    <Card><CardHeader title="Test the connection" aside={check && <Pill tone={check.state === "successful" ? "good" : check.state === "pending" ? "wait" : "bad"}>{check.state === "successful" ? `Connected from ${check.evidence?.client_addr || "the SSH tunnel"}` : check.state === "pending" ? "Waiting…" : "Expired"}</Pill>} /><p>Run a temporary command where your app runs to confirm it can reach this database.</p>{!check ? <Button variant="secondary" onClick={() => void start()}><Plug size={15} />Start test</Button> : <>{check.command && <CodeBlock label="Run this where your app runs (valid 10 min)" value={check.command} />}{check.state === "successful" && <Banner><Check size={17} />Your app can reach this database.</Banner>}{check.state === "expired" && <Button variant="secondary" onClick={() => void start()}>Start again</Button>}</>}{error && <ErrorNotice message={error} />}</Card>
  </div>;
}
