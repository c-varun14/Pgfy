import type { CSSProperties, ReactNode } from "react";
export function SegmentedControl<T extends string>({ value, options, onChange, label }: { value: T; options: { value: T; label: string; icon?: ReactNode }[]; onChange: (value: T) => void; label: string }) {
  const selectedIndex = options.findIndex((option) => option.value === value);
  return <div className="segmented" role="group" aria-label={label} style={{ "--selected-index": selectedIndex } as CSSProperties}><span className="segmented-selection" aria-hidden="true" />{options.map((option) => <button type="button" key={option.value} aria-pressed={value === option.value} onClick={() => onChange(option.value)}>{option.icon}{option.label}</button>)}</div>;
}
