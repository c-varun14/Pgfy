import { Check, CloudUpload, TriangleAlert } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, type CheckStep, type StorageSettings } from "../../api";
import { ErrorNotice } from "../../components/ui/banner";
import { Button } from "../../components/ui/button";
import { Field } from "../../components/ui/field";
import { useToast } from "../../components/ui/toast";

export const EMPTY_STORAGE: StorageSettings = { endpoint: "https://s3.us-east-1.amazonaws.com", region: "us-east-1", bucket: "", prefix: "pgfy", access_key: "", secret_key: "", session_token: "", path_style: false, private_endpoint: false, bucket_protection: "versioning" };
export function StorageForm({ initial, configured, onSaved }: { initial?: StorageSettings; configured: boolean; onSaved: (settings: StorageSettings) => void }) {
  const [form, setForm] = useState<StorageSettings>({ ...EMPTY_STORAGE, ...initial }); const [advanced, setAdvanced] = useState(false); const [busy, setBusy] = useState<"save" | "check" | "">(""); const [error, setError] = useState(""); const [steps, setSteps] = useState<CheckStep[] | null>(null); const { showToast } = useToast();
  useEffect(() => { setForm({ ...EMPTY_STORAGE, ...initial }); setAdvanced(!!initial && (initial.path_style || initial.private_endpoint || !!initial.session_token || !initial.endpoint.includes("amazonaws.com"))); }, [initial]);
  function set<K extends keyof StorageSettings>(key: K, value: StorageSettings[K]) { setForm((current) => ({ ...current, [key]: value })); }
  async function save(event: FormEvent) { event.preventDefault(); setBusy("save"); setError(""); setSteps(null); try { const body = await api<{ configured: boolean; settings: StorageSettings }>("/settings/storage", { method: "PUT", body: JSON.stringify(form) }); setForm({ ...EMPTY_STORAGE, ...body.settings }); onSaved(body.settings); showToast("Backup storage saved"); } catch (failure) { setError((failure as Error).message); } finally { setBusy(""); } }
  async function check() { setBusy("check"); setError(""); try { setSteps((await api<{ ok: boolean; steps: CheckStep[] }>("/settings/storage/check", { method: "POST", body: "{}" })).steps); } catch (failure) { setError((failure as Error).message); } finally { setBusy(""); } }
  return <form className="storage-form" onSubmit={(event) => void save(event)}>
    <Field label="Bucket" htmlFor="bucket"><input id="bucket" value={form.bucket} onChange={(event) => set("bucket", event.target.value)} placeholder="my-backups" required /></Field>
    <Field label="Region" htmlFor="region"><input id="region" value={form.region} onChange={(event) => set("region", event.target.value)} placeholder="us-east-1" /></Field>
    <Field label="Access key ID" htmlFor="access-key"><input id="access-key" value={form.access_key} onChange={(event) => set("access_key", event.target.value)} autoComplete="off" required /></Field>
    <Field label="Secret access key" htmlFor="secret-key"><input id="secret-key" type="password" value={form.secret_key} onChange={(event) => set("secret_key", event.target.value)} autoComplete="off" required /></Field>
    <Field label="Folder" htmlFor="prefix" hint="Backups are kept under this folder in your bucket."><input id="prefix" value={form.prefix} onChange={(event) => set("prefix", event.target.value)} placeholder="pgfy" /></Field>
    <fieldset className="protection-choice">
      <legend>Deleted backups</legend>
      <p className="caption">These keys can delete objects, so the bucket needs a way back from a mistake.</p>
      <label className="check-field"><input type="radio" name="bucket-protection" checked={form.bucket_protection !== "acknowledged"} onChange={() => set("bucket_protection", "versioning")} />Bucket versioning is on (checked with your provider)</label>
      <label className="check-field"><input type="radio" name="bucket-protection" checked={form.bucket_protection === "acknowledged"} onChange={() => set("bucket_protection", "acknowledged")} />My provider can't report versioning — I've protected the bucket myself, or I accept that these keys can delete backups</label>
    </fieldset>
    <details className="form-details" open={advanced} onToggle={(event) => setAdvanced(event.currentTarget.open)}><summary>Advanced</summary><div className="advanced-fields"><Field label="Endpoint URL" htmlFor="endpoint"><input id="endpoint" value={form.endpoint} onChange={(event) => set("endpoint", event.target.value)} required /></Field><Field label="Session token" htmlFor="session-token"><input id="session-token" type="password" value={form.session_token} onChange={(event) => set("session_token", event.target.value)} autoComplete="off" /></Field><label className="check-field"><input type="checkbox" checked={form.path_style} onChange={(event) => set("path_style", event.target.checked)} />Use path-style addressing</label><label className="check-field"><input type="checkbox" checked={form.private_endpoint} onChange={(event) => set("private_endpoint", event.target.checked)} />Private endpoint on this machine or network (allows http://)</label></div></details>
    {error && <ErrorNotice message={error} />}
    <div className="actions"><Button type="submit" loading={busy === "save"}>{busy === "save" ? "Saving…" : "Save"}</Button><Button type="button" variant="secondary" loading={busy === "check"} disabled={!configured} onClick={() => void check()}><CloudUpload size={15} />{busy === "check" ? "Testing…" : "Test storage"}</Button></div>
    {steps && <ul className="check-steps">{steps.map((step) => <li key={step.name} className={step.ok ? "ok" : "bad"}>{step.ok ? <Check size={14} /> : <TriangleAlert size={14} />}{step.name}{(step.error || step.detail) && <small>{step.error || step.detail}</small>}</li>)}</ul>}
  </form>;
}
