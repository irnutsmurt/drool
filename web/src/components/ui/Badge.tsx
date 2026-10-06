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

export function Badge({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span className={cx("inline-flex items-center whitespace-nowrap rounded px-1.5 py-0.5 text-xs font-medium", tones[tone])}>
      {children}
    </span>
  );
}
