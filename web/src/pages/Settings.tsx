import { ExternalLink, Terminal } from "lucide-react";
import type { Settings, Status } from "../api";
import { Notice, Pill } from "../ui";
import { StoragePanel } from "./Storage";

export function SettingsPage({ settings, status }: { settings: Settings; status: Status | null }) {
  const access = status?.database_access;
  const cert = access?.certificate;
  return (
    <>
      <StoragePanel />
      <section className="panel">
        <div className="panel-heading">
          <h2>Database access</h2>
          {access && (
            <Pill tone={access.mode === "direct" ? (cert?.state === "trusted" ? "good" : "wait") : "neutral"}>
              {access.mode === "direct" ? "Public · TLS required" : "SSH tunnel only"}
            </Pill>
          )}
        </div>
        {access ? (
          <dl className="details">
            <div>
              <dt>Host</dt>
              <dd className="mono">{access.host}</dd>
            </div>
            <div>
              <dt>Port</dt>
              <dd className="mono">{access.port}</dd>
            </div>
            <div>
              <dt>Certificate</dt>
              <dd>
                {cert?.state === "trusted"
                  ? `Issued by ${cert.issuer || "a public authority"} · valid until ${cert.not_after}`
                  : cert?.state === "placeholder"
                    ? "Self-signed placeholder — clients cannot verify the server yet"
                    : "Unknown"}
              </dd>
            </div>
          </dl>
        ) : (
          <p className="muted">Loading…</p>
        )}
        {access?.mode === "direct" && (
          <Notice>
            <Terminal size={18} />
            <span>
              Applications connect to <code>{access.host}:5432</code>. Your provider’s firewall must allow TCP 5432; each project also has its own allowed-address rules.
              {cert?.state !== "trusted" && (
                <>
                  {" "}Run <code>sudo pgfyctl sync-db-cert</code> on the server to deliver the dashboard certificate to PostgreSQL.
                </>
              )}
            </span>
          </Notice>
        )}
        {access?.mode === "tunnel" && (
          <Notice>
            <Terminal size={18} />
            <span>
              PostgreSQL listens on the server’s loopback only. Developers forward it with <code>ssh -N -L 5432:127.0.0.1:5432 user@server</code>. To offer direct TLS access, give the installation a hostname with <code>pgfyctl hostname</code>.
            </span>
          </Notice>
        )}
      </section>
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
            <dd>{settings.mode === "https" ? "Public HTTPS" : "Loopback · SSH tunnel only"}</dd>
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
        <Notice>
          <Terminal size={18} />
          <span>
            Change the hostname or recover access with <code>pgfyctl</code> on your server. Changes require signing in again.
          </span>
        </Notice>
      </section>
      <section className="panel">
        <div className="panel-heading">
          <h2>Host components</h2>
          <span className="muted">Recorded during installation</span>
        </div>
        <dl className="details">
          <div>
            <dt>PostgreSQL</dt>
            <dd>{status?.postgres.version || "Connection unavailable"}</dd>
          </div>
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
          {status &&
            Object.entries(status.versions).map(([key, value]) => (
              <div key={key}>
                <dt>{key}</dt>
                <dd>{value}</dd>
              </div>
            ))}
        </dl>
      </section>
    </>
  );
}
