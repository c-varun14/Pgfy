import { ArrowRight, Check, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Field } from "../components/ui/field";
import { Brand, BrandMark } from "../components/Sidebar";

export function AuthPage({ setup, error, onRetry, onSuccess }: { setup: boolean; error: string; onRetry: () => void; onSuccess: () => Promise<void> }) {
  return <div className="auth-layout"><aside className="auth-aside"><div className="auth-brand"><Brand /></div><div><h1>Self-hosted PostgreSQL that just works</h1><ul><li><Check size={17} />Databases in seconds</li><li><Check size={17} />Daily backups to your own S3</li><li><Check size={17} />Restore on any server</li></ul></div><span className="aside-footer">Your data stays on your server.</span></aside>
    <main className="auth-main"><div className="auth-card"><BrandMark size={34} /><h1>{setup ? "Create your admin account" : "Sign in"}</h1><p>{setup ? "Set up the administrator for this Pgfy server." : "Use your administrator account to continue."}</p>{error && <><ErrorNotice message={error} /><Button variant="secondary" onClick={onRetry}>Retry connection</Button></>}<AuthForm setup={setup} onSuccess={onSuccess} /><p className="fine-print"><ShieldCheck size={15} />{location.protocol === "https:" ? "Connected over HTTPS" : "HTTP access — use only through your SSH tunnel"}</p></div></main>
  </div>;
}

function AuthForm({ setup, onSuccess }: { setup: boolean; onSuccess: () => Promise<void> }) {
  const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); setBusy(true); setError(""); const data = new FormData(event.currentTarget); try { await api(setup ? "/setup" : "/auth/login", { method: "POST", body: JSON.stringify({ email: data.get("email"), password: data.get("password"), ...(setup ? { token: data.get("token") } : {}) }) }); await onSuccess(); } catch (failure) { setError((failure as Error).message); } finally { setBusy(false); } }
  return <form onSubmit={(event) => void submit(event)}>
    {setup && <Field label="Setup token" htmlFor="setup-token" hint="Copy the 30-minute token from your installer terminal."><input id="setup-token" name="token" type="password" autoComplete="off" required maxLength={43} /></Field>}
    <Field label="Email address" htmlFor="email"><input id="email" name="email" type="email" placeholder="you@example.com" autoComplete="username" required maxLength={254} /></Field>
    <Field label="Password" htmlFor="password" hint={setup ? "Use at least 15 characters. A passphrase works well." : undefined}><input id="password" name="password" type="password" autoComplete={setup ? "new-password" : "current-password"} required minLength={setup ? 15 : undefined} maxLength={256} /></Field>
    {error && <ErrorNotice message={error} />}<Button loading={busy} type="submit">{busy ? "Please wait…" : setup ? "Create administrator" : "Sign in"}<ArrowRight size={17} /></Button>
  </form>;
}
