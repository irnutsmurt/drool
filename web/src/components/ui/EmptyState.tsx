import type { ReactNode } from "react";

/** Nothing here yet: what this is, why you'd use it, and what to do next
 * (an action only where the user may take it). */
export function EmptyState({ title, body, action }: { title: string; body?: ReactNode; action?: ReactNode }) {
  return (
    <div className="mx-auto max-w-md py-10 text-center sm:py-16">
      <h2 className="text-base font-semibold">{title}</h2>
      {body && <div className="mt-2 text-sm text-muted">{body}</div>}
      {action && <div className="mt-4 flex flex-wrap justify-center gap-2">{action}</div>}
    </div>
  );
}
