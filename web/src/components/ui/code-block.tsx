import { CopyButton } from "./copy-button";
export function CodeBlock({ value, label, copyLabel = "Copy" }: { value: string; label?: string; copyLabel?: string }) {
  return <div className="code-block">{label && <span className="code-label">{label}</span>}<pre><code>{value}</code></pre><CopyButton value={value} label={copyLabel} /></div>;
}
