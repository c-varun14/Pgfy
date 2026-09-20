export type TabOption<T extends string> = { value: T; label: string };
export function Tabs<T extends string>({ value, options, onChange, label = "Sections" }: { value: T; options: TabOption<T>[]; onChange: (value: T) => void; label?: string }) {
  return <div className="tabs" role="tablist" aria-label={label}>{options.map((option) => <button key={option.value} type="button" role="tab" aria-selected={value === option.value} tabIndex={value === option.value ? 0 : -1} onClick={() => onChange(option.value)}>{option.label}</button>)}</div>;
}
