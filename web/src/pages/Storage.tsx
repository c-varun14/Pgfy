import { useEffect, useState, type FormEvent } from "react";
import { Check, CloudUpload, TriangleAlert } from "lucide-react";
import { Button } from "../components/ui/button";
import { api, type CheckStep, type StorageSettings } from "../api";
import { ErrorNotice, Pill } from "../ui";

const EMPTY: StorageSettings = { endpoint: "https://s3.us-east-1.amazonaws.com", region: "us-east-1", bucket: "", prefix: "pgfy", access_key: "", secret_key: "", session_token: "", path_style: false };

export function StoragePanel({ onChange }: { onChange?: () => void }) {
  const [form, setForm] = useState<StorageSettings>(EMPTY);
  const [configured, setConfigured] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [busy, setBusy] = useState<"save" | "check" | "">("");
  const [error, setError] = useState("");
  const [steps, setSteps] = useState<CheckStep[] | null>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    api<{ configured: boolean; settings: StorageSettings }>("/settings/storage")
      .then((body) => {
        setConfigured(body.configured);
        setForm({ ...EMPTY, ...body.settings });
        setAdvanced(body.settings.path_style || !!body.settings.session_token || !body.settings.endpoint.includes("amazonaws.com"));
      })
      .catch((e) => setError((e as Error).message));
  }, []);
  function set<K extends keyof StorageSettings>(key: K, value: StorageSettings[K]) {
    setForm({ ...form, [key]: value });
    setSaved(false);
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy("save");
    setError("");
    setSteps(null);
    try {
      const body = await api<{ configured: boolean; settings: StorageSettings }>("/settings/storage", { method: "PUT", body: JSON.stringify(form) });
      setConfigured(body.configured);
      setForm({ ...EMPTY, ...body.settings });
      setSaved(true);
      onChange?.();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  async function check() {
    setBusy("check");
    setError("");
    try {
      setSteps((await api<{ ok: boolean; steps: CheckStep[] }>("/settings/storage/check", { method: "POST", body: "{}" })).steps);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Backup storage</h2>
        <Pill tone={configured ? "good" : "bad"}>{configured ? "Configured" : "Not configured"}</Pill>
      </div>
      <p className="muted small">
        Backups go to a private bucket you own, on AWS S3 or any S3-compatible service. Create the bucket and a key pair limited to it, then paste them here. Keep a copy of these values somewhere safe: they are what you need to recover on a new server.
      </p>
      <form className="storage-form" onSubmit={(e) => void save(e)}>
        <div className="field">
          <label htmlFor="bucket">Bucket</label>
          <input id="bucket" value={form.bucket} onChange={(e) => set("bucket", e.target.value)} placeholder="my-backups" required />
        </div>
        <div className="field">
          <label htmlFor="region">Region</label>
          <input id="region" value={form.region} onChange={(e) => set("region", e.target.value)} placeholder="us-east-1" />
        </div>
        <div className="field">
          <label htmlFor="access_key">Access key ID</label>
          <input id="access_key" value={form.access_key} onChange={(e) => set("access_key", e.target.value)} autoComplete="off" required />
        </div>
        <div className="field">
          <label htmlFor="secret_key">Secret access key</label>
          <input id="secret_key" type="password" value={form.secret_key} onChange={(e) => set("secret_key", e.target.value)} autoComplete="off" required />
        </div>
        <div className="field">
          <label htmlFor="prefix">Folder (prefix)</label>
          <input id="prefix" value={form.prefix} onChange={(e) => set("prefix", e.target.value)} placeholder="pgfy" />
          <small>Backups are stored under this folder as backups/&lt;database&gt;/&lt;time&gt;/.</small>
        </div>
        <div className="field">
          <button type="button" className="link" onClick={() => setAdvanced(!advanced)}>
            {advanced ? "Hide advanced options" : "Advanced: custom endpoint, session token, path-style"}
          </button>
        </div>
        {advanced && (
          <>
            <div className="field">
              <label htmlFor="endpoint">Endpoint URL</label>
              <input id="endpoint" value={form.endpoint} onChange={(e) => set("endpoint", e.target.value)} placeholder="https://s3.us-east-1.amazonaws.com" required />
              <small>Any S3-compatible HTTPS endpoint works; for AWS use the regional S3 endpoint.</small>
            </div>
            <div className="field">
              <label htmlFor="session_token">Session token (optional)</label>
              <input id="session_token" type="password" value={form.session_token} onChange={(e) => set("session_token", e.target.value)} autoComplete="off" />
            </div>
            <div className="field checkbox">
              <label htmlFor="path_style">
                <input id="path_style" type="checkbox" checked={form.path_style} onChange={(e) => set("path_style", e.target.checked)} />
                Use path-style addressing (needed by some compatible services)
              </label>
            </div>
          </>
        )}
        {error && <ErrorNotice message={error} />}
        <div className="actions">
          <Button type="submit" disabled={busy !== ""}>
            {busy === "save" ? "Saving…" : saved ? "Saved" : "Save storage settings"}
          </Button>
          <Button type="button" variant="outline" disabled={busy !== "" || !configured} onClick={() => void check()}>
            <CloudUpload size={15} /> {busy === "check" ? "Testing…" : "Test storage"}
          </Button>
        </div>
      </form>
      {steps && (
        <ul className="steps check-steps">
          {steps.map((s) => (
            <li key={s.name} className={s.ok ? "done" : "failed"}>
              {s.ok ? <Check size={14} /> : <TriangleAlert size={14} />}
              {s.name}
              {s.error && <span className="muted"> — {s.error}</span>}
            </li>
          ))}
          {steps.every((s) => s.ok) && <li className="done muted">Upload, listing, download and cleanup all worked. Backups can use this bucket.</li>}
        </ul>
      )}
    </section>
  );
}
