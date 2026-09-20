import { Globe, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type PolicyState, type Project, type Session } from "../../api";
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
  return <Card><CardHeader title="Network access" aside={<Pill tone={tone}>{policy.state === "applied" ? "Active" : policy.state === "failed" ? "Failed" : "Applying…"}</Pill>} />
    <div className="radio-cards"><label className={anywhere ? "selected" : ""}><input type="radio" name="access-mode" checked={anywhere} onChange={() => setAddresses(ANYWHERE)} /><span><strong>Anywhere</strong><small>Any address can connect with TLS and this database's password — the default.</small></span></label><label className={!anywhere ? "selected" : ""}><input type="radio" name="access-mode" checked={!anywhere} onChange={() => setAddresses(addresses.filter((address) => !ANYWHERE.includes(address)))} /><span><strong>Only these addresses</strong><small>Allow only your app servers and trusted networks.</small></span></label></div>
    {!anywhere && <><div className="chips">{addresses.map((address) => <span className="chip mono" key={address}>{address}<button type="button" aria-label={`Remove ${address}`} onClick={() => setAddresses(addresses.filter((item) => item !== address))}><X size={13} /></button></span>)}</div><div className="chip-input"><input aria-label="IP or range" placeholder="IP or range, e.g. 203.0.113.10 or 10.0.0.0/24" value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); add(input); } }} /><Button variant="secondary" onClick={() => add(input)} disabled={!input.trim()}>Add</Button>{myIP && <Button variant="secondary" onClick={() => add(myIP)}>Add my IP ({myIP})</Button>}</div><p className="caption">Add your app server's outbound IP, not the one you browse from.</p></>}
    {error && <ErrorNotice message={error} />}<div className="actions"><Button loading={busy} disabled={!dirty} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button><Button variant="ghost" disabled={!dirty} onClick={() => setAddresses(policy.addresses)}>Discard</Button></div>
  </Card>;
}
