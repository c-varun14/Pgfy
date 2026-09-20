import type { ReactNode } from "react";
export function EmptyState({ icon, title, children, action }: { icon?: ReactNode; title: string; children?: ReactNode; action?: ReactNode }) {
  return <div className="empty-state">{icon}<h2>{title}</h2>{children && <p>{children}</p>}{action}</div>;
}
