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
import { useHostHealth } from "@/components/health/queries";
import { SubjectHealthDialog } from "@/components/health/HostHealth";
import { type MonService } from "./queries";

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
          <dt className="text-muted">Monitored</dt><dd>{unit.monitored ? `yes, expected ${unit.expected}${where}` : unit.expected === "ignored" ? `no, ignored${where}` : "no: its state is shown, it never alerts"}</dd>
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

type Expected = "running" | "stopped" | "ignored";
const expectedName: Record<Expected, string> = { running: "Running", stopped: "Stopped", ignored: "Ignored" };
const monitoring = (e?: string) => e === "running" || e === "stopped";

/** The small line under a service's Monitor box, only when it isn't the
 * plain case (ticked here, expected to run): where monitoring comes from,
 * an expectation of stopped, or an exception to a pack's setting. */
export function monitorNote(u: MonService): string {
  const src = u.expected_source;
  const own = !!src?.override_id;
  const inh = u.inherited_source ?? "";
  switch (u.expected) {
    case "running":
      if (!own) return src?.label ?? "";
      return u.inherited === "stopped" ? `expected running here (${inh}: stopped)` : "";
    case "stopped":
      return own ? (monitoring(u.inherited) ? `expected stopped here (${inh}: ${u.inherited})` : "expected stopped") : `expected stopped, ${src?.label}`;
    case "ignored":
      if (!own) return `ignored, ${src?.label}`;
      return monitoring(u.inherited) ? `not monitored here (${inh} monitors it)` : "ignored on this host";
  }
  return src ? src.label : "";
}

/** Whether the row shows "Customized": the host changes the unit's checks
 * (turned off, another duration), or its expected state set here is more
 * than ticking Monitor (stopped, ignored, or running over a pack's stopped). */
export function isCustomized(u: MonService): boolean {
  const own = !!u.expected_source?.override_id;
  return u.customized || (own && !(u.expected === "running" && u.inherited !== "stopped"));
}

/** Monitor: a service row's one control. Ticked means DROOL alerts on the
 * unit's state (expected running, or stopped). The server works out the
 * host's own setting: ticking expects it to run (or removes a host exception
 * when a pack or every host monitors it); unticking removes the host's
 * setting, or makes an exception when a pack or every host monitors it,
 * which only administrators may (it stops what they set up). */
export function MonitorToggle({ hostId, unit }: { hostId: string; unit: MonService }) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const toggle = useMutation({
    mutationFn: (on: boolean) => unwrap(api.PUT("/hosts/{id}/monitoring/services/{unit}/monitor", {
      params: { path: { id: hostId, unit: unit.name } }, body: { monitor: on },
    })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["monitoring"] });
      void qc.invalidateQueries({ queryKey: ["health"] });
      void qc.invalidateQueries({ queryKey: ["alerts"] });
    },
  });
  const exception = unit.monitored && monitoring(unit.inherited) && !can("monitoring.settings");
  const why = !can("monitoring.manage") ? "Changing what DROOL monitors needs more permissions."
    : exception ? `Monitored ${unit.inherited_source}: an administrator can make an exception for this host.` : undefined;
  const note = monitorNote(unit);
  return (
    <span className="flex flex-col gap-0.5">
      <label className="inline-flex items-center gap-2 text-sm" title={why}>
        <input type="checkbox" className="h-4 w-4 accent-[var(--ui-accent)]" checked={unit.monitored}
          disabled={!!why || toggle.isPending} aria-label={`Monitor ${unit.name}`}
          aria-describedby={note ? `mon-${unit.name}` : undefined} onChange={(e) => toggle.mutate(e.target.checked)} />
        <span className="sm:sr-only">Monitor</span>
      </label>
      {note && <span id={`mon-${unit.name}`} className="text-xs text-muted" data-testid="monitor-note">{note}</span>}
      {toggle.error && <span className="text-xs text-danger" role="alert">{errorMessage(toggle.error)}</span>}
    </span>
  );
}

/** Customize… for one service on one host: the expected state behind the
 * Monitor box (running, stopped, ignored, or what the host inherits), where
 * each comes from, and the timing of its checks. A change applies to this
 * host only; packs and every host are set on the pack's page and in
 * Monitoring → Health checks. */
export function ServiceMonitorDialog({ hostId, hostName, unit, onClose }: { hostId: string; hostName: string; unit: MonService; onClose: () => void }) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const own = !!unit.expected_source?.override_id;
  const [value, setValue] = useState<"" | Expected>(own ? (unit.expected as Expected) : "");
  const [timing, setTiming] = useState(false);
  const health = useHostHealth(hostId, undefined, can("monitoring.read"));
  const subject = (health.data?.subjects ?? []).find((x) => x.kind === "service" && x.ref === unit.name);
  const save = useMutation({
    mutationFn: (v: "" | Expected) => unwrap(api.PUT("/monitoring/health/overrides", {
      body: { scope: "host", host_id: hostId, check: "service_expected", subject: `unit:${unit.name}`, subject_label: unit.name, fields: v ? { expected: v } : {} },
    })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["monitoring"] });
      void qc.invalidateQueries({ queryKey: ["health"] });
      void qc.invalidateQueries({ queryKey: ["alerts"] });
      onClose();
    },
  });
  const inherited = unit.inherited
    ? `${expectedName[unit.inherited as Expected]}, ${unit.inherited_source}`
    : unit.inherited_source ? `not monitored: ${unit.inherited_source}` : "not monitored (nothing is set for a pack or every host)";
  const options: { v: "" | Expected; label: string; help: string; admin?: boolean }[] = [
    { v: "", label: own ? "Reset to inherited" : "Inherited (no setting on this host)", help: `What this host gets without its own setting: ${inherited}.` },
    { v: "running", label: "Monitor: expect it to run", help: "Stopped, failed or not installed is a health issue (failed is critical). The Monitor box does this." },
    { v: "stopped", label: "Monitor: expect it to be stopped", help: "Running is the health issue; stopped or failed is what you want." },
    { v: "ignored", label: "Ignore it on this host", help: "Its state stays visible, but it never alerts here, even where a pack or every host monitors it.", admin: true },
  ];
  const write = can("monitoring.manage");
  const current = own ? `${expectedName[unit.expected as Expected]}, set on this host` : unit.expected ? `${expectedName[unit.expected as Expected]}, ${unit.expected_source?.label}` : unit.expected_source ? `not monitored: ${unit.expected_source.label}` : "not monitored";
  return (
    <Dialog title={`Monitoring ${unit.name} on ${hostName}`} onClose={onClose}>
      <form className="space-y-4 text-sm" data-testid="service-monitor" onSubmit={(e) => { e.preventDefault(); save.mutate(value); }}>
        <p>Now: <b>{current}</b>.</p>
        <fieldset className="space-y-2">
          <legend className="mb-1 font-medium">What DROOL expects of it on this host</legend>
          {options.map((o) => {
            const blocked = !write || (o.admin && !can("monitoring.settings"));
            return (
              <label key={o.v || "inherit"} className={`flex items-start gap-2 rounded-ui border p-2 ${value === o.v ? "border-accent bg-accent/5" : "border-line"} ${blocked ? "opacity-60" : ""}`}>
                <input type="radio" name="expected" className="mt-1 accent-[var(--ui-accent)]" value={o.v} checked={value === o.v} disabled={blocked}
                  onChange={() => setValue(o.v)} />
                <span>
                  <span className="font-medium">{o.label}</span>{o.admin && blocked && write && <span className="text-muted"> (administrators)</span>}
                  <span className="block text-xs text-muted">{o.help}</span>
                </span>
              </label>
            );
          })}
        </fieldset>
        <p className="text-xs text-muted">
          A service no setting monitors is inventory only: DROOL shows its state, failed included, and never alerts on it. Starting or stopping it
          never changes this. For a pack&apos;s hosts, set it on the pack&apos;s page; for every host, in Monitoring → Health checks.
        </p>
        {subject ? (
          <p className="text-xs">
            <button type="button" className="text-accent underline" onClick={() => setTiming(true)}>How long before it alerts, or turning one of its checks off…</button>
            {unit.customized && <span className="text-muted"> (customized on this host)</span>}
          </p>
        ) : (
          <p className="text-xs text-muted">Once it&apos;s monitored, how long a problem lasts before it alerts can be changed here too.</p>
        )}
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>{write ? "Cancel" : "Close"}</Button>
          {write && <Button type="submit" variant="primary" disabled={save.isPending || value === (own ? unit.expected : "")}>{save.isPending ? "Saving…" : "Save"}</Button>}
        </div>
      </form>
      {timing && subject && <SubjectHealthDialog hostId={hostId} subject={subject} onClose={() => setTiming(false)} />}
    </Dialog>
  );
}
