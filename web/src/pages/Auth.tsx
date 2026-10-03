import { ArrowRight, Check, Eye, EyeOff, KeyRound, Lock, Monitor, Moon, ShieldCheck, Sun } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, APIError } from "../api";
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

type EnrolView = { key: string; account: string; issuer: string; server_time: number };
type Step = { kind: "password" } | { kind: "reset" } | { kind: "enrol"; view: EnrolView } | { kind: "code"; serverTime: number };
type Reply = { next?: "code" | "enrol"; server_time?: number } & Partial<EnrolView>;

/** Sign-in happens in steps: a password (or a setup or reset token), then a
 *  code from an authenticator app, or first the key to enrol one. */
function AuthForm({ setup, onSuccess }: { setup: boolean; onSuccess: () => Promise<void> }) {
  const [step, setStep] = useState<Step>({ kind: "password" }); const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  // A reload during enrolment shows the same key again while the step lives.
  useEffect(() => { void api<EnrolView>("/auth/enrol").then((view) => setStep({ kind: "enrol", view })).catch(() => undefined); }, []);
  async function after(reply: Reply | undefined) {
    if (reply?.next === "code") setStep({ kind: "code", serverTime: reply.server_time || 0 });
    else if (reply?.next === "enrol") setStep({ kind: "enrol", view: reply as EnrolView });
    else await onSuccess();
  }
  async function run(action: () => Promise<void>) { setBusy(true); setError(""); try { await action(); } catch (failure) { const code = (failure as APIError).code; if (code === "code_expired" || code === "enrol_expired") { setStep({ kind: "password" }); setError(setup && code === "enrol_expired" ? "The setup step expired and its token was used. Run sudo pgfyctl setup-token on the server for a new one." : (failure as Error).message); return; } setError((failure as Error).message); } finally { setBusy(false); } }
  // Walking away ends the step on the server too, not only in this browser.
  function back() { void api("/auth/abandon", { method: "POST", body: "{}" }).catch(() => undefined); setStep({ kind: "password" }); setError(""); }
  function submitPassword(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => after(await api<Reply>(setup ? "/setup" : "/auth/login", { method: "POST", body: JSON.stringify({ email: data.get("email"), password: data.get("password"), ...(setup ? { token: data.get("token") } : {}) }) }))); }
  function submitReset(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => after(await api<Reply>("/auth/reset", { method: "POST", body: JSON.stringify({ token: data.get("token"), password: data.get("password") }) }))); }
  function submitCode(path: string) { return (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(async () => { await api(path, { method: "POST", body: JSON.stringify({ code: String(data.get("code") || "").replace(/\s/g, "") }) }); await onSuccess(); }); }; }
  if (step.kind === "enrol") return <EnrolStep view={step.view} busy={busy} error={error} onSubmit={submitCode("/auth/enrol/confirm")} />;
  if (step.kind === "code") return <form onSubmit={submitCode("/auth/code")}>
    <p className="auth-step-hint">Enter the 6-digit code from your authenticator app.</p>
    <CodeField />
    <ClockWarning serverTime={step.serverTime} />
    {error && <ErrorNotice message={error} />}<Button loading={busy} type="submit">{busy ? "Checking…" : "Verify"}<ArrowRight size={17} /></Button>
    <button type="button" className="link" onClick={back}>Back to sign in</button>
  </form>;
  if (step.kind === "reset") return <form onSubmit={submitReset}>
    <p className="auth-step-hint">On the server, run <code>sudo pgfyctl reset-admin</code>. Enter the token it prints and a new password; you will then enrol a new authenticator. Nothing changes until that is done.</p>
    <Field label="Reset token" htmlFor="reset-token"><SecretInput id="reset-token" name="token" autoComplete="off" maxLength={43} icon="key" /></Field>
    <Field label="New password" htmlFor="new-password" hint="Use 15+ characters."><SecretInput id="new-password" name="password" autoComplete="new-password" minLength={15} maxLength={256} /></Field>
    {error && <ErrorNotice message={error} />}<Button loading={busy} type="submit">{busy ? "Please wait…" : "Continue"}<ArrowRight size={17} /></Button>
    <button type="button" className="link" onClick={() => { setStep({ kind: "password" }); setError(""); }}>Back to sign in</button>
  </form>;
  return <form onSubmit={submitPassword}>
    {setup && <Field label="Setup token" htmlFor="setup-token" hint="Find this 30-minute token in your installer terminal."><SecretInput id="setup-token" name="token" autoComplete="off" maxLength={43} icon="key" /></Field>}
    <Field label="Email address" htmlFor="email"><input id="email" name="email" type="email" placeholder="you@example.com" autoComplete="username" required maxLength={254} autoFocus={!setup} /></Field>
    <Field label="Password" htmlFor="password" hint={setup ? "Use 15+ characters. A memorable passphrase works well." : undefined}><SecretInput id="password" name="password" autoComplete={setup ? "new-password" : "current-password"} minLength={setup ? 15 : undefined} maxLength={256} /></Field>
    {error && <ErrorNotice message={error} />}<Button loading={busy} type="submit">{busy ? "Please wait…" : setup ? "Create administrator" : "Sign in"}<ArrowRight size={17} /></Button>
    {!setup && <button type="button" className="link" onClick={() => { setStep({ kind: "reset" }); setError(""); }}>Reset access</button>}
  </form>;
}

function CodeField() {
  return <Field label="Code" htmlFor="code"><input id="code" name="code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9 ]{6,7}" maxLength={7} required autoFocus placeholder="123 456" /></Field>;
}

/** Codes depend on the time; a device far off the server's clock never matches. */
function ClockWarning({ serverTime }: { serverTime: number }) {
  if (!serverTime || Math.abs(serverTime - Date.now() / 1000) <= 30) return null;
  return <p className="caption warn-text">This device's clock differs from the server's by more than 30 seconds; codes may not match. Check the time on your phone and computer.</p>;
}

function EnrolStep({ view, busy, error, onSubmit }: { view: EnrolView; busy: boolean; error: string; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  const [copied, setCopied] = useState(false);
  return <form onSubmit={onSubmit}>
    <p className="auth-step-hint">Add an account to your authenticator app (for example 1Password, Google Authenticator or Aegis) by entering this key by hand. Choose "time-based"; the app will show 6-digit codes.</p>
    <div className="enrol-key"><code aria-label="Authenticator key">{view.key}</code><button type="button" className="link" onClick={() => void navigator.clipboard.writeText(view.key.replace(/ /g, "")).then(() => setCopied(true))}>{copied ? "Copied" : "Copy key"}</button></div>
    <p className="caption">Account: {view.account} · Issuer: {view.issuer}. Keep this key somewhere safe only if your app cannot back it up; losing the app means resetting access over SSH.</p>
    <CodeField />
    <ClockWarning serverTime={view.server_time} />
    {error && <ErrorNotice message={error} />}<Button loading={busy} type="submit">{busy ? "Checking…" : "Confirm and continue"}<ArrowRight size={17} /></Button>
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
