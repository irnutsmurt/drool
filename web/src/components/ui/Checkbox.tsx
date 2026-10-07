import type { ReactNode } from "react";
import { cx } from "./cx";

export function Checkbox({ checked, onChange, label, hint, disabled }: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  hint?: string;
  disabled?: boolean;
}) {
  return (
    <label className={cx("flex items-start gap-2 text-sm", disabled && "opacity-50")}>
      <input type="checkbox" className="mt-0.5 accent-[var(--ui-accent)]" checked={checked} disabled={disabled}
        onChange={(e) => onChange(e.target.checked)} />
      <span>
        {label}
        {hint && <span className="block text-xs text-muted">{hint}</span>}
      </span>
    </label>
  );
}
