import type { ReactNode } from "react";
export type PillTone = "good" | "wait" | "bad" | "neutral";
export function Pill({ tone = "neutral", children }: { tone?: PillTone; children: ReactNode }) {
  return <span className={`pill pill-${tone}`}>{tone === "wait" && <span className="pulse-dot" aria-hidden="true" />}{children}</span>;
}
