import { ExternalLink } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type Alerts, type AlertSettings, type BackupPolicy, type ConnectionBudget, type HostStatus, type RoleUse, type Settings, type Status } from "../api";
import { Button } from "../components/ui/button";
import { formatBytes, formatDate, relativeTime } from "../lib/format";
import { PageHeader } from "../components/PageHeader";
import { Banner } from "../components/ui/banner";
import { Card, CardHeader } from "../components/ui/card";
import { CopyButton } from "../components/ui/copy-button";
import { DetailsList } from "../components/ui/details-list";
import { Pill } from "../components/ui/pill";
import { SegmentedControl } from "../components/ui/segmented-control";
import { ErrorNotice } from "../components/ui/banner";
import { useToast } from "../components/ui/toast";
import { type ThemePreference, useTheme } from "../theme";

const INTERVALS = [24, 12, 6, 1];
function intervalLabel(hours: number) { return hours === 1 ? "Every hour" : hours === 24 ? "Every day" : `Every ${hours} hours`; }

/** The interval is a target the scheduler works towards, not a promise: one
 *  backup runs at a time and a failing database is retried with a backoff. */
function BackupsCard() {
  const [policy, setPolicy] = useState<BackupPolicy | null>(null); const [error, setError] = useState(""); const [busy, setBusy] = useState(false); const { showToast } = useToast();
  useEffect(() => { void api<BackupPolicy>("/settings/backups").then(setPolicy).catch((failure) => setError((failure as Error).message)); }, []);
  async function choose(hours: number) {
    if (!policy || hours === policy.target_interval_hours) return;
    setBusy(true); setError("");
    try { setPolicy(await api<BackupPolicy>("/settings/backups", { method: "PUT", body: JSON.stringify({ ...policy, target_interval_hours: hours }) })); showToast("Backup target updated"); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  return <Card><CardHeader title="Backups" />
    {policy ? <>
      <div className="setting-row">
        <label htmlFor="backup-interval">Back up each database</label>
        <select id="backup-interval" value={policy.target_interval_hours} disabled={busy} onChange={(event) => void choose(Number(event.target.value))}>
          {INTERVALS.map((hours) => <option key={hours} value={hours}>{intervalLabel(hours)}</option>)}
        </select>
      </div>
      <p className="caption">A target, not a guarantee: one backup runs at a time, and a database whose backup fails is retried after 15 minutes, an hour, then four hours. The dashboard warns when the newest recoverable backup is older than one and a half times this.</p>
      <p className="caption">Kept in your bucket: the newest backup of each database, then {policy.retention_daily} daily and {policy.retention_weekly} weekly. Older ones are deleted after a successful backup.</p>
    </> : !error && <p>Loading backup settings…</p>}
    {error && <ErrorNotice message={error} />}
  </Card>;
}

/** What PostgreSQL will accept from project users, what they use now, and the
 *  slots held back so the dashboard and health checks are never locked out. */
function ConnectionsCard() {
  const [budget, setBudget] = useState<ConnectionBudget | null>(null); const [error, setError] = useState("");
  useEffect(() => { const load = () => void api<ConnectionBudget>("/system/connections").then((next) => { setBudget(next); setError(""); }).catch((failure) => setError((failure as Error).message)); load(); const timer = setInterval(load, 15000); return () => clearInterval(timer); }, []);
  const used = budget ? budget.projects_used + budget.other_used : 0; const share = budget && budget.available > 0 ? Math.min(100, Math.round((used / budget.available) * 100)) : 0;
  const row = (u: RoleUse) => <tr key={u.role}><td>{u.project || u.role}</td><td className="mono">{u.connections} / {u.limit === -1 ? "∞" : u.limit}</td><td>{u.warning && <Pill tone="bad">Near limit</Pill>}</td></tr>;
  return <Card><CardHeader title="Connections" aside={budget && <Pill tone={budget.warning ? "bad" : "good"}>{used} of {budget.available} in use</Pill>} />
    {budget ? <>
      <div className={`budget-meter${budget.warning ? " warn" : ""}`} role="meter" aria-valuemin={0} aria-valuemax={budget.available} aria-valuenow={used} aria-label="Connections in use"><span style={{ width: `${share}%` }} /></div>
      {budget.warning && <Banner tone="warn">Connections are above 80% of what this server accepts. Lower per-database limits or close idle connections.</Banner>}
      <p className="caption">PostgreSQL accepts {budget.max_connections} connections: {budget.reserved} are reserved for the dashboard and health checks and {budget.superuser_reserved} for maintenance, leaving {budget.available} for your databases. Their limits add up to {budget.projects_limit}{budget.overcommitted ? ", more than can be open at once — fine while they are not all busy" : ""}.</p>
      <table className="plain-table"><thead><tr><th>Database</th><th>Open / limit</th><th /></tr></thead><tbody>{budget.roles.map(row)}{budget.system.map(row)}</tbody></table>
    </> : !error && <p>Loading connection use…</p>}
    {error && <ErrorNotice message={error} />}
  </Card>;
}

const DISK_NAMES: Record<string, string> = { postgres: "PostgreSQL data", workspace: "Backup workspace", root: "System disk" };
/** Disk and clock as the host records them every five minutes. */
function HostCard({ host }: { host?: HostStatus }) {
  if (!host) return null;
  const clock = host.ntp_synchronized === true ? "Synchronised" : host.ntp_synchronized === false ? "Not synchronised" : "Unknown";
  const tone = host.state !== "ok" || host.disks.some((d) => d.low || d.error) || host.ntp_synchronized === false ? "bad" : "good";
  return <Card><CardHeader title="Host" aside={<Pill tone={tone}>{host.state === "unknown" ? "No report" : host.state === "stale" ? "Report stale" : tone === "good" ? "Healthy" : "Needs attention"}</Pill>} />
    {host.state === "unknown" && <Banner tone="warn">The host has not reported disk and clock status. On the server, check <code>systemctl status pgfy-host-status.timer</code> and run <code>sudo pgfyctl diagnostics</code>.</Banner>}
    {host.state === "stale" && <Banner tone="warn">The last host report is from {relativeTime(host.written_at)}; the status timer may have stopped. Check <code>systemctl status pgfy-host-status.timer</code> on the server.</Banner>}
    {host.state !== "unknown" && <DetailsList items={[
      ...host.disks.map((d) => ({ label: DISK_NAMES[d.name] || d.name, value: d.error ? "Could not be measured" : <span>{Math.round(d.free_percent)}% free · {formatBytes(d.free_bytes ?? null)} of {formatBytes(d.total_bytes ?? null)}{d.low && <> <Pill tone="bad">Low</Pill></>}</span> })),
      { label: "Clock", value: <span>{clock}{host.ntp_synchronized === false && <> <Pill tone="bad">Sign-in codes and certificates depend on it</Pill></>}</span> },
      { label: "Reported", value: <time title={formatDate(host.written_at)}>{relativeTime(host.written_at)}</time> },
    ]} />}
  </Card>;
}

/** One generic JSON webhook. The URL is often the credential, so only its host is ever shown back. */
function AlertsCard() {
  const [settings, setSettings] = useState<AlertSettings | null>(null); const [alerts, setAlerts] = useState<Alerts | null>(null);
  const [url, setUrl] = useState(""); const [secret, setSecret] = useState(""); const [privateEndpoint, setPrivate] = useState(false);
  const [busy, setBusy] = useState(false); const [testing, setTesting] = useState(false); const [error, setError] = useState(""); const [result, setResult] = useState(""); const { showToast } = useToast();
  async function load() { try { const [next, list] = await Promise.all([api<AlertSettings>("/settings/alerts"), api<Alerts>("/alerts")]); setSettings(next); setAlerts(list); setUrl(next.url); setPrivate(next.private_endpoint); } catch (failure) { setError((failure as Error).message); } }
  useEffect(() => { void load(); const timer = setInterval(() => void api<Alerts>("/alerts").then(setAlerts).catch(() => undefined), 30000); return () => clearInterval(timer); }, []);
  async function save(event: React.FormEvent) { event.preventDefault(); setBusy(true); setError(""); setResult(""); try { const next = await api<AlertSettings>("/settings/alerts", { method: "PUT", body: JSON.stringify({ url, secret, private_endpoint: privateEndpoint }) }); setSettings(next); setUrl(next.url); setSecret(""); showToast(next.configured ? "Alerts saved" : "Alerts turned off"); } catch (failure) { setError((failure as Error).message); } finally { setBusy(false); } }
  async function test() { setTesting(true); setResult(""); setError(""); try { const outcome = await api<{ ok: boolean; error?: string }>("/settings/alerts/test", { method: "POST", body: "{}" }); if (outcome.ok) setResult("Test alert delivered."); else setError(`The test alert was not delivered: ${outcome.error}`); } catch (failure) { setError((failure as Error).message); } finally { setTesting(false); } }
  const active = alerts?.conditions.filter((c) => c.active) || [];
  return <Card><CardHeader title="Alerts" aside={settings && <Pill tone={settings.configured ? "good" : "neutral"}>{settings.configured ? "Webhook set" : "Off"}</Pill>} />
    <p className="caption">Pgfy posts JSON to one webhook when backups fail or fall behind, PostgreSQL is unreachable for five minutes, disk runs low, the clock drifts, the certificate is expiring or was not delivered, connections reach 80%, a job is interrupted, or a password is changed. Each condition is sent at most once a day and followed by a resolved message.</p>
    <form className="dialog-form" onSubmit={(event) => void save(event)}>
      <label className="field"><span>Webhook URL</span><input value={url} onChange={(event) => setUrl(event.target.value)} placeholder="https://hooks.example.com/…" aria-label="Webhook URL" /><small className="caption">Leave empty to turn alerts off. Slack incoming webhooks work as they are; for Discord add <code>/slack</code> to its webhook URL.</small></label>
      <label className="field"><span>Signing secret (optional)</span><input type="password" value={secret} onChange={(event) => setSecret(event.target.value)} placeholder={settings?.has_secret ? "Stored — leave empty to keep it" : "Signs each delivery with HMAC-SHA256"} aria-label="Signing secret" autoComplete="off" /></label>
      <label className="checkbox"><input type="checkbox" checked={privateEndpoint} onChange={(event) => setPrivate(event.target.checked)} /> The receiver is on this machine or a private network (allows plain HTTP)</label>
      {error && <ErrorNotice message={error} />}{result && <Banner>{result}</Banner>}
      <div className="actions"><Button type="submit" loading={busy}>{busy ? "Saving…" : "Save"}</Button><Button type="button" variant="secondary" loading={testing} disabled={!settings?.configured} onClick={() => void test()}>Send test alert</Button></div>
    </form>
    <h3 className="subheading">Needs attention now</h3>
    {alerts ? active.length === 0 ? <p className="caption">Nothing needs attention.</p> : <ul className="alert-list">{active.map((c) => <li key={c.key}><strong>{c.summary}</strong><span className="caption"> — since {relativeTime(c.first_seen_at)}. {c.detail}</span></li>)}</ul> : <p>Loading…</p>}
    {alerts?.delivery.last_error && <p className="caption warn-text">Last delivery failed: {alerts.delivery.last_error}</p>}
  </Card>;
}

function friendlyName(key: string) { return key === "application" ? "Pgfy" : key.replaceAll("_", " ").replace(/\b\w/g, (value) => value.toUpperCase()); }
export function SettingsPage({ settings, status }: { settings: Settings; status: Status | null }) {
  const access = status?.database_access; const certificate = access?.certificate; const hostCert = status?.host?.certificate; const { theme, setTheme } = useTheme();
  const themeOptions: { value: ThemePreference; label: string }[] = [{ value: "light", label: "Light" }, { value: "dark", label: "Dark" }, { value: "system", label: "System" }];
  return <><PageHeader title="Settings" />
    <Card><CardHeader title="Server" /><DetailsList items={[
      { label: "Dashboard address", value: <a href={settings.origin}>{settings.origin} <ExternalLink size={13} /></a> },
      { label: "Access mode", value: settings.mode === "https" ? "Public HTTPS" : "SSH tunnel only" },
      { label: "Pgfy version", value: settings.release || status?.versions.application || "Unavailable" },
      { label: "PostgreSQL version", value: status?.postgres.version || "Connection unavailable" },
    ]} /></Card>
    <Card><CardHeader title="Database endpoint" aside={access && <Pill tone={access.mode === "direct" ? certificate?.state === "trusted" ? "good" : "wait" : "neutral"}>{access.mode === "direct" ? certificate?.state === "trusted" ? "Certificate trusted" : "Certificate pending" : "Tunnel only"}</Pill>} />
      {access ? <><DetailsList items={[{ label: "Address", value: <code>{access.host}:{access.port}</code>, copy: `${access.host}:${access.port}` }, { label: "Certificate", value: access.mode === "tunnel" ? "Not used in tunnel mode" : certificate?.state === "trusted" ? `Trusted${certificate.issuer ? ` · ${certificate.issuer}` : ""}` : certificate?.state === "placeholder" ? "Not issued yet" : "Status unavailable" }]} />
        {access.mode === "direct" && hostCert?.expires_at && <p className={hostCert.expiring ? "caption warn-text" : "caption"}>Certificate expires {formatDate(hostCert.expires_at)} ({relativeTime(hostCert.expires_at)}). It serves both the dashboard and PostgreSQL, so an expired certificate stops every client using <code>verify-full</code>.</p>}
        {access.mode === "direct" && hostCert?.expired && <Banner tone="warn">The certificate has expired: clients verifying it cannot connect. Check that ports 80/443 reach Caddy, then run <code>sudo pgfyctl sync-db-cert</code>.</Banner>}
        {access.mode === "direct" && hostCert?.expiring && !hostCert.expired && <Banner tone="warn">The certificate expires within 14 days. Check that ports 80/443 reach Caddy, then run <code>sudo pgfyctl sync-db-cert</code>.</Banner>}
        {access.mode === "direct" && hostCert?.last_sync && !hostCert.last_sync.ok && <Banner tone="warn">The last certificate delivery to PostgreSQL failed{Number.isFinite(Date.parse(hostCert.last_sync.at)) ? ` (${relativeTime(Date.parse(hostCert.last_sync.at) / 1000)})` : ""}: {hostCert.last_sync.message}</Banner>}
        {access.mode === "direct" && certificate?.state !== "trusted" && <Banner tone="warn">Run <code>sudo pgfyctl sync-db-cert</code> on the server to issue the database certificate.</Banner>}
        <p className="caption">{access.mode === "direct" ? "Allow TCP port 5432 in your provider firewall for the app servers that connect." : "Open an SSH tunnel before connecting to PostgreSQL."}</p></> : <p>Database access details are unavailable.</p>}
    </Card>
    <HostCard host={status?.host} />
    <BackupsCard />
    <ConnectionsCard />
    <AlertsCard />
    <Card><CardHeader title="Appearance" /><SegmentedControl value={theme} options={themeOptions} onChange={setTheme} label="Theme" /></Card>
    <details className="components-details"><summary>Components</summary><Card><DetailsList items={[
      { label: "Caddy", value: settings.caddy_version }, { label: "Docker", value: settings.docker_version }, { label: "Compose", value: settings.compose_version },
      ...Object.entries(status?.versions || {}).map(([key, value]) => ({ label: friendlyName(key), value })),
    ]} /><div className="installation-id"><span>Installation ID</span><code>{settings.id}</code><CopyButton value={settings.id} label="Copy" /></div></Card></details>
  </>;
}
