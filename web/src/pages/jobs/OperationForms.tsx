import { useEffect, useMemo } from "react";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import type { SimSummary } from "@/components/jobs/RunConfirmDialog";
import { ChangeList } from "./StepResult";

export type OpKind = "command" | "script" | "package" | "system_update" | "service" | "reboot" | "file_deploy";

export const opKinds: { kind: OpKind; label: string }[] = [
  { kind: "command", label: "Command" },
  { kind: "script", label: "Script" },
  { kind: "package", label: "Packages" },
  { kind: "system_update", label: "System update" },
  { kind: "service", label: "Service" },
  { kind: "reboot", label: "Reboot" },
  { kind: "file_deploy", label: "Deploy file" },
];

export const defaultTimeouts: Record<OpKind, number> = {
  command: 300, script: 600, package: 1800, system_update: 3600, service: 120, reboot: 900, file_deploy: 600,
};

export interface PackageState { action: "install" | "remove" | "upgrade"; names: string; allowRemovals: boolean }
export interface SystemUpdateState {
  mode: "upgrade" | "full_upgrade" | "security";
  refresh: boolean;
  reboot: "never" | "if_required" | "always";
  allowRemovals: boolean;
}
export type ServiceAction = "start" | "stop" | "restart" | "reload" | "enable" | "disable" | "enable_start" | "disable_stop";
export interface ServiceState { name: string; action: ServiceAction }
export interface RebootState { wait: boolean }

export const serviceActions: { value: ServiceAction; label: string; state?: "running" | "stopped" | "restarted" | "reloaded"; enabled?: boolean }[] = [
  { value: "start", label: "Start", state: "running" },
  { value: "stop", label: "Stop", state: "stopped" },
  { value: "restart", label: "Restart", state: "restarted" },
  { value: "reload", label: "Reload configuration", state: "reloaded" },
  { value: "enable", label: "Enable at boot", enabled: true },
  { value: "disable", label: "Disable at boot", enabled: false },
  { value: "enable_start", label: "Enable at boot and start", state: "running", enabled: true },
  { value: "disable_stop", label: "Disable at boot and stop", state: "stopped", enabled: false },
];

export const packageNames = (s: string) => s.split(/[\s,]+/).filter(Boolean);

export function PackageForm({ v, set, autoFocus = true }: { v: PackageState; set: (v: PackageState) => void; autoFocus?: boolean }) {
  return (
    <div className="grid gap-4 sm:grid-cols-[12rem_1fr]">
      <Field label="Action">
        <Select value={v.action} onChange={(e) => set({ ...v, action: e.target.value as PackageState["action"], allowRemovals: false })}>
          <option value="install">Install</option>
          <option value="remove">Remove</option>
          <option value="upgrade">Upgrade (installed only)</option>
        </Select>
      </Field>
      <Field label="Packages" hint="Separate names with spaces. Already-installed (or already-removed) packages are left alone.">
        <Input className="font-mono" value={v.names} required autoFocus={autoFocus} placeholder="nginx curl"
          onChange={(e) => set({ ...v, names: e.target.value, allowRemovals: false })} />
      </Field>
    </div>
  );
}

const modeHelp: Record<SystemUpdateState["mode"], string> = {
  upgrade: "Upgrades installed packages without removing any (apt-get upgrade / dnf upgrade). Updates that need new or removed packages are held back on Debian/Ubuntu.",
  full_upgrade: "Also installs or removes packages when an upgrade needs it (apt-get full-upgrade; same as upgrade on dnf). If anything would be removed, the job stops unless you allow it.",
  security: "Only security updates (dnf upgrade --security). RHEL family and Fedora only; Debian/Ubuntu hosts fail with \"not supported\".",
};

/** group names the mode radios (several forms can share a page). */
export function SystemUpdateForm({ v, set, group = "mode" }: { v: SystemUpdateState; set: (v: SystemUpdateState) => void; group?: string }) {
  return (
    <div className="space-y-4">
      <fieldset className="space-y-2">
        <legend className="mb-1 text-sm font-medium">Mode</legend>
        {([["upgrade", "Upgrade"], ["full_upgrade", "Full upgrade"], ["security", "Security updates only"]] as const).map(([m, label]) => (
          <label key={m} className="flex items-start gap-2 text-sm">
            <input type="radio" name={group} className="mt-1" checked={v.mode === m} onChange={() => set({ ...v, mode: m, allowRemovals: false })} />
            <span>{label}<span className="block text-xs text-muted">{modeHelp[m]}</span></span>
          </label>
        ))}
      </fieldset>
      <Checkbox checked={v.refresh} onChange={(refresh) => set({ ...v, refresh })} label="Refresh package metadata first"
        hint="apt-get update / dnf makecache. Recommended." />
      <Field label="Reboot afterwards">
        <Select className="max-w-xs" value={v.reboot} onChange={(e) => set({ ...v, reboot: e.target.value as SystemUpdateState["reboot"] })}>
          <option value="never">Never</option>
          <option value="if_required">Only if the update requires it</option>
          <option value="always">Always</option>
        </Select>
      </Field>
      {v.reboot !== "never" && (
        <Alert tone="warn">
          Hosts {v.reboot === "always" ? "will reboot" : "that need it will reboot"} after updating and are unreachable until they come back.
          DROOL waits for each one to return with a new boot id.
        </Alert>
      )}
    </div>
  );
}

export function ServiceForm({ v, set, suggestions, autoFocus = true }: { v: ServiceState; set: (v: ServiceState) => void; suggestions?: string[]; autoFocus?: boolean }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Service" hint="systemd unit name; .service is added if omitted.">
        <Input className="font-mono" value={v.name} required autoFocus={autoFocus} placeholder="nginx" list={suggestions?.length ? "drool-services" : undefined}
          onChange={(e) => set({ ...v, name: e.target.value })} />
        {suggestions && suggestions.length > 0 && (
          <datalist id="drool-services">{suggestions.map((s) => <option key={s} value={s} />)}</datalist>
        )}
      </Field>
      <Field label="Action" hint="Start, stop, enable and disable do nothing if already in that state.">
        <Select value={v.action} onChange={(e) => set({ ...v, action: e.target.value as ServiceAction })}>
          {serviceActions.map((a) => <option key={a.value} value={a.value}>{a.label}</option>)}
        </Select>
      </Field>
    </div>
  );
}

export function RebootForm({ v, set }: { v: RebootState; set: (v: RebootState) => void }) {
  return (
    <div className="space-y-4">
      <Alert tone="warn">
        Reboots the selected hosts. Everything running on them stops, and they are unreachable until they come back.
      </Alert>
      <Checkbox checked={v.wait} onChange={(wait) => set({ ...v, wait })} label="Wait for each host to come back"
        hint="Succeeds only when the host answers with its trusted key and a new boot id. Within a job, the next batch of hosts starts after this." />
    </div>
  );
}

type SimHost = Schemas["SimulateResult"]["hosts"][number];

/**
 * Dry run of a package or system-update step. Nothing changes on the hosts.
 * Removals beyond the named packages must be allowed explicitly; the allow
 * box only appears once a preview has shown them.
 */
export function SimulatePanel({ targets, step, allow, setAllow, hasTargets, onResult }: {
  targets: Schemas["TargetSpec"];
  step: Schemas["Operation"];
  allow: boolean;
  setAllow: (v: boolean) => void;
  hasTargets: boolean;
  /** The dry run's outcome, for the run's review (null: none for this step). */
  onResult?: (r: SimSummary | null) => void;
}) {
  const sim = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs/simulate", { body: { targets, step } })),
  });
  // A preview is only valid for exactly this step and these targets.
  const key = useMemo(() => {
    const { allow_removals: _, ...rest } = step as Schemas["Operation"] & { allow_removals?: boolean };
    return JSON.stringify([targets, rest]);
  }, [targets, step]);
  const { reset } = sim;
  useEffect(() => {
    reset();
    setAllow(false);
  }, [key, reset, setAllow]);

  const hosts = sim.data?.hosts ?? [];
  const withRemovals = hosts.filter((h) => h.extra_removals.length > 0);
  useEffect(() => {
    if (!onResult) return;
    if (!sim.data) return onResult(null);
    const changes = (h: SimHost) => (h.plan ? h.plan.upgrade.length + h.plan.install.length + h.plan.remove.length + h.plan.downgrade.length : 0);
    onResult({
      hosts: hosts.length, changing: hosts.filter((h) => changes(h) > 0).length, packages: hosts.reduce((n, h) => n + changes(h), 0),
      removing: withRemovals.map((h) => h.host_name), failed: hosts.filter((h) => h.error).map((h) => h.host_name), removalsAllowed: allow,
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sim.data, allow]);
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <Button type="button" size="sm" onClick={() => sim.mutate()} disabled={!hasTargets || sim.isPending}>
          {sim.isPending ? "Checking hosts…" : sim.data ? "Preview again" : "Preview changes"}
        </Button>
        <span className="text-xs text-muted">Dry run on each host. Nothing is changed.</span>
      </div>
      {sim.error && <Alert>{errorMessage(sim.error)}</Alert>}
      {sim.data && (
        <>
          {step.type === "system_update" && step.refresh !== false && (
            <p className="text-xs text-muted">
              Based on each host's current package metadata. The job refreshes it first, so a few more updates may appear;
              new removals would still stop the job unless allowed.
            </p>
          )}
          {withRemovals.length > 0 && (
            <div className="rounded-ui border border-danger/40 bg-danger-bg p-3 text-sm text-danger">
              <strong>{withRemovals.length} host{withRemovals.length === 1 ? "" : "s"} would remove other packages.</strong>{" "}
              Without permission, the job stops on those hosts before changing anything.
              <div className="mt-2 font-medium text-fg">
                <Checkbox checked={allow} onChange={setAllow} label="Allow these removals" />
              </div>
            </div>
          )}
          <ul className="divide-y divide-line rounded-ui border border-line">
            {hosts.map((h) => <SimHostRow key={h.host_id} h={h} />)}
          </ul>
        </>
      )}
      {!sim.data && allow && (
        <Checkbox checked={allow} onChange={setAllow} label="Allow removing other packages" />
      )}
    </div>
  );
}

export function SimHostRow({ h }: { h: SimHost }) {
  const p = h.plan;
  const counts = p && [
    p.upgrade.length && `${p.upgrade.length} to upgrade`,
    p.install.length && `${p.install.length} new`,
    p.remove.length && `${p.remove.length} to remove`,
    p.downgrade.length && `${p.downgrade.length} to downgrade`,
  ].filter(Boolean).join(", ");
  return (
    <li className="space-y-1 p-3 text-sm">
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="font-medium">{h.host_name}</span>
        {h.package_manager && <span className="text-xs text-muted">{h.package_manager}</span>}
        <span className={h.error ? "text-danger" : h.skipped ? "text-muted" : undefined}>
          {h.skipped ? `skipped: ${h.skipped}` : h.error ? h.error.message : counts || "nothing to do"}
        </span>
      </div>
      {h.error?.hint && <p className="text-xs text-muted">{h.error.hint}</p>}
      {p && p.remove.length > 0 && (
        <div className="text-danger">
          <span className="text-xs font-medium uppercase">Removes</span>
          <ChangeList items={p.remove} highlight={new Set(h.extra_removals.map((c) => c.name))} />
        </div>
      )}
      {p && p.upgrade.length + p.install.length + p.downgrade.length > 0 && (
        <details>
          <summary className="cursor-pointer text-xs text-muted">Show packages</summary>
          {p.install.length > 0 && <><div className="mt-1 text-xs font-medium uppercase text-muted">Installs</div><ChangeList items={p.install} /></>}
          {p.upgrade.length > 0 && <><div className="mt-1 text-xs font-medium uppercase text-muted">Upgrades</div><ChangeList items={p.upgrade} /></>}
          {p.downgrade.length > 0 && <><div className="mt-1 text-xs font-medium uppercase text-muted">Downgrades</div><ChangeList items={p.downgrade} /></>}
        </details>
      )}
    </li>
  );
}
