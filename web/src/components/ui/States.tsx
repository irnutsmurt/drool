import { errorMessage } from "@/lib/format";

/** Data is on its way: never shown as empty, zero or healthy. */
export function Loading({ what, className }: { what?: string; className?: string }) {
  return <p className={className ?? "text-sm text-muted"} role="status">{what ? `Loading ${what}…` : "Loading…"}</p>;
}

/** Data couldn't be loaded: say so, and what failed — never "none". */
export function LoadError({ what, error, className }: { what: string; error: unknown; className?: string }) {
  return <p className={className ?? "text-sm text-danger"} role="alert">Couldn't load {what}: {errorMessage(error)}</p>;
}
