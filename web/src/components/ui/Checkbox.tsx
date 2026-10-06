import type { ReactNode } from "react";

export function Checkbox({ checked, onChange, label, hint }: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  hint?: string;
}) {
  return (
    <label className="flex items-start gap-2 text-sm">
      <input type="checkbox" className="mt-0.5 accent-[var(--fm-accent)]" checked={checked}
        onChange={(e) => onChange(e.target.checked)} />
      <span>
        {label}
        {hint && <span className="block text-xs text-muted">{hint}</span>}
      </span>
    </label>
  );
}
