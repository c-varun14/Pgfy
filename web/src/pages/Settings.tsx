import { ExternalLink } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type BackupPolicy, type Settings, type Status } from "../api";
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

function friendlyName(key: string) { return key === "application" ? "Pgfy" : key.replaceAll("_", " ").replace(/\b\w/g, (value) => value.toUpperCase()); }
export function SettingsPage({ settings, status }: { settings: Settings; status: Status | null }) {
  const access = status?.database_access; const certificate = access?.certificate; const { theme, setTheme } = useTheme();
  const themeOptions: { value: ThemePreference; label: string }[] = [{ value: "light", label: "Light" }, { value: "dark", label: "Dark" }, { value: "system", label: "System" }];
  return <><PageHeader title="Settings" />
    <Card><CardHeader title="Server" /><DetailsList items={[
      { label: "Dashboard address", value: <a href={settings.origin}>{settings.origin} <ExternalLink size={13} /></a> },
      { label: "Access mode", value: settings.mode === "https" ? "Public HTTPS" : "SSH tunnel only" },
      { label: "Pgfy version", value: settings.release || status?.versions.application || "Unavailable" },
      { label: "PostgreSQL version", value: status?.postgres.version || "Connection unavailable" },
    ]} /></Card>
    <Card><CardHeader title="Database endpoint" aside={access && <Pill tone={access.mode === "direct" ? certificate?.state === "trusted" ? "good" : "wait" : "neutral"}>{access.mode === "direct" ? certificate?.state === "trusted" ? "Certificate trusted" : "Certificate pending" : "Tunnel only"}</Pill>} />
      {access ? <><DetailsList items={[{ label: "Address", value: <code>{access.host}:{access.port}</code>, copy: `${access.host}:${access.port}` }, { label: "Certificate", value: certificate?.state === "trusted" ? `Trusted${certificate.issuer ? ` · ${certificate.issuer}` : ""}` : certificate?.state === "placeholder" ? "Not issued yet" : "Status unavailable" }]} />
        {access.mode === "direct" && certificate?.state !== "trusted" && <Banner tone="warn">Run <code>sudo pgfyctl sync-db-cert</code> on the server to issue the database certificate.</Banner>}
        <p className="caption">{access.mode === "direct" ? "Allow TCP port 5432 in your provider firewall for the app servers that connect." : "Open an SSH tunnel before connecting to PostgreSQL."}</p></> : <p>Database access details are unavailable.</p>}
    </Card>
    <BackupsCard />
    <Card><CardHeader title="Appearance" /><SegmentedControl value={theme} options={themeOptions} onChange={setTheme} label="Theme" /></Card>
    <details className="components-details"><summary>Components</summary><Card><DetailsList items={[
      { label: "Caddy", value: settings.caddy_version }, { label: "Docker", value: settings.docker_version }, { label: "Compose", value: settings.compose_version },
      ...Object.entries(status?.versions || {}).map(([key, value]) => ({ label: friendlyName(key), value })),
    ]} /><div className="installation-id"><span>Installation ID</span><code>{settings.id}</code><CopyButton value={settings.id} label="Copy" /></div></Card></details>
  </>;
}
