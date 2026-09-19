import { useState, type FormEvent } from "react";
import { ArrowRight, Database, ShieldCheck } from "lucide-react";
import { Button } from "../components/ui/button";
import { api } from "../api";
import { Brand, ErrorNotice } from "../ui";

export function AuthPage({ setup, error, onRetry, onSuccess }: { setup: boolean; error: string; onRetry: () => void; onSuccess: () => Promise<void> }) {
  return (
    <div className="auth-layout">
      <aside className="auth-aside">
        <Brand />
        <div>
          <span className="eyebrow">A home for your data</span>
          <h1>
            Your server.
            <br />
            Your databases.
            <br />
            <em>Your control.</em>
          </h1>
          <p>A small, deliberate foundation for the things you’re building.</p>
        </div>
        <span className="aside-footer">
          <Database size={16} />
          Self-hosted PostgreSQL
        </span>
      </aside>
      <main className="auth-main">
        <div className="auth-card">
          <span className="eyebrow">{setup ? "Welcome to Pgfy" : "Welcome back"}</span>
          <h2>{setup ? "Make yourself at home." : "Sign in to your server."}</h2>
          <p className="muted">
            {setup ? "Create the administrator account for this installation." : "Your databases stay on the server you control."}
          </p>
          {error && (
            <>
              <ErrorNotice message={error} />
              <Button variant="outline" onClick={onRetry}>
                Retry connection
              </Button>
            </>
          )}
          <AuthForm setup={setup} onSuccess={onSuccess} />
          <p className="fine-print">
            <ShieldCheck size={15} />
            {location.protocol === "https:" ? "Connected over HTTPS" : "HTTP access — use only through your SSH tunnel"}
          </p>
        </div>
      </main>
    </div>
  );
}

function AuthForm({ setup, onSuccess }: { setup: boolean; onSuccess: () => Promise<void> }) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(event.currentTarget);
    try {
      await api(setup ? "/setup" : "/auth/login", {
        method: "POST",
        body: JSON.stringify({
          email: data.get("email"),
          password: data.get("password"),
          ...(setup ? { token: data.get("token") } : {}),
        }),
      });
      await onSuccess();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)}>
      {setup && (
        <div className="field">
          <label htmlFor="setup-token">Setup token</label>
          <input id="setup-token" name="token" type="password" autoComplete="off" required maxLength={43} aria-describedby="token-hint" />
          <small id="token-hint">Copy the 30-minute token from your installer terminal.</small>
        </div>
      )}
      <div className="field">
        <label htmlFor="email">Email address</label>
        <input id="email" name="email" type="email" placeholder="you@example.com" autoComplete="username" required maxLength={254} />
      </div>
      <div className="field">
        <label htmlFor="password">Password</label>
        <input
          id="password"
          name="password"
          type="password"
          autoComplete={setup ? "new-password" : "current-password"}
          required
          minLength={setup ? 15 : undefined}
          maxLength={256}
          aria-describedby={setup ? "password-hint" : undefined}
        />
        {setup && <small id="password-hint">Use at least 15 characters. A passphrase works well.</small>}
      </div>
      {error && <ErrorNotice message={error} />}
      <Button disabled={busy} type="submit">
        {busy ? "Please wait…" : setup ? "Create administrator" : "Sign in"}
        <ArrowRight size={17} />
      </Button>
    </form>
  );
}
