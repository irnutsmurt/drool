import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input } from "@/components/ui/Field";
import type { Operation, RolloutWave, Strategy } from "./queries";

/** Rollout as edited in a form (numbers as typed). */
export interface RolloutState {
  concurrency: string;
  threshold: string;
  canary: "none" | "count" | "hosts";
  canaryCount: string;
  canaryHosts: string[];
  batch: string;
}

export const defaultRollout: RolloutState = { concurrency: "10", threshold: "", canary: "none", canaryCount: "1", canaryHosts: [], batch: "" };

/** Try one host first, then five at a time; pause after two failures. */
export const recommendedRollout: RolloutState = { concurrency: "5", threshold: "2", canary: "count", canaryCount: "1", canaryHosts: [], batch: "5" };

export function rolloutFrom(s?: Strategy | null): RolloutState {
  if (!s) return defaultRollout;
  return {
    concurrency: String(s.concurrency ?? 10),
    threshold: s.failure_threshold ? String(s.failure_threshold) : "",
    canary: s.canary_hosts?.length ? "hosts" : s.canary_count ? "count" : "none",
    canaryCount: String(s.canary_count || 1),
    canaryHosts: s.canary_hosts ?? [],
    batch: s.batch_size ? String(s.batch_size) : "",
  };
}

export function strategyFrom(r: RolloutState): Strategy {
  return {
    concurrency: Number(r.concurrency) || 10,
    failure_threshold: r.threshold ? Number(r.threshold) : null,
    canary_count: r.canary === "count" ? Number(r.canaryCount) || 1 : undefined,
    canary_hosts: r.canary === "hosts" && r.canaryHosts.length ? r.canaryHosts : undefined,
    batch_size: r.batch ? Number(r.batch) : undefined,
  };
}

/** Whether a rollout already limits the blast radius. */
export const isCautious = (s: Strategy) => !!(s.canary_count || s.canary_hosts?.length || s.batch_size || s.failure_threshold);

/** Steps that change hosts (checks and read-only commands don't). */
export const mutates = (steps: Operation[]) =>
  steps.some((s) => !["wait", "verify_host", "verify_services", "verify_health"].includes(s.type) &&
    !((s.type === "command" || s.type === "script") && s.read_only));

/** "1 canary host first, then 5 at a time; pause after 2 failures". */
export function rolloutText(s: Strategy): string {
  const parts: string[] = [];
  const n = s.canary_hosts?.length || s.canary_count || 0;
  if (n) parts.push(`${n} canary host${n === 1 ? "" : "s"} first`);
  parts.push(s.batch_size ? `${n ? "then " : ""}batches of ${s.batch_size}` : n ? "then the rest" : "all hosts in one wave");
  parts.push(`up to ${s.concurrency ?? 10} at a time`);
  parts.push(s.failure_threshold ? `pause after ${s.failure_threshold} failure${s.failure_threshold === 1 ? "" : "s"}` : "no failure limit");
  return parts.join(", ");
}

/** Suggests the conservative rollout for a change to several hosts. */
export function RolloutAdvice({ hosts, steps, strategy, apply }: { hosts: number; steps: Operation[]; strategy: Strategy; apply: () => void }) {
  if (hosts < 2 || !mutates(steps) || isCautious(strategy)) return null;
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-ui border border-accent/40 bg-accent/10 p-3 text-sm" role="note">
      <span className="min-w-0 flex-1">
        <strong>Recommended for {hosts} hosts:</strong> try one host first, then five at a time, and pause if two fail.
        A bad change then stops early instead of reaching every host.
      </span>
      <Button type="button" size="sm" onClick={apply}>Use recommended rollout</Button>
    </div>
  );
}

/** Canary, batches, concurrency and the failure threshold. */
export function RolloutFields({ v, set, hosts, noExplicit }: {
  v: RolloutState;
  set: (v: RolloutState) => void;
  /** Hosts that can be explicit canaries (the current targets). */
  hosts: { id: string; name: string }[];
  /** No explicit canaries (a workflow's default rollout has no targets). */
  noExplicit?: boolean;
}) {
  const pick = (id: string, on: boolean) => set({ ...v, canaryHosts: on ? [...v.canaryHosts, id] : v.canaryHosts.filter((x) => x !== id) });
  return (
    <div className="space-y-4">
      <fieldset className="space-y-2">
        <legend className="mb-1 text-sm font-medium">Canary</legend>
        <p className="text-xs text-muted">Canary hosts run first, on their own. If one fails, the job pauses before any other host starts.</p>
        {([["none", "No canary"], ["count", "The first hosts by name"], ["hosts", "Hosts I choose"]] as const).filter(([k]) => !noExplicit || k !== "hosts").map(([k, label]) => (
          <label key={k} className="flex items-center gap-2 text-sm">
            <input type="radio" name="canary" checked={v.canary === k} onChange={() => set({ ...v, canary: k })} />
            {label}
          </label>
        ))}
        {v.canary === "count" && (
          <Field label="Number of canary hosts" hint="1–10.">
            <Input type="number" min={1} max={10} className="max-w-[8rem]" value={v.canaryCount} onChange={(e) => set({ ...v, canaryCount: e.target.value })} />
          </Field>
        )}
        {v.canary === "hosts" && (
          <div className="max-h-40 space-y-1 overflow-y-auto rounded-ui border border-line p-2">
            {hosts.length === 0 && <p className="text-xs text-muted">Choose targets first.</p>}
            {hosts.map((h) => (
              <Checkbox key={h.id} checked={v.canaryHosts.includes(h.id)} disabled={!v.canaryHosts.includes(h.id) && v.canaryHosts.length >= 10}
                onChange={(on) => pick(h.id, on)} label={h.name} />
            ))}
          </div>
        )}
      </fieldset>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Batch size" hint="Hosts per wave after the canaries. Empty: all at once.">
          <Input type="number" min={1} max={1000} value={v.batch} onChange={(e) => set({ ...v, batch: e.target.value })} />
        </Field>
        <Field label="Hosts at a time" hint="Within a wave.">
          <Input type="number" min={1} max={100} value={v.concurrency} onChange={(e) => set({ ...v, concurrency: e.target.value })} />
        </Field>
        <Field label="Pause after failures" hint="Empty: never pause for failures.">
          <Input type="number" min={1} value={v.threshold} onChange={(e) => set({ ...v, threshold: e.target.value })} />
        </Field>
      </div>
    </div>
  );
}

/** The exact waves a rollout would run (the preview's resolution). */
export function WavesList({ waves, max = 12 }: { waves: RolloutWave[]; max?: number }) {
  if (waves.length === 0) return null;
  const shown = waves.slice(0, max);
  const offset = waves[0]?.canary ? 0 : 1;
  return (
    <ol className="space-y-1 text-sm" aria-label="Rollout waves">
      {shown.map((w) => (
        <li key={w.index} className="flex gap-2">
          <span className="w-20 shrink-0 font-medium">{w.canary ? "Canary" : `Batch ${w.index + offset}`}</span>
          <span className="min-w-0 text-muted">
            {w.hosts.slice(0, 8).join(", ")}{w.hosts.length > 8 && ` and ${w.hosts.length - 8} more`}
          </span>
        </li>
      ))}
      {waves.length > max && <li className="text-xs text-muted">… and {waves.length - max} more waves</li>}
    </ol>
  );
}
