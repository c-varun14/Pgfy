import type { ReactNode } from "react";
export function Tooltip({ text, children }: { text: string; children: ReactNode }) { return <span className="tooltip" data-tip={text}>{children}</span>; }
