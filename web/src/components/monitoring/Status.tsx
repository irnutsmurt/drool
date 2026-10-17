import type { ReactNode } from "react";
import { cx } from "@/components/ui/cx";

/** Health status: an icon and a word, never colour alone. */
export type Status = "ok" | "info" | "warning" | "critical" | "unknown";

const look: Record<Status, { icon: string; cls: string; word: string }> = {
  ok: { icon: "✓", cls: "bg-ok-bg text-ok", word: "OK" },
  info: { icon: "ℹ", cls: "bg-accent/15 text-accent", word: "Info" },
  warning: { icon: "⚠", cls: "bg-warn-bg text-warn", word: "Warning" },
  critical: { icon: "✕", cls: "bg-danger-bg text-danger", word: "Critical" },
  unknown: { icon: "?", cls: "bg-surface-2 text-muted", word: "Unknown" },
};

/** The status in words, for screen readers next to an icon-only status. */
export const statusWord = (s: Status) => look[s].word;

export function statusOf(s: string | undefined): Status {
  return s === "ok" || s === "info" || s === "warning" || s === "critical" ? s : "unknown";
}

export function StatusIcon({ status, className }: { status: Status; className?: string }) {
  return (
    <span aria-hidden="true" className={cx("inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-xs font-bold", look[status].cls, className)}>
      {look[status].icon}
    </span>
  );
}

export function StatusBadge({ status, children }: { status: Status; children?: ReactNode }) {
  return (
    <span className={cx("inline-flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-0.5 text-xs font-medium", look[status].cls)}>
      <span aria-hidden="true">{look[status].icon}</span>
      {children ?? look[status].word}
    </span>
  );
}

/** Storage device health, with honest words for limited telemetry. */
export const deviceHealth: Record<string, { status: Status; word: string }> = {
  ok: { status: "ok", word: "Healthy" },
  warning: { status: "warning", word: "Warning" },
  failing: { status: "critical", word: "Failing" },
  no_indicators: { status: "info", word: "No failure indicators" },
  unknown: { status: "unknown", word: "Health unknown" },
};

export const capabilityWord: Record<string, string> = {
  full: "Full", partial: "Partial", vendor: "Vendor-specific", limited: "Limited", unavailable: "Unavailable",
};

/** The most serious severity among active issues ("ok" when none). */
export function worstOf(issues: { severity: string }[]): string {
  const rank: Record<string, number> = { ok: 0, info: 1, warning: 2, critical: 3 };
  return issues.reduce((w, i) => ((rank[i.severity] ?? 0) > (rank[w] ?? 0) ? i.severity : w), "ok");
}

/** A host's health as monitoring sees it: no data, stale, healthy,
 * degraded (its worst issue is a warning) and unhealthy (something is
 * critical) stay apart, in words as well as icon and colour. The Monitoring
 * page, the Hosts list and a host's header all use this one; DROOL's
 * connection to the host and the last reachability check are separate
 * indicators, never folded into it. */
export function HealthBadge(h: HealthInput) {
  const { status, text } = healthOf(h);
  return <StatusBadge status={status}>{text}</StatusBadge>;
}

export interface HealthInput { hasData: boolean; stale: boolean; issues: number; worst: string }

/** HealthBadge's status and words. */
export function healthOf({ hasData, stale, issues, worst }: HealthInput): { status: Status; text: string } {
  if (!hasData) return { status: "unknown", text: "No data" };
  if (stale) return { status: "warning", text: "Stale" };
  const n = `${issues} issue${issues === 1 ? "" : "s"}`;
  if (worst === "critical") return { status: "critical", text: `Unhealthy · ${n}` };
  if (worst === "warning") return { status: "warning", text: `Degraded · ${n}` };
  if (issues > 0) return { status: "info", text: `Healthy · ${issues} notice${issues === 1 ? "" : "s"}` };
  return { status: "ok", text: "Healthy" };
}

export function SeverityBadge({ severity }: { severity: string }) {
  return <StatusBadge status={statusOf(severity)}>{severity === "critical" ? "Critical" : severity === "warning" ? "Warning" : "Info"}</StatusBadge>;
}
