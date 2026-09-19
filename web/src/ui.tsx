import { useState, type ReactNode } from "react";
import { Check, Copy, Database, TriangleAlert } from "lucide-react";
import { Button } from "./components/ui/button";

export function Brand() {
  return (
    <a href="/" className="brand" aria-label="Pgfy home">
      <span className="brand-icon">
        <Database size={22} />
      </span>
      pgfy<span className="brand-dot">.</span>
    </a>
  );
}
export function ErrorNotice({ message }: { message: string }) {
  return (
    <div className="notice error" role="alert">
      <TriangleAlert size={18} />
      <span>{message}</span>
    </div>
  );
}
export function Notice({ children }: { children: ReactNode }) {
  return <div className="notice">{children}</div>;
}
export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="outline"
      className="copy"
      type="button"
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        });
      }}
    >
      {copied ? <Check size={14} /> : <Copy size={14} />}
      {copied ? "Copied" : label}
    </Button>
  );
}
export function CodeBlock({ value, label }: { value: string; label?: string }) {
  return (
    <div className="code-block">
      {label && <span className="code-label">{label}</span>}
      <pre>
        <code>{value}</code>
      </pre>
      <CopyButton value={value} />
    </div>
  );
}
export type Tone = "good" | "wait" | "bad" | "neutral";
export function Pill({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`pill pill-${tone}`}>{children}</span>;
}
