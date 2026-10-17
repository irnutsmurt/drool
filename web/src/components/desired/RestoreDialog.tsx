import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { keepPreviousData, useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { plainLink } from "@/components/ui/links";
import { RolloutFields, recommendedRollout, defaultRollout, rolloutText, strategyFrom, type RolloutState } from "@/components/automation/Rollout";
import { WavesList } from "@/components/automation/Rollout";
import { useDebounced } from "@/hooks/useDebounced";
import { errorMessage } from "@/lib/format";
import { describeKey, useInvalidateDesired, type DesiredRestoreRequest } from "./queries";
import { diffLine } from "./ui";

/** Restore managed state: check the hosts, show the exact correction (one
 * ordinary job, each host running only its own steps), then submit it. */
export function RestoreDialog({ selection, title, onClose }: {
  selection: Pick<DesiredRestoreRequest, "host_ids" | "keys" | "baseline_id">;
  title: string;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const invalidate = useInvalidateDesired();
  const [rollout, setRollout] = useState<RolloutState | null>(null);
  // The rollout goes into the request once someone changes it: until then
  // the server's own choice (the recommended rollout for several hosts,
  // the same as the fields show) plans it, in one round trip, not two.
  const [rolloutChanged, setRolloutChanged] = useState(false);
  const [allowRemovals, setAllowRemovals] = useState(false);
  const [showRollout, setShowRollout] = useState(false);
  const req: DesiredRestoreRequest = useMemo(() => ({
    ...selection,
    allow_removals: allowRemovals || undefined,
    strategy: rollout && rolloutChanged ? strategyFrom(rollout) : undefined,
  }), [selection, allowRemovals, rollout, rolloutChanged]);
  const debounced = useDebounced(req, 400);
  // A new preview (another rollout, removals allowed) keeps the plan in
  // sight while it is worked out; Restore waits for it.
  const preview = useQuery({
    queryKey: ["desired", "restore-preview", debounced],
    queryFn: () => unwrap(api.POST("/desired/restore/preview", { body: debounced })),
    staleTime: Infinity,
    retry: false,
    placeholderData: keepPreviousData,
  });
  const plan = preview.data?.plan;
  const job = plan?.jobs[0];
  // Start from the rollout the server chose (recommended for several hosts).
  useEffect(() => {
    if (rollout === null && job) {
      setRollout(plan!.hosts > 1 ? recommendedRollout : defaultRollout);
    }
  }, [rollout, job, plan]);
  const submit = useMutation({
    mutationFn: () => unwrap(api.POST("/desired/restore", { body: { ...debounced, plan_hash: plan!.hash } })),
    onSuccess: (res) => {
      invalidate();
      onClose();
      if (res.jobs.length === 1) navigate(`/jobs/${res.jobs[0]}`);
    },
  });
  const stale = debounced !== req;
  const missing = preview.data?.missing_permissions ?? [];
  const n = plan?.items.length ?? 0;
  return (
    <Dialog title={title} onClose={onClose} wide>
      {preview.isPending && <p className="text-sm text-muted" role="status">Checking the hosts and planning the correction (nothing changes yet)…</p>}
      {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
      {plan && (
        <div className="space-y-4">
          {n === 0 ? (
            <Alert tone="ok">Nothing to restore: everything selected matches its desired state, or can't be restored right now (see below).</Alert>
          ) : (
            <p className="text-sm">
              <strong>{n} correction{n === 1 ? "" : "s"} on {plan.hosts} host{plan.hosts === 1 ? "" : "s"}</strong>, as {plan.jobs.length === 1 ? "one ordinary job" : `${plan.jobs.length} ordinary jobs`}.
              {" "}This is the plan: nothing changes until you choose Restore. Each host runs only its own corrections; DROOL checks every host again afterwards and only then calls it restored.
            </p>
          )}
          {n > 0 && (
            <div className="max-h-56 overflow-y-auto rounded-ui border border-line">
              <table className="w-full text-left text-sm" aria-label="Corrections">
                <thead>
                  <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
                    <th className="px-3 py-1.5 font-medium">Host</th>
                    <th className="px-3 py-1.5 font-medium">Resource</th>
                    <th className="px-3 py-1.5 font-medium">Now</th>
                    <th className="px-3 py-1.5 font-medium">Will be</th>
                  </tr>
                </thead>
                <tbody>
                  {plan.items.map((it) => (
                    <tr key={it.host_id + it.key} className="border-b border-line last:border-0">
                      <td className="px-3 py-1.5">{it.host_name}</td>
                      <td className="px-3 py-1.5 font-mono text-xs">{describeKey(it.key)}</td>
                      <td className="px-3 py-1.5 text-muted" title={diffLine(it.diffs)}>{it.current}</td>
                      <td className="px-3 py-1.5">{it.want}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {job && (
            <div>
              <h3 className="mb-1 text-sm font-medium">Steps</h3>
              <ol className="list-decimal space-y-0.5 pl-5 text-sm">
                {job.step_text.map((s, i) => (
                  <li key={i}>{s} <span className="text-muted">— on {job.step_hosts[i]} host{job.step_hosts[i] === 1 ? "" : "s"}</span></li>
                ))}
              </ol>
              <p className="mt-1 text-xs text-muted">Packages are installed first, then files, then services; removals come last.</p>
            </div>
          )}
          {job && plan.hosts > 1 && (
            <div className="rounded-ui border border-line p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm"><span className="font-medium">Rollout:</span> {rolloutText(job.strategy)}.</p>
                <Button size="sm" type="button" onClick={() => setShowRollout(!showRollout)} aria-expanded={showRollout}>
                  {showRollout ? "Hide rollout settings" : "Change rollout"}
                </Button>
              </div>
              {showRollout && rollout && <div className="mt-3"><RolloutFields v={rollout} set={(v) => { setRollout(v); setRolloutChanged(true); }} hosts={[]} noExplicit /></div>}
              <div className="mt-3"><WavesList waves={job.waves} /></div>
              {job.wave_note && <p className="mt-1 text-xs text-muted">{job.wave_note}</p>}
            </div>
          )}
          {plan.removals.some((r) => r.also.length > 0) && (
            <Checkbox checked={allowRemovals} onChange={setAllowRemovals}
              label="Also remove packages that depend on the prohibited ones"
              hint="Off: such a removal fails safely on that host and nothing is removed there." />
          )}
          {plan.warnings.length > 0 && (
            <Alert tone="warn">
              <ul className="list-disc space-y-1 pl-4">
                {plan.warnings.map((w, i) => <li key={i}>{w.host_name ? <><strong>{w.host_name}</strong>: </> : null}{w.message}</li>)}
              </ul>
            </Alert>
          )}
          {plan.excluded.length > 0 && (
            <details className="text-sm">
              <summary className="cursor-pointer text-muted">{plan.excluded.length} not included</summary>
              <ul className="mt-2 space-y-1">
                {plan.excluded.map((x) => (
                  <li key={x.host_id + x.key}>
                    <span className="font-medium">{x.host_name}</span> · <span className="font-mono text-xs">{describeKey(x.key)}</span>: <span className="text-muted">{x.reason}</span>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {missing.length > 0 && <Alert>Restoring this needs {missing.join(", ")}, which your role doesn't have.</Alert>}
          {submit.error && <Alert>{errorMessage(submit.error)}</Alert>}
          <div className="flex flex-wrap items-center justify-end gap-2">
            {plan.jobs.length > 0 && plan.items.some((i) => i.kind === "file") && (
              <span className="mr-auto text-xs text-muted">Review file differences first from each resource's <em>Review difference</em>.</span>
            )}
            <Button type="button" onClick={onClose}>Cancel</Button>
            <Button type="button" variant="primary" disabled={n === 0 || missing.length > 0 || stale || preview.isFetching || submit.isPending}
              onClick={() => submit.mutate()}>
              {submit.isPending ? "Starting…" : `Restore ${n} resource${n === 1 ? "" : "s"}`}
            </Button>
          </div>
          {preview.data && !preview.isFetching && (
            <p className="text-xs text-muted">Checked just now. Corrections run as an ordinary job: see it in <Link className={plainLink} to="/jobs">Jobs</Link>.</p>
          )}
        </div>
      )}
    </Dialog>
  );
}
