import { Badge } from "@/components/ui/Badge";
import type { Job, JobHost } from "@/api/queries";

type Tone = "ok" | "warn" | "danger" | "neutral" | "accent";

const jobTones: Record<Job["status"], Tone> = {
  pending: "neutral", running: "accent", paused: "warn", completed: "ok", cancelled: "neutral", interrupted: "warn",
};

/** Completed jobs with failures are shown as such, not as plain "completed". */
export function JobStatusBadge({ job }: { job: Pick<Job, "status" | "counts"> }) {
  const failed = (job.counts.failed ?? 0) + (job.counts.timed_out ?? 0);
  if (job.status === "completed" && failed > 0) return <Badge tone="danger">completed with failures</Badge>;
  return <Badge tone={jobTones[job.status]}>{job.status}</Badge>;
}

const hostTones: Record<JobHost["state"], Tone> = {
  queued: "neutral", waiting_lock: "warn", connecting: "accent", running: "accent", rebooting: "accent",
  waiting_host: "accent", verifying: "accent", succeeded: "ok", failed: "danger", timed_out: "danger",
  cancelled: "neutral", skipped: "neutral", interrupted: "warn",
};

const hostLabels: Partial<Record<JobHost["state"], string>> = {
  waiting_lock: "waiting for host", timed_out: "timed out", waiting_host: "waiting for host to return",
};

export function HostStateBadge({ state }: { state: JobHost["state"] }) {
  return <Badge tone={hostTones[state]}>{hostLabels[state] ?? state}</Badge>;
}

/** Plain-language explanations for error classes (ARCHITECTURE-ALPHA §14). */
export const errorExplanations: Record<string, string> = {
  ConnectionError: "DROOL could not reach the host (SSH or Shepherd connection).",
  AuthenticationError: "The host rejected DROOL's credential.",
  HostKeyError: "The host's SSH key does not match the trusted key, so DROOL did not connect.",
  AuthorizationError: "Not allowed: sudo, file permissions or the host's management state refused this.",
  TimeoutError: "The operation did not finish within its timeout.",
  ExecutionError: "The command ran but exited with an error.",
  ConfigurationError: "The host is missing something this operation needs.",
  ValidationError: "DROOL refused to make this change.",
  PackageManagerError: "The package manager reported a problem.",
  ServiceError: "The service could not be put in the requested state.",
  CancelledError: "The job was cancelled.",
  LockTimeoutError: "Another job kept this host busy for too long.",
  InterruptedError: "The controller stopped (a restart, or a restore from a backup) while this host was running; the outcome is unknown.",
  InternalError: "Something went wrong inside DROOL. The controller log has details.",
  CapabilityError: "Refused by the host's Shepherd (DROOL agent): outside what the host allows.",
  VerificationError: "A check after the change didn't pass (the host, its services or its health).",
};
