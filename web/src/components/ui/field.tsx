import type { ReactNode } from "react";
export function Field({ label, htmlFor, hint, error, children }: { label: string; htmlFor: string; hint?: string; error?: string; children: ReactNode }) {
  return <div className="field"><label htmlFor={htmlFor}>{label}</label>{children}{hint && <small>{hint}</small>}{error && <small className="field-error">{error}</small>}</div>;
}
