import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { SimHostRow } from "@/pages/jobs/OperationForms";
import { modeInfo } from "./parts";

const step = { type: "package" as const, action: "install" as const, names: ["nftables"] };
const activeJob = (s?: string) => s === "pending" || s === "running" || s === "paused";

/** "Install nftables…" beside a firewall environment check that found it
 * missing (DESIGN-STANDARD §1, Workflow first: DROOL does what it already
 * knows how to do safely). The ordinary package run does it — a dry run
 * first, then the same locks, checks and audit as any install — once
 * someone confirms; its progress and outcome show here, and the environment
 * is checked again after it succeeds (onInstalled). It never changes the
 * mode or enforces anything, and Check itself stays read-only. Where it
 * can't install, it says why instead of offering the action. */
export function InstallNftables({ hostId, hostName, mode, jobId, onStarted, onInstalled, checking }: {
  hostId: string; hostName: string; mode: string;
  /** The run started from here, kept by the caller so its outcome stays in
   * sight after the check no longer reports nftables missing. */
  jobId?: string;
  onStarted: (jobId: string) => void;
  onInstalled: () => void;
  checking: boolean;
}) {
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  const job = useQuery({
    queryKey: jk.job(jobId ?? "none"),
    queryFn: () => unwrap(api.GET("/jobs/{id}", { params: { path: { id: jobId! } } })),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && !activeJob(q.state.data.status) ? false : 1000),
  });
  const done = !!job.data && !activeJob(job.data.status);
  const result = useQuery({
    queryKey: jk.jobHost(jobId ?? "none", hostId),
    queryFn: () => unwrap(api.GET("/jobs/{id}/hosts/{hostId}", { params: { path: { id: jobId!, hostId } } })),
    enabled: done,
  });
  const state = result.data?.state;
  const rechecked = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (state === "succeeded" && jobId && rechecked.current !== jobId) {
      rechecked.current = jobId;
      onInstalled();
    }
  }, [state, jobId, onInstalled]);

  if (!can("jobs.execute") || !can("firewall.write")) {
    return <p className="mt-1 text-xs" data-testid="nft-install-unavailable">Installing it from here needs permission to run jobs and to manage the firewall: ask an administrator.</p>;
  }
  const run = jobId && <Link className={plainLink} to={`/jobs/${jobId}`}>View the run</Link>;
  return (
    <div className="mt-2 space-y-1" data-testid="nft-install">
      {jobId && !done && <p role="status">Installing nftables on {hostName}… {run}</p>}
      {done && state === "succeeded" && (
        <p role="status" className="text-ok">nftables is installed. {checking ? "Checking the firewall environment again…" : "The environment was checked again."} {run}</p>
      )}
      {done && state && state !== "succeeded" && (
        <Alert>{state === "failed" ? `Installing nftables failed: ${result.data?.error_message || "see the run"}` : `The installation ended as “${state}”.`}
          {result.data?.error_hint && <> {result.data.error_hint}</>} {run}</Alert>
      )}
      {(!jobId || (done && state && state !== "succeeded")) && (
        <Button size="sm" onClick={() => setOpen(true)}>Install nftables…</Button>
      )}
      {open && <InstallNftablesDialog hostId={hostId} hostName={hostName} mode={mode} onClose={() => setOpen(false)}
        onStarted={(id) => { setOpen(false); onStarted(id); }} />}
    </div>
  );
}

/** What installing nftables involves on this host (the package manager's own
 * dry run), and the confirmation. */
function InstallNftablesDialog({ hostId, hostName, mode, onClose, onStarted }: {
  hostId: string; hostName: string; mode: string; onClose: () => void; onStarted: (jobId: string) => void;
}) {
  const sim = useQuery({
    queryKey: ["firewall", "nft-install-preview", hostId],
    queryFn: () => unwrap(api.POST("/jobs/simulate", { body: { targets: { hosts: [hostId] }, step } })),
    retry: false,
    gcTime: 0,
  });
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs", {
      body: { name: `Install nftables on ${hostName} (firewall prerequisite)`, steps: [step], targets: { hosts: [hostId] } },
    })),
    onSuccess: (j) => onStarted(j.id),
  });
  const h = sim.data?.hosts[0];
  const p = h?.plan;
  // Why it can't be installed from here, if it can't.
  const blocked = sim.error ? `DROOL couldn't work out what installing it involves: ${errorMessage(sim.error)}`
    : !h ? ""
      : h.skipped ? `DROOL can't install it on ${hostName}: ${h.skipped}`
        : h.error ? `DROOL can't install it on ${hostName}: ${h.error.message}${h.error.hint ? ` ${h.error.hint}` : ""}`
          : h.extra_removals.length > 0 ? `Installing nftables on ${hostName} would also remove ${h.extra_removals.map((c) => c.name).join(", ")}. DROOL doesn't do that from here: review it as a package run under Runs.`
            : p && p.install.length + p.upgrade.length === 0 ? `nftables is already installed on ${hostName}: choose Check to update the result.`
              : "";
  return (
    <Dialog title={`Install nftables on ${hostName}?`} onClose={onClose}>
      <div className="space-y-3 text-sm" data-testid="nft-install-dialog">
        {sim.isLoading && <p>Asking {hostName}&apos;s package manager what installing nftables involves (a dry run: nothing changes)…</p>}
        {blocked && <Alert tone="warn">{blocked}</Alert>}
        {h && !blocked && (
          <>
            <p>DROOL installs the <strong>nftables</strong> package with {hostName}&apos;s package manager{h.package_manager ? ` (${h.package_manager})` : ""},
              from the host&apos;s configured repositories. Its dry run:</p>
            <ul className="rounded-ui border border-line"><SimHostRow h={h} showPackages /></ul>
            <p className="text-xs text-muted">
              A normal package run: the same locks, checks and audit as any install. Nothing else changes: the firewall mode stays
              {" "}{modeInfo[mode]?.label ?? mode}, nothing is enforced, and DROOL checks the firewall environment again when it&apos;s done.
            </p>
          </>
        )}
        {start.error && <Alert>{errorMessage(start.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{blocked ? "Close" : "Cancel"}</Button>
          {h && !blocked && (
            <Button variant="primary" disabled={start.isPending} onClick={() => start.mutate()}>{start.isPending ? "Starting…" : "Install nftables"}</Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}
