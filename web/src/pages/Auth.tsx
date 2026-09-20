import { ArrowRight, Check, Eye, EyeOff, KeyRound, Lock, Monitor, Moon, ShieldCheck, Sun } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { ErrorNotice } from "../components/ui/banner";
import { Button } from "../components/ui/button";
import { Field } from "../components/ui/field";
import { Brand, BrandMark } from "../components/Brand";
import { useTheme } from "../theme";
import { SegmentedControl } from "../components/ui/segmented-control";

export function AuthPage({ setup, error, onRetry, onSuccess }: { setup: boolean; error: string; onRetry: () => void; onSuccess: () => Promise<void> }) {
  const secure = location.protocol === "https:";
  const { theme, setTheme } = useTheme();
  return <div className="auth-layout">
    <aside className="auth-aside">
      <div className="auth-brand"><Brand /><span className="auth-edition">POSTGRES, PERSONALLY.</span></div>
      <div className="auth-pitch">
        <p className="auth-eyebrow"><i /> A home for your data</p>
        <h1>Your database.<br /><span>Your rules.</span></h1>
        <p className="auth-intro">PostgreSQL, with room to breathe.<br />On your server. Under your control.</p>
        <div className="auth-proof">
          <span><Check size={14} /> Runs on your server</span>
          <span><Check size={14} /> No vendor lock-in</span>
        </div>
      </div>
      <div className="auth-sculpture" aria-hidden="true"><div className="sculpture-grid" /><div className="sculpture-mark"><BrandMark size={240} /><BrandMark size={240} /><BrandMark size={240} /></div><span className="sculpture-caption">YOUR INFRASTRUCTURE. YOUR POSSIBILITIES.</span></div>
      <div className="auth-aside-footer"><span><Lock size={13} /> Private by design</span><span>Built around you <ArrowRight size={14} /></span></div>
    </aside>
    <main className="auth-main">
      <div className="auth-topbar"><div className="auth-mobile-brand"><Brand /></div><SegmentedControl value={theme} onChange={setTheme} label="Appearance" options={[{ value: "light", label: "Light", icon: <Sun size={14} /> }, { value: "system", label: "System", icon: <Monitor size={14} /> }, { value: "dark", label: "Dark", icon: <Moon size={14} /> }]} /></div>
      <div className="auth-card">
        <p className="auth-form-kicker"><span className="auth-step">{setup ? "01" : <Lock size={12} />}</span>{setup ? "A fresh start" : "Your private workspace"}</p>
        <header className="auth-card-head"><h1>{setup ? "Create your admin account" : "Welcome back"}</h1><p>{setup ? "Make yourself at home. Set up your administrator account to get started." : "Your databases, right where you left them. Sign in to your server."}</p></header>
        {error && <><ErrorNotice message={error} /><Button variant="secondary" onClick={onRetry}>Retry connection</Button></>}
        <AuthForm setup={setup} onSuccess={onSuccess} />
        <p className={`fine-print${secure ? "" : " fine-print-warn"}`}><ShieldCheck size={15} />{secure ? "Your connection to this server is encrypted" : "HTTP access — use only through your SSH tunnel"}</p>
      </div>
      <p className="auth-bottom-note">Your server. Your data. Always.</p>
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
