import type { KeyboardEvent, ReactNode } from "react";
import { cx } from "./cx";

export interface TabItem<K extends string> {
  key: K;
  label: ReactNode;
}

/** A row of tabs (the ARIA tabs pattern) switching what a page shows: the
 * row is one Tab stop, ←/→ (and Home/End) move to a tab and show it, the
 * shown one is marked selected, and its panel (TabPanel) is named by it.
 * The page may keep the tab in its address (?tab=) so it can be linked. */
export function Tabs<K extends string>({ label, tabs, current, onSelect, id, className, small }: {
  label: string;
  tabs: TabItem<K>[];
  current: K;
  onSelect: (k: K) => void;
  /** Prefix for the tabs' and panel's ids. */
  id: string;
  className?: string;
  /** Compact tabs (a side panel). */
  small?: boolean;
}) {
  const onKey = (e: KeyboardEvent, i: number) => {
    const n = e.key === "ArrowRight" ? i + 1 : e.key === "ArrowLeft" ? i - 1 : e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : null;
    if (n === null) return;
    e.preventDefault();
    const k = tabs[(n + tabs.length) % tabs.length]!.key;
    onSelect(k);
    document.getElementById(`${id}-tab-${k}`)?.focus();
  };
  return (
    <div role="tablist" aria-label={label} className={cx("flex gap-1 border-b border-line", className)}>
      {tabs.map((t, i) => (
        <button key={t.key} id={`${id}-tab-${t.key}`} type="button" role="tab" aria-selected={current === t.key} aria-controls={`${id}-panel`}
          tabIndex={current === t.key ? 0 : -1} onKeyDown={(e) => onKey(e, i)} onClick={() => onSelect(t.key)}
          className={cx("-mb-px whitespace-nowrap border-b-2 text-sm", small ? "px-2 py-1" : "px-3 py-2",
            current === t.key ? "border-accent font-medium" : "border-transparent text-muted hover:text-fg")}>
          {t.label}
        </button>
      ))}
    </div>
  );
}

/** The view a Tabs row shows, named by its selected tab. */
export function TabPanel<K extends string>({ id, current, className, children }: { id: string; current: K; className?: string; children: ReactNode }) {
  return <div role="tabpanel" id={`${id}-panel`} aria-labelledby={`${id}-tab-${current}`} className={className}>{children}</div>;
}
