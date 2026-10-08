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

export function SeverityBadge({ severity }: { severity: string }) {
  return <StatusBadge status={statusOf(severity)}>{severity === "critical" ? "Critical" : severity === "warning" ? "Warning" : "Info"}</StatusBadge>;
}
