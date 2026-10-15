import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { monKeys, type MonService } from "./queries";

type Action = MonService["actions"][number];

/** What each action asks of the typed service operation, and says. */
const actions: Record<Action, { label: string; state?: "running" | "stopped" | "restarted"; enabled?: boolean; what: string; doing: string }> = {
  start: { label: "Start", state: "running", what: "Starts it now. Whether it starts at boot stays as it is.", doing: "Starting" },
  stop: { label: "Stop", state: "stopped", what: "Stops it now. Whether it starts at boot stays as it is.", doing: "Stopping" },
  restart: { label: "Restart", state: "restarted", what: "Stops and starts it again.", doing: "Restarting" },
  enable: { label: "Enable", enabled: true, what: "Starts it at boot from now on. It isn't started now.", doing: "Enabling" },
  disable: { label: "Disable", enabled: false, what: "Doesn't start it at boot any more. It keeps running now.", doing: "Disabling" },
  enable_start: { label: "Enable & Start", state: "running", enabled: true, what: "Starts it now and at every boot.", doing: "Enabling and starting" },
  disable_stop: { label: "Disable & Stop", state: "stopped", enabled: false, what: "Stops it now and doesn't start it at boot any more.", doing: "Disabling and stopping" },
};

const order: Action[] = ["start", "stop", "restart", "enable", "disable", "enable_start", "disable_stop"];
const activeJob = (s?: string) => s === "pending" || s === "running" || s === "paused";

/** Does the action leave the unit off (now, or from the next boot)? */
const stops = (a: Action) => a === "stop" || a === "disable_stop";
const starts = (a: Action) => a === "start" || a === "enable_start" || a === "restart";

/** Running a service action from a host's Services view: the actions that
 * make sense for the unit as it is (the server says which), what each does,
 * a warning when it goes against the unit's expected state (which DROOL
 * never changes because a unit was stopped or started), the run itself, and
 * the state DROOL verified on the host afterwards. */
export function ServiceActionsDialog({ hostId, hostName, unit, onClose }: { hostId: string; hostName: string; unit: MonService; onClose: () => void }) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const [picked, setPicked] = useState<Action | null>(null);
  const [jobId, setJobId] = useState<string>();
  const offered = order.filter((a) => unit.actions.includes(a));
  const run = useMutation({
    mutationFn: (a: Action) => {
      const x = actions[a];
      return unwrap(api.POST("/jobs", {
        body: {
          name: `${x.label} ${unit.name} on ${hostName}`,
          steps: [{ type: "service", name: unit.name, ...(x.state ? { state: x.state } : {}), ...(x.enabled !== undefined ? { enabled: x.enabled } : {}) }],
          targets: { hosts: [hostId] },
        },
      }));
    },
    onSuccess: (j) => setJobId(j.id),
  });
  const job = useQuery({
    queryKey: jk.job(jobId ?? "none"),
    queryFn: () => unwrap(api.GET("/jobs/{id}", { params: { path: { id: jobId! } } })),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && !activeJob(q.state.data.status) ? false : 1000),
  });
  const done = !!job.data && !activeJob(job.data.status);
  const detail = useQuery({
    queryKey: jk.jobHost(jobId ?? "none", hostId),
    queryFn: () => unwrap(api.GET("/jobs/{id}/hosts/{hostId}", { params: { path: { id: jobId!, hostId } } })),
    enabled: done,
  });
  // The visible state: ask the host again right away, and refresh the list.
  useEffect(() => {
    if (!done) return;
    const refresh = can("monitoring.manage") ? unwrap(api.POST("/hosts/{id}/monitoring/refresh", { params: { path: { id: hostId } } })).catch(() => undefined) : Promise.resolve();
    void refresh.then(() => {
      void qc.invalidateQueries({ queryKey: ["monitoring", "services", hostId] });
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["monitoring", "services", hostId] }), 2000);
    });
  }, [done, hostId, qc, can]);
  const expectMut = useMutation({
    mutationFn: (expected: "running" | "stopped") => unwrap(api.PUT("/monitoring/health/overrides", {
      body: { scope: "host", host_id: hostId, check: "service_expected", subject: `unit:${unit.name}`, subject_label: unit.name, fields: { expected } },
    })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["monitoring"] }),
  });

  const where = unit.expected_source?.label ? ` (${unit.expected_source.label})` : "";
  const warning = (a: Action) => {
    if (unit.expected === "running" && stops(a)) {
      return `${unit.name} is expected to run${where}. Stopping it raises a health issue, and an alert, until it runs again or you change its expected state. DROOL doesn't change that for you.`;
    }
    if (unit.expected === "running" && a === "disable") {
      return `${unit.name} is expected to run${where}. Disabled, it won't start at the next boot, which raises a health issue then.`;
    }
    if (unit.expected === "stopped" && starts(a)) {
      return `${unit.name} is expected to be stopped${where}. Running, it raises a health issue ("running unexpectedly"), and an alert, until it stops or you change its expected state. DROOL doesn't change that for you.`;
    }
    return "";
  };

  const title = `${unit.name} on ${hostName}`;
  if (jobId) {
    const d = detail.data;
    const result = d?.steps?.[0]?.result as { after?: { active?: string; sub?: string; unit_file?: string } } | undefined;
    const after = result?.after;
    const a = picked!;
    const leftOff = stops(a) && unit.expected === "running";
    const leftOn = starts(a) && unit.expected === "stopped";
    return (
      <Dialog title={title} onClose={onClose}>
        <div className="space-y-4 text-sm" data-testid="service-action">
          {!done || !d ? (
            <p>{actions[a].doing} {unit.name}, then checking on the host that it worked…</p>
          ) : d.state === "succeeded" ? (
            <Alert tone="ok">
              {actions[a].label}: done. DROOL checked on {hostName}: {unit.name} is now {after ? `${after.active} (${after.sub}), ${after.unit_file || "no unit-file state"} at boot` : "as requested"}.
            </Alert>
          ) : d.state === "failed" ? (
            <Alert>{d.error_message || `${actions[a].label} failed.`}{d.error_hint && <> {d.error_hint}</>}</Alert>
          ) : (
            <Alert tone="warn">The run ended as “{d.state}”: DROOL can't say for sure what state {unit.name} is in. The run's page has the details.</Alert>
          )}
          {done && d?.state === "succeeded" && (leftOff || leftOn) && (
            <div className="rounded-ui border border-line p-3" data-testid="expect-followup">
              <p className="mb-2">{unit.name} is still expected to {leftOff ? "run" : "be stopped"}{where}, so it will raise a health issue. If it's meant to stay {leftOff ? "off" : "on"}, change its expected state:</p>
              {expectMut.isSuccess ? <p className="text-ok">Expected state changed to {leftOff ? "Stopped" : "Running"} on this host.</p> : (
                <Button size="sm" disabled={expectMut.isPending || !can("monitoring.manage")} onClick={() => expectMut.mutate(leftOff ? "stopped" : "running")}>
                  Mark it expected {leftOff ? "stopped" : "running"} on this host
                </Button>
              )}
              {expectMut.error && <div className="mt-2"><Alert>{errorMessage(expectMut.error)}</Alert></div>}
            </div>
          )}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Link className={plainLink} to={`/jobs/${jobId}`}>View the run</Link>
            <Button variant="primary" onClick={onClose} data-autofocus>{done ? "Done" : "Close"}</Button>
          </div>
        </div>
      </Dialog>
    );
  }

  if (picked) {
    const x = actions[picked];
    const w = warning(picked);
    return (
      <Dialog title={`${x.label} ${title}?`} onClose={onClose}>
        <div className="space-y-4 text-sm" data-testid="service-action">
          <p>{x.what} Afterwards DROOL asks the host again and reports what it finds, not what systemctl answered.</p>
          {w && <Alert tone="warn">{w}</Alert>}
          {run.error && <Alert>{errorMessage(run.error)}</Alert>}
          <div className="flex justify-end gap-2">
            <Button onClick={() => { setPicked(null); run.reset(); }}>Back</Button>
            <Button variant={stops(picked) || picked === "disable" ? "danger" : "primary"} disabled={run.isPending} onClick={() => run.mutate(picked)} data-autofocus>
              {run.isPending ? "Starting…" : x.label}
            </Button>
          </div>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog title={title} onClose={onClose}>
      <div className="space-y-4 text-sm" data-testid="service-action">
        <dl className="grid grid-cols-[minmax(6rem,8rem)_1fr] gap-y-1">
          <dt className="text-muted">Now</dt><dd>{unit.active}{unit.sub ? ` (${unit.sub})` : ""}</dd>
          <dt className="text-muted">At boot</dt><dd>{unit.file_state || "—"}</dd>
          <dt className="text-muted">Expected</dt><dd>{unit.expected ? `${unit.expected}${where}` : "automatic (failed counts, stopped is fine)"}</dd>
        </dl>
        {offered.length === 0 ? <p className="text-muted">No action applies to it as it is.</p> : (
          <ul className="space-y-2" aria-label="Actions">
            {offered.map((a) => (
              <li key={a}>
                <Button className="w-full justify-start" onClick={() => setPicked(a)} aria-label={`${actions[a].label} ${unit.name}`}>
                  <span className="font-medium">{actions[a].label}</span>
                  <span className="ml-2 text-left text-xs text-muted">{actions[a].what}</span>
                </Button>
              </li>
            ))}
          </ul>
        )}
        {unit.action_note && <p className="text-xs text-muted" data-testid="action-note">{unit.action_note}</p>}
        <div className="flex justify-end"><Button onClick={onClose}>Close</Button></div>
      </div>
    </Dialog>
  );
}

/** The expected-state choice of one unit on one host: what applies, where it
 * comes from, and (with the permission) a change on this host only. Ignored
 * turns its checks off, so it needs monitoring.settings. */
export function ExpectedStateSelect({ hostId, unit }: { hostId: string; unit: MonService }) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const own = unit.expected_source?.override_id;
  const save = useMutation({
    mutationFn: (v: "" | "running" | "stopped" | "ignored") => unwrap(api.PUT("/monitoring/health/overrides", {
      body: { scope: "host", host_id: hostId, check: "service_expected", subject: `unit:${unit.name}`, subject_label: unit.name, fields: v ? { expected: v } : {} },
    })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: monKeys.services(hostId, "", false).slice(0, 3) }),
  });
  const value = own ? unit.expected : "";
  const names: Record<string, string> = { running: "Running", stopped: "Stopped", ignored: "Ignored" };
  const inherited = unit.expected ? `${names[unit.expected]} (${unit.expected_source?.label})` : "Automatic";
  return (
    <span className="flex flex-col gap-0.5">
      <select aria-label={`Expected state of ${unit.name}`} className="h-8 rounded-ui border border-line bg-surface px-2 text-xs"
        value={value} disabled={!can("monitoring.manage") || save.isPending}
        onChange={(e) => save.mutate(e.target.value as "" | "running" | "stopped" | "ignored")}>
        <option value="">{own ? "Use the inherited setting" : inherited}</option>
        <option value="running">Running</option>
        <option value="stopped">Stopped</option>
        <option value="ignored" disabled={!can("monitoring.settings")}>Ignored{can("monitoring.settings") ? "" : " (administrators)"}</option>
      </select>
      {save.error && <span className="text-xs text-danger">{errorMessage(save.error)}</span>}
    </span>
  );
}
