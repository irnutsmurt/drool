import type { ReactNode } from "react";
import { cx } from "./cx";

export function Card({ title, actions, children, className }: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("rounded-ui border border-line bg-surface", className)}>
      {(title || actions) && (
        <header className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-line px-4 py-3">
          {title && <h2 className="min-w-0 flex-1 basis-24 text-sm font-semibold">{title}</h2>}
          {/* A long title wraps; the actions keep their labels on one line,
              and go below the title when both don't fit (a phone). */}
          {actions && <div className="max-w-full shrink-0">{actions}</div>}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}
