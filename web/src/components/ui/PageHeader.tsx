import type { ReactNode } from "react";
import { Link } from "react-router";

export interface Crumb {
  label: ReactNode;
  to?: string;
}

/** Every page's header: where you are (breadcrumbs on pages below a list),
 * the title, a line of explanation where it helps, and the actions — the
 * primary one first and filled, the others plain. On narrow screens the
 * actions wrap below the title. */
export function PageHeader({ title, description, actions, crumbs }: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  crumbs?: Crumb[];
}) {
  return (
    <div className="mb-6">
      {crumbs && crumbs.length > 0 && (
        <nav aria-label="Breadcrumb" className="mb-1 text-xs text-muted">
          <ol className="flex flex-wrap items-center gap-1">
            {crumbs.map((c, i) => (
              <li key={i} className="flex items-center gap-1">
                {c.to ? <Link className="underline-offset-2 hover:text-fg hover:underline" to={c.to}>{c.label}</Link> : <span>{c.label}</span>}
                <span aria-hidden="true">›</span>
              </li>
            ))}
          </ol>
        </nav>
      )}
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <div className="min-w-0">
          <h1 className="break-words text-xl font-semibold">{title}</h1>
          {description && <div className="mt-1 text-sm text-muted">{description}</div>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}
