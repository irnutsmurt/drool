import type { ReactNode } from "react";
import { cx } from "./cx";

type Tone = "neutral" | "ok" | "warn" | "danger" | "accent";

const tones: Record<Tone, string> = {
  neutral: "bg-surface-2 text-muted",
  ok: "bg-ok-bg text-ok",
  warn: "bg-warn-bg text-warn",
  danger: "bg-danger-bg text-danger",
  accent: "bg-accent/15 text-accent",
};

/** A short label. `wrap` for one that says a sentence (it may break onto a
 * second line in a narrow card instead of widening it). */
export function Badge({ tone = "neutral", wrap, children }: { tone?: Tone; wrap?: boolean; children: ReactNode }) {
  return (
    <span className={cx("inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium", wrap ? "whitespace-normal" : "whitespace-nowrap", tones[tone])}>
      {children}
    </span>
  );
}
