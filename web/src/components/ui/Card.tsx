import type { ReactNode } from "react";
import { cx } from "./cx";

export function Card({ title, actions, children, className }: {
  title?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("rounded-ui border border-line bg-surface", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
          {title && <h2 className="min-w-0 text-sm font-semibold">{title}</h2>}
          {/* A long title wraps; the action keeps its label on one line. */}
          {actions && <div className="shrink-0">{actions}</div>}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}
