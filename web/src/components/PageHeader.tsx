import type { ReactNode } from "react";
export function PageHeader({ title, description, eyebrow, status, actions }: { title: string; description?: string; eyebrow?: ReactNode; status?: ReactNode; actions?: ReactNode }) {
  return <header className="page-header"><div>{eyebrow}{<div className="page-title"><h1>{title}</h1>{status}</div>}{description && <p>{description}</p>}</div>{actions && <div className="page-actions">{actions}</div>}</header>;
}
