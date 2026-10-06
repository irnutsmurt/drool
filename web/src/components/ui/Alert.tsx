import type { ReactNode } from "react";
import { cx } from "./cx";

type Tone = "danger" | "warn" | "ok";

const tones: Record<Tone, string> = {
  danger: "border-danger/40 bg-danger-bg text-danger",
  warn: "border-warn/40 bg-warn-bg text-warn",
  ok: "border-ok/40 bg-ok-bg text-ok",
};

export function Alert({ tone = "danger", children }: { tone?: Tone; children: ReactNode }) {
  return <div role="alert" className={cx("rounded-ui border px-3 py-2 text-sm", tones[tone])}>{children}</div>;
}
