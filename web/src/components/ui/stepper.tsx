import { Check, TriangleAlert } from "lucide-react";
export function Stepper({ steps }: { steps: { label: string; state: "done" | "active" | "pending" | "failed" }[] }) {
  return <ol className="stepper">{steps.map((step) => <li key={step.label} data-state={step.state}><span>{step.state === "done" ? <Check size={14} /> : step.state === "failed" ? <TriangleAlert size={14} /> : null}</span>{step.label}</li>)}</ol>;
}
