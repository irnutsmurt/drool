import type { Job } from "@/api/queries";

const order = ["succeeded", "failed", "timed_out", "running", "rebooting", "waiting_host", "verifying", "connecting", "waiting_lock",
  "queued", "cancelled", "skipped", "interrupted"];
const labels: Record<string, string> = {
  timed_out: "timed out", waiting_lock: "waiting for another job", waiting_host: "waiting for host to return",
};

/** "3 succeeded · 1 failed" style summary of a job's host states. */
export function summarizeCounts(j: Pick<Job, "counts" | "host_count">): string {
  const parts = order.filter((s) => j.counts[s]).map((s) => `${j.counts[s]} ${labels[s] ?? s}`);
  return parts.length ? parts.join(" · ") : `${j.host_count} host${j.host_count === 1 ? "" : "s"}`;
}
