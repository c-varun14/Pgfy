import { CopyButton } from "./copy-button";
export function CodeBlock({ value, label, copyLabel = "Copy", copy = true }: { value: string; label?: string; copyLabel?: string; copy?: boolean }) {
  return <div className="code-block">{label && <span className="code-label">{label}</span>}<pre><code>{value}</code></pre>{copy && <CopyButton value={value} label={copyLabel} />}</div>;
}
