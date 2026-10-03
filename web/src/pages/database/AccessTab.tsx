import { Globe, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type Limits, type PolicyState, type Project, type ProjectLimits, type Session } from "../../api";
import { ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { Card, CardHeader } from "../../components/ui/card";
import { Pill } from "../../components/ui/pill";
import { useToast } from "../../components/ui/toast";

const ANYWHERE = ["0.0.0.0/0", "::/0"];
export function AccessTab({ project, session, onChange }: { project: Project; session: Session; onChange: () => Promise<void> }) {
  const policy = project.policy || { current_revision: 0, applied_revision: 0, state: "idle", last_error: "", addresses: ANYWHERE };
  const [addresses, setAddresses] = useState(policy.addresses); const [revision, setRevision] = useState(policy.current_revision); const [input, setInput] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const { showToast } = useToast();
  useEffect(() => { if (policy.current_revision !== revision) { setAddresses(policy.addresses); setRevision(policy.current_revision); } }, [policy.current_revision]);
  const anywhere = addresses.some((address) => ANYWHERE.includes(address)); const dirty = JSON.stringify(addresses) !== JSON.stringify(policy.addresses); const myIP = session.client_ip && !session.client_ip.startsWith("127.") ? session.client_ip : "";
  function add(value: string) { const address = value.trim(); if (!address || addresses.includes(address)) return; setAddresses(addresses.filter((item) => !ANYWHERE.includes(item)).concat(address)); setInput(""); }
  async function save() { setBusy(true); setError(""); try { const next = await api<PolicyState>(`/projects/${project.id}/access`, { method: "PUT", body: JSON.stringify({ revision, addresses }) }); setAddresses(next.addresses); setRevision(next.current_revision); if (next.state === "failed") setError(next.last_error || "The rules could not be applied."); else showToast("Network access saved"); await onChange(); } catch (failure) { setError((failure as Error).message); if ((failure as { status?: number }).status === 409) await onChange(); } finally { setBusy(false); } }
  const tone = policy.state === "applied" ? "good" : policy.state === "failed" ? "bad" : "wait";
  return <div className="tab-stack"><Card><CardHeader title="Network access" aside={<Pill tone={tone}>{policy.state === "applied" ? "Active" : policy.state === "failed" ? "Failed" : "Applying…"}</Pill>} />
    <div className="radio-cards"><label className={anywhere ? "selected" : ""}><input type="radio" name="access-mode" checked={anywhere} onChange={() => setAddresses(ANYWHERE)} /><span><strong>Anywhere</strong><small>Any address can connect with TLS and this database's password — the default.</small></span></label><label className={!anywhere ? "selected" : ""}><input type="radio" name="access-mode" checked={!anywhere} onChange={() => setAddresses(addresses.filter((address) => !ANYWHERE.includes(address)))} /><span><strong>Only these addresses</strong><small>Allow only your app servers and trusted networks.</small></span></label></div>
    {!anywhere && <><div className="chips">{addresses.map((address) => <span className="chip mono" key={address}>{address}<button type="button" aria-label={`Remove ${address}`} onClick={() => setAddresses(addresses.filter((item) => item !== address))}><X size={13} /></button></span>)}</div><div className="chip-input"><input aria-label="IP or range" placeholder="IP or range, e.g. 203.0.113.10 or 10.0.0.0/24" value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); add(input); } }} /><Button variant="secondary" onClick={() => add(input)} disabled={!input.trim()}>Add</Button>{myIP && <Button variant="secondary" onClick={() => add(myIP)}>Add my IP ({myIP})</Button>}</div><p className="caption">Add your app server's outbound IP, not the one you browse from.</p></>}
    {error && <ErrorNotice message={error} />}<div className="actions"><Button loading={busy} disabled={!dirty} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button><Button variant="ghost" disabled={!dirty} onClick={() => setAddresses(policy.addresses)}>Discard</Button></div>
  </Card>{project.limits && <LimitsCard project={project} limits={project.limits} onChange={onChange} />}</div>;
}

const DEFAULTS: Limits = { statement_timeout_ms: 60000, idle_in_transaction_ms: 300000, temp_file_limit_kb: 1048576, lock_timeout_ms: 10000, connection_limit: 25 };
type Field = { key: keyof Limits; label: string; unit: string; scale: number; hint: string };
const FIELDS: Field[] = [
  { key: "statement_timeout_ms", label: "Statement timeout", unit: "seconds", scale: 1000, hint: "0 turns it off. A default the app can override." },
  { key: "idle_in_transaction_ms", label: "Idle in transaction", unit: "seconds", scale: 1000, hint: "Ends sessions that leave a transaction open. 0 turns it off." },
  { key: "lock_timeout_ms", label: "Lock wait", unit: "seconds", scale: 1000, hint: "0 turns it off. A default the app can override." },
  { key: "temp_file_limit_kb", label: "Temporary files", unit: "MB", scale: 1024, hint: "Per session; -1 is unlimited. Enforced." },
  { key: "connection_limit", label: "Connections", unit: "at once", scale: 1, hint: "Enforced; counts against the server's budget." },
];
/** Guardrails on this database's user; they apply to new connections. */
function LimitsCard({ project, limits, onChange }: { project: Project; limits: ProjectLimits; onChange: () => Promise<void> }) {
  const shown = (l: Limits) => Object.fromEntries(FIELDS.map((f) => [f.key, String(l[f.key] === -1 ? -1 : Math.round((l[f.key] / f.scale) * 100) / 100)])) as Record<keyof Limits, string>;
  const [values, setValues] = useState(shown(limits)); const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const { showToast } = useToast();
  useEffect(() => { setValues(shown(limits)); }, [limits.revision]);
  const parsed = Object.fromEntries(FIELDS.map((f) => { const n = Number(values[f.key]); return [f.key, n === -1 ? -1 : Math.round(n * f.scale)]; })) as Limits;
  // An empty field is not "0": Number("") would silently turn a timeout off.
  const invalid = FIELDS.some((f) => values[f.key].trim() === "" || Number.isNaN(Number(values[f.key])));
  const dirty = FIELDS.some((f) => parsed[f.key] !== limits[f.key]); const applied = limits.applied_revision >= limits.revision;
  async function save(next: Limits) { setBusy(true); setError(""); try { await api<ProjectLimits>(`/projects/${project.id}/limits`, { method: "PUT", body: JSON.stringify({ ...next, revision: limits.revision }) }); showToast("Limits saved"); await onChange(); } catch (failure) { setError((failure as Error).message); if ((failure as { status?: number }).status === 409) await onChange(); } finally { setBusy(false); } }
  return <Card><CardHeader title="Limits" aside={<Pill tone={applied ? "good" : limits.last_error ? "bad" : "wait"}>{applied ? "Active" : limits.last_error ? "Not applied" : "Applying…"}</Pill>} />
    {!applied && limits.last_error && <ErrorNotice message={limits.last_error} />}
    <div className="limits-grid">{FIELDS.map((f) => <label key={f.key} className="field"><span>{f.label} <small>({f.unit})</small></span><input inputMode="numeric" value={values[f.key]} onChange={(event) => setValues({ ...values, [f.key]: event.target.value })} aria-label={f.label} /><small className="caption">{f.hint}</small></label>)}</div>
    <p className="caption">Changes apply to new connections; open ones keep their settings until they reconnect.</p>
    {error && <ErrorNotice message={error} />}
    <div className="actions"><Button loading={busy} disabled={!dirty || invalid} onClick={() => void save(parsed)}>{busy ? "Saving…" : "Save limits"}</Button><Button variant="ghost" disabled={busy} onClick={() => void save(DEFAULTS)}>Reset to defaults</Button></div>
  </Card>;
}
