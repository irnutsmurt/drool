import type { ReactNode } from "react";

/** A page the user may view but not change: say so once, and what it
 * would take, instead of leaving disabled controls unexplained. */
export function ReadOnlyNote({ children }: { children: ReactNode }) {
  return (
    <p className="mb-4 rounded-ui border border-line bg-surface px-3 py-2 text-sm text-muted" role="note" data-testid="read-only-note">
      <span className="font-medium text-fg">Read-only:</span> {children}
    </p>
  );
}
