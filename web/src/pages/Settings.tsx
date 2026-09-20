import { ExternalLink } from "lucide-react";
import type { Settings, Status } from "../api";
import { PageHeader } from "../components/PageHeader";
import { Banner } from "../components/ui/banner";
import { Card, CardHeader } from "../components/ui/card";
import { CopyButton } from "../components/ui/copy-button";
import { DetailsList } from "../components/ui/details-list";
import { Pill } from "../components/ui/pill";
import { SegmentedControl } from "../components/ui/segmented-control";
import { type ThemePreference, useTheme } from "../theme";

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
    <Card><CardHeader title="Appearance" /><SegmentedControl value={theme} options={themeOptions} onChange={setTheme} label="Theme" /></Card>
    <details className="components-details"><summary>Components</summary><Card><DetailsList items={[
      { label: "Caddy", value: settings.caddy_version }, { label: "Docker", value: settings.docker_version }, { label: "Compose", value: settings.compose_version },
      ...Object.entries(status?.versions || {}).map(([key, value]) => ({ label: friendlyName(key), value })),
    ]} /><div className="installation-id"><span>Installation ID</span><code>{settings.id}</code><CopyButton value={settings.id} label="Copy" /></div></Card></details>
  </>;
}
