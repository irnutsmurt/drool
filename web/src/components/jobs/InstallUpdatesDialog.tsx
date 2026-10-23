import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { jk, qk, type Host, type JobHost } from "@/api/queries";
import { defaultRollout, recommendedRollout, rolloutText, strategyFrom } from "@/components/automation/Rollout";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { plainLink } from "@/components/ui/links";
import { useJobProgress } from "@/hooks/useJobProgress";
import { errorMessage, formatAgo } from "@/lib/format";
import { defaultTimeouts, SimHostRow, SystemUpdateForm, type SystemUpdateState } from "@/pages/jobs/OperationForms";
import { recheckState, updateHostWord, updateProgress } from "./updateProgress";

type Targets = { hosts?: string[]; groups?: string[] };
type SimHost = Schemas["SimulateResult"]["hosts"][number];

const modeWord: Record<SystemUpdateState["mode"], string> = { upgrade: "Upgrade", full_upgrade: "Full upgrade", security: "Security updates only" };
const rebootWord: Record<SystemUpdateState["reboot"], string> = { never: "no reboot", if_required: "reboot if required", always: "reboot afterwards" };
const changes = (h: SimHost) => (h.plan ? h.plan.upgrade.length + h.plan.install.length + h.plan.remove.length + h.plan.downgrade.length : 0);

/**
 * Install updates, from where pending updates are seen (DESIGN-STANDARD §1
 * Workflow first, §5 review before execution): one dialog is the review —
 * each host's package manager's dry run, what follows (reboots, removals),
 * the recommended rollout for several hosts — and its button starts the
 * ordinary system update run, whose progress and outcome then show here.
 * Mode, metadata refresh and reboot are options with DROOL's defaults; the
 * run composer (rollout, schedule, workflow) is one link away. The same
 * locks, checks, removal guard and audit apply as for any run.
 */
export function InstallUpdatesDialog({ targets, label, onClose }: {
  targets: Targets;
  /** What it runs on, in words: "media-02", "3 hosts", "Pack web-servers". */
  label: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [sys, setSys] = useState<SystemUpdateState>({ mode: "upgrade", refresh: true, reboot: "never", allowRemovals: false });
  const step = useMemo(() => ({ type: "system_update" as const, mode: sys.mode, refresh: sys.refresh, reboot: sys.reboot,
    allow_removals: sys.allowRemovals, timeout_s: defaultTimeouts.system_update }), [sys]);
  // Where it runs, as the engine resolves the targets (skips, warnings).
  // Asked with the plain rollout: the rollout is worked out here once the
  // number of hosts is known (a canary for one host would only be warned
  // about).
  const preview = useQuery({
    queryKey: ["jobs", "preview", targets, "install-updates"],
    queryFn: () => unwrap(api.POST("/jobs/preview", { body: { targets, strategy: strategyFrom(defaultRollout) } })),
  });
  const run = (preview.data?.hosts ?? []).filter((h) => !h.skip);
  const skipped = (preview.data?.hosts ?? []).filter((h) => h.skip);
  const strategy = strategyFrom(run.length >= 2 ? recommendedRollout : defaultRollout);
  // What each host's package manager would change: a dry run, nothing
  // changes. The removal permission doesn't change what it finds.
  const sim = useQuery({
    queryKey: ["jobs", "simulate", targets, sys.mode, "install-updates"],
    queryFn: () => unwrap(api.POST("/jobs/simulate", { body: { targets, step: { ...step, allow_removals: false } } })),
    retry: false,
    gcTime: 0,
  });
  const simHosts = sim.data?.hosts ?? [];
  const withRemovals = simHosts.filter((h) => h.extra_removals.length > 0);
  const changing = simHosts.filter((h) => changes(h) > 0);
  const packages = simHosts.reduce((n, h) => n + changes(h), 0);
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs", {
      params: { header: { "Idempotency-Key": crypto.randomUUID() } },
      body: { steps: [step], targets, strategy },
    })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: jk.jobs }),
  });
  const progress = useJobProgress(start.data?.id);
  // Once it's done, DROOL checks each updated host again by itself (the
  // engine's inventory recheck): the hosts are asked for until it has, so
  // the counts here and on the pages beneath are the run's outcome, and
  // until then this says they aren't. A slow host (after a reboot, its
  // checks run one after another) is asked for less often, not given up on.
  const [now, setNow] = useState(() => Date.now());
  const hostsQ = useQuery({
    queryKey: qk.hosts,
    queryFn: () => unwrap(api.GET("/hosts")),
    enabled: progress.done,
    refetchInterval: (q) => {
      const byId = new Map((q.state.data?.items ?? []).map((h) => [h.id, h]));
      const states = progress.hosts.filter((h) => h.state === "succeeded")
        .map((h) => recheckState(h.finished_at, byId.get(h.host_id)?.health?.updates_checked_at, Date.now()));
      return states.includes("checking") ? 3000 : states.includes("not_rechecked") ? 15000 : false;
    },
  });
  useEffect(() => {
    if (!progress.done) return;
    const t = setInterval(() => setNow(Date.now()), 5000);
    return () => clearInterval(t);
  }, [progress.done]);
  // Each host's own page reads it separately: fetched again with the list.
  useEffect(() => {
    if (!progress.done) return;
    for (const h of progress.hosts) void qc.invalidateQueries({ queryKey: qk.host(h.host_id), exact: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [progress.done, hostsQ.dataUpdatedAt]);
  const hostById = new Map((hostsQ.data?.items ?? []).map((h) => [h.id, h]));

  const composer = `/jobs/new?op=system_update&${targets.groups?.length ? `groups=${targets.groups.join(",")}` : `hosts=${(targets.hosts ?? []).join(",")}`}`;
  const action = `Install updates on ${label}`;
  const ready = !!preview.data && run.length > 0 && !sim.isLoading;
  const job = progress.job;
  const said = updateProgress(job?.status, progress.done, progress.hosts, label);
  return (
    <Dialog title={start.data ? said.title : `Install updates on ${label}?`} onClose={onClose} wide testId="install-updates"
      footer={start.data ? (
        <>
          <Link className={plainLink} to={`/jobs/${start.data.id}`}>View the run</Link>
          <Button data-autofocus variant={progress.done ? "primary" : "secondary"} onClick={onClose}>{progress.done ? "Done" : "Close"}</Button>
        </>
      ) : (
        <>
          <Button data-autofocus onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!ready || start.isPending}
            title={!preview.data ? "Working out the hosts…" : run.length === 0 ? "No host to run on" : sim.isLoading ? "Waiting for the dry run…" : undefined}
            onClick={() => start.mutate()}>{start.isPending ? "Starting…" : action}</Button>
        </>
      )}>
      {start.data ? (
        <div className="space-y-3 text-sm" data-testid="install-updates-progress">
          <p role="status" data-testid="install-updates-summary">
            {!job ? "Starting…" : said.summary}{job && !progress.done && " You can close this: the run continues, and it's under Runs."}
          </p>
          <ul className="divide-y divide-line rounded-ui border border-line">
            {progress.hosts.map((h) => (
              <li key={h.host_id} className="space-y-1 p-3" data-testid="install-updates-host">
                <div className="flex flex-wrap items-center gap-2"><span className="font-medium">{h.host_name}</span>
                  <Badge tone={updateHostWord[h.state].tone}>{updateHostWord[h.state].word}</Badge></div>
                {h.error_message && <p className="text-danger">{h.error_message}{h.error_hint && <span className="block text-xs text-muted">{h.error_hint}</span>}</p>}
                {progress.done && h.state === "succeeded" && <PendingAfter run={h} host={hostById.get(h.host_id)} now={now} upgradeOnly={sys.mode === "upgrade"} />}
              </li>
            ))}
          </ul>
          {progress.error && <Alert>{errorMessage(progress.error)}</Alert>}
        </div>
      ) : (
        <div className="space-y-4 text-sm" data-testid="install-updates-review">
          <p><strong>This is the review: nothing changes until you choose {action}.</strong>{" "}
            DROOL runs the system update with each host&apos;s package manager: {modeWord[sys.mode].toLowerCase()}, {sys.refresh ? "package lists refreshed first" : "without refreshing package lists"}, {rebootWord[sys.reboot]}.</p>
          {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
          {sim.isLoading && <p role="status">Asking each host&apos;s package manager what would change (a dry run: nothing changes)…</p>}
          {sim.error && <Alert tone="warn">The dry run didn&apos;t finish: {errorMessage(sim.error)}. You can still start the run: it checks each host again first.</Alert>}
          {sim.data && (
            <div className="space-y-2" data-testid="install-updates-dry-run">
              <p className="font-medium">
                {packages === 0 ? `Nothing to install on ${label} now.` : `${packages} package change${packages === 1 ? "" : "s"} on ${changing.length} of ${simHosts.length} host${simHosts.length === 1 ? "" : "s"}.`}
              </p>
              {sys.refresh && <p className="text-xs text-muted">From each host&apos;s current package lists: the run refreshes them first, so a few more updates may appear.</p>}
              {withRemovals.length > 0 && (
                <div className="rounded-ui border border-danger/40 bg-danger-bg p-3 text-danger">
                  <strong>{withRemovals.length === 1 ? `${withRemovals[0]!.host_name} would` : `${withRemovals.length} hosts would`} remove other packages.</strong>{" "}
                  Without your permission, the run stops there before changing anything.
                  <div className="mt-2 font-medium text-fg"><Checkbox checked={sys.allowRemovals} onChange={(v) => setSys({ ...sys, allowRemovals: v })} label="Allow these removals" /></div>
                </div>
              )}
              <ul className="divide-y divide-line rounded-ui border border-line">{simHosts.map((h) => <SimHostRow key={h.host_id} h={h} />)}</ul>
            </div>
          )}
          {(skipped.length > 0 || run.some((h) => h.warning) || (preview.data?.warnings ?? []).length > 0) && (
            <Alert tone="warn">
              {skipped.map((h) => <span key={h.id} className="block">{h.name}: left out{h.warning ? `: ${h.warning}` : ""}</span>)}
              {run.filter((h) => h.warning).map((h) => <span key={h.id} className="block">{h.name}: {h.warning}</span>)}
              {(preview.data?.warnings ?? []).map((w) => <span key={w} className="block">{w}</span>)}
            </Alert>
          )}
          <ul className="list-disc space-y-0.5 pl-5" data-testid="install-updates-consequences">
            <li>{sys.reboot === "always" ? "Every host reboots afterwards." : sys.reboot === "if_required" ? "Hosts that need it reboot afterwards; DROOL waits for each one to come back."
              : "No host reboots. Where an update needs one, the host says so afterwards (Reboot required)."}</li>
            {run.length >= 2 && <li data-testid="install-updates-rollout">Rollout (recommended): {rolloutText(strategy)}.</li>}
          </ul>
          <details className="rounded-ui border border-line p-3" data-testid="install-updates-options">
            <summary className="cursor-pointer font-medium">Options: {modeWord[sys.mode]}, {sys.refresh ? "refresh first" : "no refresh"}, {rebootWord[sys.reboot]}</summary>
            <div className="mt-3"><SystemUpdateForm v={sys} set={setSys} group="install-updates-mode" /></div>
          </details>
          <p className="text-xs text-muted">
            To change the rollout, schedule it or save it as a workflow, <Link className={plainLink} to={composer}>open it in the run composer</Link>.
          </p>
          {start.error && <Alert>{errorMessage(start.error)}</Alert>}
        </div>
      )}
    </Dialog>
  );
}

/** A host's pending updates after the run: the count only once DROOL has
 * checked the host again since it finished; until then, that it hasn't. */
function PendingAfter({ run, host, now, upgradeOnly }: { run: JobHost; host?: Host; now: number; upgradeOnly: boolean }) {
  const s = host?.health;
  const state = recheckState(run.finished_at, s?.updates_checked_at, now);
  if (state === "checking") return <p className="text-xs text-muted" data-testid="pending-after">Checking its pending updates again…</p>;
  if (state === "not_rechecked") {
    return <p className="text-xs text-muted" data-testid="pending-after">Its pending updates haven&apos;t been checked again yet: the count DROOL shows is from before the run
      {s?.updates_checked_at ? ` (checked ${formatAgo(s.updates_checked_at)})` : ""}.</p>;
  }
  const n = s?.updates_available;
  return (
    <p className="text-xs text-muted" data-testid="pending-after">
      {n == null ? "Pending updates: not known (no package cache reported)."
        : n === 0 ? `No pending updates left (checked ${formatAgo(s!.updates_checked_at)}).`
          : `${n} pending update${n === 1 ? "" : "s"} left${s?.security_updates ? `, ${s.security_updates} security` : ""} (checked ${formatAgo(s!.updates_checked_at)}).`
            + (upgradeOnly ? " Upgrade holds back updates that need new packages; Full upgrade (Options) installs them." : "")}
    </p>
  );
}
