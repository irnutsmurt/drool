import type { Job } from "@/api/queries";

const order = ["succeeded", "failed", "timed_out", "running", "connecting", "waiting_lock", "queued", "cancelled", "skipped", "interrupted"];
const labels: Record<string, string> = { timed_out: "timed out", waiting_lock: "waiting" };

/** "3 succeeded · 1 failed" style summary of a job's host states. */
export function summarizeCounts(j: Pick<Job, "counts" | "host_count">): string {
  const parts = order.filter((s) => j.counts[s]).map((s) => `${j.counts[s]} ${labels[s] ?? s}`);
  return parts.length ? parts.join(" · ") : `${j.host_count} hosts`;
}
