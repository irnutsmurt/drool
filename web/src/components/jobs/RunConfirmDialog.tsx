import { useState, type ReactNode } from "react";
import type { Schemas } from "@/api/client";
import { rolloutText } from "@/components/automation/Rollout";
import type { Strategy } from "@/components/automation/queries";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import type { DeploySummary } from "@/pages/files/FileDeployForm";
import { describeStep } from "@/pages/jobs/StepResult";

type Preview = Schemas["JobPreview"];
type Operation = Schemas["Operation"];

/** What a package dry run found, as far as the review needs it. */
export interface SimSummary {
  hosts: number;
  changing: number;
  packages: number;
  removing: string[];
  failed: string[];
  removalsAllowed: boolean;
}

const familyName: Record<string, string> = { debian: "Debian / Ubuntu", redhat: "RHEL family / Fedora", suse: "SUSE", arch: "Arch" };

/** "3 hosts". */
export const hostCount = (n: number) => `${n} host${n === 1 ? "" : "s"}`;

/** One step as the review shows it: the operation, how it runs, where. */
export function StepSummary({ step }: { step: Operation }) {
  const families = ("when" in step && step.when?.os_family) || [];
  return (
    <>
      {step.type === "command"
        ? <><span>Command</span> <code className="break-all font-mono text-xs">{step.command}</code>{step.become && <span className="text-warn"> · as root (sudo)</span>}{step.read_only && <span className="text-muted"> · read-only</span>}</>
        : step.type === "script"
          ? <>Script{step.become && <span className="text-warn"> · as root (sudo)</span>}{step.read_only && <span className="text-muted"> · read-only</span>}</>
          : describeStep(step)}
      {families.length > 0 && <div className="text-xs text-muted">Only on {families.map((f) => familyName[f] ?? f).join(", ")} hosts.</div>}
      {"timeout_s" in step && step.timeout_s ? <div className="text-xs text-muted">Stops on a host after {step.timeout_s} seconds.</div> : null}
    </>
  );
}

/** A run reviewed before it starts: what it does, on which hosts (the
 * preview's resolution, a long list folded), how it rolls out, what the
 * preview found in the way. The body of the New run confirmation and of a
 * workflow's run review. */
export function RunReview({ what, preview, strategy, consequences = [], found }: {
  what: ReactNode;
  preview: Preview;
  strategy: Strategy;
  /** Plain sentences about what happens beyond the steps themselves. */
  consequences?: string[];
  /** What a dry run or deployment preview found, if the step has one. */
  found?: ReactNode;
}) {
  const [allHosts, setAllHosts] = useState(false);
  const run = preview.hosts.filter((h) => !h.skip);
  const skipped = preview.hosts.filter((h) => h.skip);
  const flagged = run.filter((h) => h.warning);
  const shown = allHosts ? run : run.slice(0, 8);
  const warnings = [...preview.warnings, ...preview.desired_warnings.map((w) => w.message)];
  return (
    <div className="space-y-3 text-sm">
      <dl className="grid grid-cols-[minmax(5.5rem,7rem)_1fr] gap-x-3 gap-y-2" data-testid="run-review">
        <dt className="text-muted">What</dt>
        <dd>{what}</dd>
        <dt className="text-muted">On</dt>
        <dd>
          <span className="font-medium">{hostCount(run.length)}</span>
          {run.length > 0 && (
            <ul className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs">
              {shown.map((h) => <li key={h.id}>{h.name}</li>)}
              {!allHosts && run.length > shown.length && (
                <li><button type="button" className="underline underline-offset-2" onClick={() => setAllHosts(true)}>and {run.length - shown.length} more</button></li>
              )}
            </ul>
          )}
        </dd>
        <dt className="text-muted">Rollout</dt>
        <dd>{run.length < 2 ? "One host." : rolloutText(strategy)}
          {run.length >= 2 && !strategy.canary_count && !strategy.canary_hosts?.length && !strategy.batch_size && (
            <div className="text-xs text-warn">Every host at once: a bad change reaches all of them before anything stops it.</div>
          )}
        </dd>
        {found && (
          <>
            <dt className="text-muted">Preview</dt>
            <dd data-testid="run-preview-found">{found}</dd>
          </>
        )}
      </dl>
      {consequences.length > 0 && (
        <ul className="list-disc space-y-1 pl-5" data-testid="run-consequences">{consequences.map((c) => <li key={c}>{c}</li>)}</ul>
      )}
      {(skipped.length > 0 || flagged.length > 0 || warnings.length > 0) && (
        <div className="space-y-1 rounded-ui border border-warn/40 bg-warn-bg px-3 py-2 text-warn" data-testid="run-warnings">
          {skipped.length > 0 && <p><strong>Skipped ({skipped.length}):</strong> {skipped.map((h) => `${h.name}${h.warning ? ` — ${h.warning}` : ""}`).join("; ")}</p>}
          {flagged.map((h) => <p key={h.id}><strong>{h.name}:</strong> {h.warning}</p>)}
          {warnings.map((w) => <p key={w}>{w}</p>)}
        </div>
      )}
    </div>
  );
}

/** What a package dry run found. */
function SimFound({ sim }: { sim: SimSummary }) {
  return (
    <>
      {sim.packages} package change{sim.packages === 1 ? "" : "s"} on {sim.changing} of {hostCount(sim.hosts)}
      {sim.removing.length > 0 && <div className={sim.removalsAllowed ? "text-warn" : "text-danger"}>
        {hostCount(sim.removing.length)} would also remove packages ({sim.removing.join(", ")}): {sim.removalsAllowed ? "allowed" : "not allowed, so the run stops on them before changing anything"}.
      </div>}
      {sim.failed.length > 0 && <div className="text-warn">The dry run failed on {sim.failed.join(", ")}.</div>}
    </>
  );
}

/** What a deployment preview found. */
function DeployFound({ d }: { d: DeploySummary }) {
  return (
    <>
      On {hostCount(d.hosts)}: {d.outcomes.map(([what, n]) => `${n} ${what}`).join(", ") || "nothing to compare"}.
      {d.skipped.length > 0 && <div className="text-muted">Skipped: {d.skipped.join(", ")}.</div>}
      {d.failed.length > 0 && <div className="text-warn">Couldn&apos;t check {d.failed.join(", ")}.</div>}
    </>
  );
}

/** Starting a run, reviewed (Pattern B), then Start. Replaces the
 * browser's own confirm box. The server still checks permissions, host
 * locks, capabilities and every operation's own safety rules when the run
 * starts. */
export function RunConfirmDialog({ step, preview, strategy, consequences, sim, deploy, busy, error, onConfirm, onClose }: {
  step: Operation;
  preview: Preview;
  strategy: Strategy;
  consequences: string[];
  /** A package or update step's dry run, if one ran. */
  sim?: SimSummary | null;
  /** A file deployment's preview, if one ran. */
  deploy?: DeploySummary | null;
  busy?: boolean;
  error?: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const n = hostCount(preview.hosts.filter((h) => !h.skip).length);
  const reboot = step.type === "reboot";
  const previewable = step.type === "package" || step.type === "system_update" || step.type === "file_deploy";
  const found = sim ? <SimFound sim={sim} /> : deploy ? <DeployFound d={deploy} />
    : previewable ? <span className="text-muted">Not previewed: Preview changes shows what each host would get, without changing anything.</span> : undefined;
  return (
    <ConfirmDialog title={reboot ? `Reboot ${n}?` : `Start this run on ${n}?`} confirmLabel={reboot ? `Reboot ${n}` : `Start run on ${n}`}
      danger={reboot} busy={busy} error={error} onConfirm={onConfirm} onClose={onClose} wide testId="run-confirm">
      <RunReview what={<StepSummary step={step} />} preview={preview} strategy={strategy} consequences={consequences} found={found} />
    </ConfirmDialog>
  );
}
