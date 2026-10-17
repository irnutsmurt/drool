import { Fragment, type ReactNode } from "react";
import { cx } from "./cx";

/**
 * Inherited values, one pattern for Monitoring and Desired State: the
 * scopes that say something about a value, broadest first, ending at the
 * most specific. The server decides which value applies (and when equal
 * priorities conflict); this only shows its answer — the value in effect
 * stands out, the values it overrides recede, an unresolved conflict says
 * so in words. Scopes the server didn't report are never invented.
 */
export interface ProvenanceLayer {
  key: string;
  /** Which scope: "Global", "Pack: lab (priority 10)", "This host"… */
  scope: ReactNode;
  /** The value this scope sets. */
  value?: ReactNode;
  /** Where it is declared, who set it. */
  detail?: ReactNode;
  state: "effective" | "overridden" | "conflict" | "unset";
}

const stateText: Record<ProvenanceLayer["state"], string> = {
  effective: "✓ In effect",
  overridden: "Overridden",
  conflict: "⚠ Conflict — not resolved",
  unset: "Not set",
};

export function Provenance({ layers, label, note, compact }: { layers: ProvenanceLayer[]; label: string; note?: ReactNode; compact?: boolean }) {
  return (
    <div className="space-y-1">
      <ol aria-label={label} className={cx("space-y-1 border-l-2 border-line", compact ? "pl-2" : "pl-3")} data-testid="provenance">
        {layers.map((l) => (
          <li key={l.key} data-state={l.state}
            className={cx("rounded-ui border px-2 text-sm", compact ? "py-0.5" : "py-1",
              l.state === "effective" ? "border-accent/60 bg-surface" : l.state === "conflict" ? "border-warn/50 bg-warn-bg" : "border-transparent")}>
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className={cx(l.state === "effective" ? "font-medium text-fg" : l.state === "conflict" ? "font-medium text-fg" : "text-muted")}>{l.scope}</span>
              {l.value !== undefined && (
                <span className={cx(l.state === "effective" ? "font-semibold text-fg" : l.state === "conflict" ? "text-fg" : "text-muted")}>{l.value}</span>
              )}
              <span className={cx("ml-auto text-xs", l.state === "effective" ? "font-semibold text-fg" : l.state === "conflict" ? "font-semibold text-warn" : "text-muted")}>
                {stateText[l.state]}
              </span>
            </div>
            {l.detail && <div className="text-xs text-muted">{l.detail}</div>}
          </li>
        ))}
      </ol>
      {note && <div className="text-xs text-muted">{note}</div>}
    </div>
  );
}

/** The order scopes inherit in, most specific last ("most specific wins"). */
export function ScopeLegend({ scopes, label = "Inheritance" }: { scopes: string[]; label?: string }) {
  return (
    <p className="flex flex-wrap items-center gap-1 text-xs text-muted">
      <span className="sr-only">{`${label}: ${scopes.join(", then ")}; the most specific wins`}</span>
      <span aria-hidden="true" className="mr-1">{label}:</span>
      {scopes.map((s, i) => (
        <Fragment key={s}>
          {i > 0 && <span aria-hidden="true">→</span>}
          <span aria-hidden="true" className="rounded border border-line bg-surface px-1.5 py-0.5 text-fg">{s}</span>
        </Fragment>
      ))}
      <span aria-hidden="true" className="ml-1">(most specific wins)</span>
    </p>
  );
}
