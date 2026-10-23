import type { JobHost } from "@/api/queries";

type Tone = "ok" | "warn" | "danger" | "neutral" | "accent";
type HostRun = Pick<JobHost, "host_name" | "state">;

/** A host's part of a system update, in plain words (the run's page keeps
 * the engine's own states). */
export const updateHostWord: Record<JobHost["state"], { word: string; tone: Tone }> = {
  queued: { word: "Waiting its turn", tone: "neutral" },
  waiting_lock: { word: "Waiting for another run on this host", tone: "warn" },
  connecting: { word: "Connecting", tone: "accent" },
  running: { word: "Installing updates", tone: "accent" },
  rebooting: { word: "Rebooting", tone: "accent" },
  waiting_host: { word: "Waiting for it to come back", tone: "accent" },
  verifying: { word: "Checking it afterwards", tone: "accent" },
  succeeded: { word: "Updated", tone: "ok" },
  failed: { word: "Failed", tone: "danger" },
  timed_out: { word: "Timed out", tone: "danger" },
  cancelled: { word: "Cancelled", tone: "neutral" },
  skipped: { word: "Skipped", tone: "neutral" },
  interrupted: { word: "Interrupted: outcome unknown", tone: "warn" },
};

const finished = new Set<JobHost["state"]>(["succeeded", "failed", "timed_out", "cancelled", "skipped", "interrupted"]);
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;

/** The dialog's title and one-line summary for a run of system updates:
 * what is happening while it runs, and what came of it once it ended —
 * never "installing" after the end. jobStatus is the run's status; done
 * whether it has ended. */
export function updateProgress(jobStatus: string | undefined, done: boolean, hosts: HostRun[], label: string): { title: string; summary: string } {
  const n = (s: JobHost["state"]) => hosts.filter((h) => h.state === s).length;
  if (!done) {
    const total = hosts.length;
    const ended = hosts.filter((h) => finished.has(h.state)).length;
    const summary = jobStatus === "paused" ? "Paused after a failure: the hosts not updated yet wait. Resume or cancel it on the run's page."
      : total === 0 ? "Starting…"
        : total === 1 ? `${updateHostWord[hosts[0]!.state].word} on ${hosts[0]!.host_name}…`
          : `${ended} of ${total} hosts done.`;
    return { title: `Installing updates on ${label}`, summary };
  }
  const ok = n("succeeded");
  const bad = n("failed") + n("timed_out") + n("interrupted");
  const parts = [[ok, "updated"], [n("failed"), "failed"], [n("timed_out"), "timed out"], [n("interrupted"), "interrupted"],
    [n("skipped"), "skipped"], [n("cancelled"), "cancelled"]].filter(([c]) => (c as number) > 0).map(([c, w]) => `${c} ${w}`);
  const counts = parts.join(" · ");
  if (jobStatus === "cancelled") return { title: `Updates stopped on ${label}`, summary: `The run was cancelled: ${counts || "nothing ran"}.` };
  if (jobStatus === "interrupted") return { title: `Updates interrupted on ${label}`, summary: `DROOL stopped while it ran (${counts || "no host finished"}): check the hosts.` };
  if (bad === 0 && ok === hosts.length && ok > 0) {
    return { title: `Updates installed on ${label}`, summary: ok === 1 ? `${hosts[0]!.host_name} is updated.` : `All ${plural(ok, "host")} are updated.` };
  }
  if (ok === 0 && bad > 0) return { title: `Updates failed on ${label}`, summary: `${counts}.` };
  if (ok === 0) return { title: `No updates installed on ${label}`, summary: `${counts || "No host ran"}.` };
  return { title: `Updates finished on ${label}, not everywhere`, summary: `${counts}.` };
}

/** What a host's pending-update count says after the run: it is the run's
 * outcome only once DROOL checked the host again after it finished. */
export function recheckState(finishedAt: string | null | undefined, checkedAt: string | null | undefined, now: number, waitMs = 120_000):
  "checking" | "checked" | "not_rechecked" {
  if (!finishedAt) return "checking";
  if (checkedAt && Date.parse(checkedAt) >= Date.parse(finishedAt)) return "checked";
  return now - Date.parse(finishedAt) > waitMs ? "not_rechecked" : "checking";
}
