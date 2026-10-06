import type { ProbeResult } from "@/api/queries";
import { cx } from "@/components/ui/cx";

const icon = { ok: "✓", failed: "✕", skipped: "–" } as const;
const tone = { ok: "text-ok", failed: "text-danger", skipped: "text-muted" } as const;

/** Step-by-step connection test results. */
export function ProbeSteps({ result }: { result: Pick<ProbeResult, "steps"> }) {
  return (
    <ul className="space-y-2">
      {result.steps.map((s) => (
        <li key={s.name} className="flex gap-3 text-sm">
          <span className={cx("w-4 shrink-0 text-center font-semibold", tone[s.status])}>{icon[s.status]}</span>
          <div className="min-w-0">
            <span className="font-medium capitalize">{s.name}</span>
            {s.detail && <span className="text-muted"> — {s.detail}</span>}
            {s.hint && s.status === "failed" && <p className="mt-0.5 text-xs text-muted">{s.hint}</p>}
          </div>
        </li>
      ))}
    </ul>
  );
}
