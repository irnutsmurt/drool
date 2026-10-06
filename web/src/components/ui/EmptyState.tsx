import type { ReactNode } from "react";

export function EmptyState({ title, body, action }: { title: string; body?: ReactNode; action?: ReactNode }) {
  return (
    <div className="mx-auto max-w-md py-16 text-center">
      <h2 className="text-base font-semibold">{title}</h2>
      {body && <p className="mt-2 text-sm text-muted">{body}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
