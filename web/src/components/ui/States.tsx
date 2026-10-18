import type { ReactNode } from "react";
import { errorMessage } from "@/lib/format";

/** Data is on its way: never shown as empty, zero or healthy. */
export function Loading({ what, className }: { what?: string; className?: string }) {
  return <p className={className ?? "text-sm text-muted"} role="status">{what ? `Loading ${what}…` : "Loading…"}</p>;
}

/** Data couldn't be loaded: say so, and what failed — never "none". */
export function LoadError({ what, error, className }: { what: string; error: unknown; className?: string }) {
  return <p className={className ?? "text-sm text-danger"} role="alert">Couldn't load {what}: {errorMessage(error)}</p>;
}

/** A list's empty text, honest (DESIGN-STANDARD §8): "Loading …" while its
 * data is on its way, "Couldn't load …" after an error (with the error,
 * unless the page already shows it above the list), and only otherwise what
 * an empty list means. */
export function emptyText(what: string, q: { isLoading: boolean; error: unknown }, empty: ReactNode, errorShownAbove = false): ReactNode {
  if (q.error) {
    return <span className="text-danger" role="alert">Couldn&apos;t load {what}{errorShownAbove ? "." : <>: {errorMessage(q.error)}</>}</span>;
  }
  if (q.isLoading) return `Loading ${what}…`;
  return empty;
}
