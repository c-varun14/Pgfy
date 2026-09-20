import type { ReactNode } from "react";
import { CopyButton } from "./copy-button";
export function DetailsList({ items }: { items: { label: string; value: ReactNode; copy?: string }[] }) {
  return <dl className="details-list">{items.map((item) => <div key={item.label}><dt>{item.label}</dt><dd>{item.value}{item.copy && <CopyButton value={item.copy} label={`Copy ${item.label.toLowerCase()}`} />}</dd></div>)}</dl>;
}
