import type { ReactNode } from "react";

/** The heading of a section inside a workspace that already has a page
 * header (a host's Monitoring, Files…): a second-level title, a line of
 * explanation where it helps, and the section's own actions — smaller than
 * a page header, so the workspace's header stays the one big title. */
export function SectionHeader({ title, description, actions, id }: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  id?: string;
}) {
  return (
    <div className="mb-4 flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
      <div className="min-w-0 flex-1 basis-[22rem]">
        <h2 id={id} className="text-base font-semibold">{title}</h2>
        {description && <div className="mt-0.5 text-sm text-muted">{description}</div>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
