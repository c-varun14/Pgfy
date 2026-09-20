import { ArrowRight, Check, Eye, EyeOff, KeyRound, Lock, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Field } from "../components/ui/field";
import { Brand, BrandMark } from "../components/Sidebar";

export function AuthPage({ setup, error, onRetry, onSuccess }: { setup: boolean; error: string; onRetry: () => void; onSuccess: () => Promise<void> }) {
  const secure = location.protocol === "https:";
  return <div className="auth-layout">
    <aside className="auth-aside">
      <div className="auth-brand"><Brand /></div>
      <div className="auth-pitch">
        <p className="auth-eyebrow"><i /> Private Postgres infrastructure</p>
        <h1>Your database.<br /><span>Your rules.</span></h1>
        <p className="auth-intro">Everything you need to run PostgreSQL with confidence. Nothing between you and your data.</p>
        <div className="auth-proof">
          <span><Check size={14} /> Runs on your server</span>
          <span><Check size={14} /> No vendor lock-in</span>
        </div>
      </div>
      <div className="auth-orbit" aria-hidden="true"><span><BrandMark size={62} /></span></div>
      <span className="aside-footer"><Lock size={13} />Private by design</span>
    </aside>
    <main className="auth-main">
      <div className="auth-card">
        <p className="auth-form-kicker">{setup ? "Server setup" : "Administrator access"}</p>
        <header className="auth-card-head"><BrandMark size={42} /><div><h1>{setup ? "Create your admin account" : "Welcome back"}</h1><p>{setup ? "This account will have full access to this server." : "Sign in to manage your databases."}</p></div></header>
        {error && <><ErrorNotice message={error} /><Button variant="secondary" onClick={onRetry}>Retry connection</Button></>}
        <AuthForm setup={setup} onSuccess={onSuccess} />
        <p className={`fine-print${secure ? "" : " fine-print-warn"}`}><ShieldCheck size={15} />{secure ? "Your connection to this server is encrypted" : "HTTP access — use only through your SSH tunnel"}</p>
      </div>
    </main>
  </div>;
}

function AuthForm({ setup, onSuccess }: { setup: boolean; onSuccess: () => Promise<void> }) {
  const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); setBusy(true); setError(""); const data = new FormData(event.currentTarget); try { await api(setup ? "/setup" : "/auth/login", { method: "POST", body: JSON.stringify({ email: data.get("email"), password: data.get("password"), ...(setup ? { token: data.get("token") } : {}) }) }); await onSuccess(); } catch (failure) { setError((failure as Error).message); } finally { setBusy(false); } }
  return <form onSubmit={(event) => void submit(event)}>
    {setup && <Field label="Setup token" htmlFor="setup-token" hint="Find this 30-minute token in your installer terminal."><SecretInput id="setup-token" name="token" autoComplete="off" maxLength={43} icon="key" /></Field>}
    <Field label="Email address" htmlFor="email"><input id="email" name="email" type="email" placeholder="you@example.com" autoComplete="username" required maxLength={254} autoFocus={!setup} /></Field>
    <Field label="Password" htmlFor="password" hint={setup ? "Use 15+ characters. A memorable passphrase works well." : undefined}><SecretInput id="password" name="password" autoComplete={setup ? "new-password" : "current-password"} minLength={setup ? 15 : undefined} maxLength={256} /></Field>
    {error && <ErrorNotice message={error} />}<Button loading={busy} type="submit">{busy ? "Please wait…" : setup ? "Create administrator" : "Sign in"}<ArrowRight size={17} /></Button>
  </form>;
}

function SecretInput({ icon, ...props }: Omit<React.ComponentProps<"input">, "type" | "required"> & { icon?: "key" }) {
  const [visible, setVisible] = useState(false);
  return <div className={`secret-input${icon ? " secret-input-icon" : ""}`}>
    {icon && <KeyRound size={15} aria-hidden="true" />}
    <input {...props} type={visible ? "text" : "password"} required />
    <button type="button" onClick={() => setVisible((value) => !value)} aria-label={visible ? "Hide value" : "Show value"}>{visible ? <EyeOff size={16} /> : <Eye size={16} />}</button>
  </div>;
}
