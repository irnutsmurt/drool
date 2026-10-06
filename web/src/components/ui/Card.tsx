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
        <header className="flex items-center justify-between border-b border-line px-4 py-3">
          {title && <h2 className="text-sm font-semibold">{title}</h2>}
          {actions}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}
