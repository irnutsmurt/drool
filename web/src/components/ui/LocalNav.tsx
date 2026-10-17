import type { ReactNode } from "react";
import { Link } from "react-router";
import { cx } from "./cx";

export interface LocalTab {
  to: string;
  label: ReactNode;
  current: boolean;
  /** A count shown after the label. */
  count?: ReactNode;
}

/** A page's one level of local navigation: peer views of the same thing,
 * as links (each view has its own address). Scrolls sideways on narrow
 * screens instead of wrapping into a second row. */
export function LocalNav({ label, tabs }: { label: string; tabs: LocalTab[] }) {
  return (
    <nav aria-label={label} className="-mx-1 mb-4 overflow-x-auto px-1">
      <ul className="flex min-w-max gap-1 border-b border-line">
        {tabs.map((t) => (
          <li key={t.to}>
            <Link to={t.to} aria-current={t.current ? "page" : undefined}
              className={cx("-mb-px inline-block whitespace-nowrap border-b-2 px-3 py-2 text-sm pointer-coarse:py-3",
                t.current ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")}>
              {t.label}
              {t.count != null && <span className="ml-1.5 tabular-nums text-muted" data-count="">{t.count}</span>}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}
