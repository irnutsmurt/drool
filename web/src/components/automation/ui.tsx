import { Badge } from "@/components/ui/Badge";
import type { Occurrence, Schedule } from "./queries";

type Tone = "ok" | "warn" | "danger" | "neutral" | "accent";

/** A run's outcome: the occurrence, and for a dispatched one its job. */
export function occurrenceTone(o: Occurrence): Tone {
  switch (o.status) {
    case "dispatched": {
      const failed = (o.job_counts?.failed ?? 0) + (o.job_counts?.timed_out ?? 0) + (o.job_counts?.interrupted ?? 0);
      if (o.job_status === "paused" || o.job_status === "interrupted") return "warn";
      if (o.job_status === "running" || o.job_status === "pending") return "accent";
      if (o.job_status === "cancelled") return "neutral";
      return failed > 0 ? "danger" : "ok";
    }
    case "queued":
      return "accent";
    case "missed":
    case "expired":
      return "warn";
    case "blocked":
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

const statusWords: Record<Occurrence["status"], string> = {
  queued: "queued", dispatched: "ran", skipped: "skipped", missed: "missed", expired: "expired", blocked: "blocked", failed: "could not start",
};

export function OccurrenceBadge({ o }: { o: Occurrence }) {
  let word = statusWords[o.status];
  if (o.status === "dispatched" && o.job_status) {
    const failed = (o.job_counts?.failed ?? 0) + (o.job_counts?.timed_out ?? 0) + (o.job_counts?.interrupted ?? 0);
    word = o.job_status === "completed" ? (failed > 0 ? "completed with failures" : "succeeded") : o.job_status;
  }
  if (o.status === "dispatched" && o.maintenance_id) word = "window started";
  return <Badge tone={occurrenceTone(o)}>{word}</Badge>;
}

export function ScheduleStateBadge({ s }: { s: Pick<Schedule, "enabled" | "archived" | "attention"> }) {
  if (s.archived) return <Badge>archived</Badge>;
  if (s.attention) return <Badge tone="danger">needs attention</Badge>;
  return s.enabled ? <Badge tone="ok">enabled</Badge> : <Badge>disabled</Badge>;
}
