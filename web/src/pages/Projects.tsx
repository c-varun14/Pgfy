import { useEffect, useState, type FormEvent } from "react";
import { ArrowRight, Database, Plus } from "lucide-react";
import { Button } from "../components/ui/button";
import { api, formatBytes, formatDate, type Project } from "../api";
import { ErrorNotice, Pill } from "../ui";

export function stageLabel(p: Project) {
  if (p.failed) return { tone: "bad" as const, text: "Needs attention" };
  if (p.stage === "ready") return { tone: "good" as const, text: "Ready" };
  return { tone: "wait" as const, text: "Creating…" };
}

export function ProjectsPage({ navigate }: { navigate: (to: string) => void }) {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const body = await api<{ projects: Project[] }>("/projects");
      setProjects(body.projects);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    void load();
  }, []);
  // Poll only while something is still being created.
  const pending = projects?.some((p) => !p.failed && p.stage !== "ready");
  useEffect(() => {
    if (!pending) return;
    const timer = setInterval(() => void load(), 2000);
    return () => clearInterval(timer);
  }, [pending]);

  async function create(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const created = await api<Project>("/projects", {
        method: "POST",
        body: JSON.stringify({ name: name.trim(), idempotency_key: crypto.randomUUID() }),
      });
      setName("");
      navigate(`/projects/${created.id}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <section className="panel create-panel">
        <form className="create-form" onSubmit={(e) => void create(e)}>
          <div className="field">
            <label htmlFor="project-name">New project</label>
            <input
              id="project-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Shop, Blog, Side project"
              maxLength={64}
              required
            />
            <small>One name is all it takes. Pgfy creates the database, a dedicated user, and a strong password.</small>
          </div>
          <Button type="submit" disabled={busy || !name.trim()}>
            <Plus size={16} />
            {busy ? "Creating…" : "Create database"}
          </Button>
        </form>
      </section>
      {error && <ErrorNotice message={error} />}
      {projects === null ? (
        !error && <p role="status">Loading projects…</p>
      ) : projects.length === 0 ? (
        <section className="panel empty">
          <span className="section-icon">
            <Database size={23} />
          </span>
          <h2>No databases yet</h2>
          <p className="muted">Create your first project above. It takes a few seconds.</p>
        </section>
      ) : (
        <div className="project-grid">
          {projects.map((p) => {
            const stage = stageLabel(p);
            return (
              <button key={p.id} className="panel project-card" onClick={() => navigate(`/projects/${p.id}`)}>
                <div className="project-card-top">
                  <span className="section-icon">
                    <Database size={20} />
                  </span>
                  <Pill tone={stage.tone}>{stage.text}</Pill>
                </div>
                <h2>{p.name}</h2>
                <p className="muted mono">{p.db_name}</p>
                <div className="project-card-meta">
                  <span>{p.stage === "ready" ? formatBytes(p.size_bytes) : "—"}</span>
                  <span>Created {formatDate(p.created_at)}</span>
                  <ArrowRight size={15} />
                </div>
              </button>
            );
          })}
        </div>
      )}
    </>
  );
}
