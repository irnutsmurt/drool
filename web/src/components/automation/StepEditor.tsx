import { useState } from "react";
import type { Schemas } from "@/api/client";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import {
  PackageForm, RebootForm, ServiceForm, SystemUpdateForm, defaultTimeouts, opKinds, packageNames, serviceActions,
  type OpKind, type PackageState, type RebootState, type ServiceState, type SystemUpdateState,
} from "@/pages/jobs/OperationForms";
import { describeStep } from "@/pages/jobs/StepResult";
import { FileDeployForm, emptyFileDeploy, fileDeployStep, type FileDeployState } from "@/pages/files/FileDeployForm";
import type { Operation } from "./queries";

/** Steps a workflow can have: the job engine's typed operations, plus
 * checks DROOL runs itself (nothing runs on the host for them). */
export type StepKind = OpKind | "wait" | "verify_host" | "verify_services" | "verify_health";

export const checkKinds: { kind: StepKind; label: string; help: string }[] = [
  { kind: "wait", label: "Wait", help: "Pause before the next step, e.g. to let services settle." },
  { kind: "verify_host", label: "Verify host is reachable", help: "Passes when the host answers DROOL again (its agent is connected, or SSH works)." },
  { kind: "verify_services", label: "Verify services are running", help: "Passes when every listed systemd service is active, from freshly collected monitoring data." },
  { kind: "verify_health", label: "Verify health", help: "Passes when the host has no active Critical health issues (or no Warning ones either)." },
];

export const stepLabel = (k: StepKind) => opKinds.find((o) => o.kind === k)?.label ?? checkKinds.find((c) => c.kind === k)?.label ?? k;

export interface StepDraft {
  key: string;
  kind: StepKind;
  command: string;
  script: string;
  interpreter: "sh" | "bash";
  become: boolean;
  readOnly: boolean;
  pkg: PackageState;
  sys: SystemUpdateState;
  svc: ServiceState;
  rb: RebootState;
  fd: FileDeployState;
  seconds: string;
  services: string;
  policy: "no_critical" | "no_warning";
  timeout: string;
  when?: Schemas["When"];
  /** A step this editor shows but doesn't edit (per-OS scripts, a saved
   * file deployment): kept exactly as it is. */
  raw?: Operation;
}

let seq = 0;
const nextKey = () => `s${++seq}`;

export function newDraft(kind: StepKind): StepDraft {
  return {
    key: nextKey(), kind, command: "", script: "#!/bin/sh\nset -e\n\n", interpreter: "sh", become: false, readOnly: false,
    pkg: { action: "install", names: "", allowRemovals: false },
    sys: { mode: "upgrade", refresh: true, reboot: "never", allowRemovals: false },
    svc: { name: "", action: "restart" }, rb: { wait: true }, fd: { ...emptyFileDeploy },
    seconds: "60", services: "", policy: "no_critical", timeout: "",
  };
}

// 0 and absent both mean "the default" (templates and older specs send 0).
const str = (n?: number | null) => (n ? String(n) : "");

/** The editable form of a saved step. */
export function draftFromOp(op: Operation): StepDraft {
  const d = newDraft(op.type);
  if ("timeout_s" in op) d.timeout = str(op.timeout_s);
  if ("when" in op) d.when = op.when;
  switch (op.type) {
    case "command":
      return { ...d, command: op.command, become: !!op.become, readOnly: !!op.read_only };
    case "script":
      if (typeof op.body !== "string") return { ...d, raw: op };
      return { ...d, script: op.body, interpreter: op.interpreter ?? "sh", become: !!op.become, readOnly: !!op.read_only };
    case "package":
      return { ...d, pkg: { action: op.action, names: op.names.join(" "), allowRemovals: !!op.allow_removals } };
    case "system_update":
      return { ...d, sys: { mode: op.mode ?? "upgrade", refresh: op.refresh !== false, reboot: op.reboot ?? "never", allowRemovals: !!op.allow_removals } };
    case "service": {
      const a = serviceActions.find((x) => x.state === op.state && x.enabled === op.enabled) ?? serviceActions[2]!;
      return { ...d, svc: { name: op.name, action: a.value } };
    }
    case "reboot":
      return { ...d, rb: { wait: op.wait !== false } };
    case "file_deploy":
      return { ...d, raw: op };
    case "wait":
      return { ...d, seconds: str(op.seconds) };
    case "verify_host":
      return d;
    case "verify_services":
      return { ...d, services: (op.services ?? []).join(" ") };
    case "verify_health":
      return { ...d, policy: op.policy ?? "no_critical" };
  }
}

/** The step a draft describes. */
export function draftToOp(d: StepDraft): Operation {
  if (d.raw) return d.raw;
  const timeout_s = d.timeout ? Number(d.timeout) : undefined;
  const when = d.when;
  switch (d.kind) {
    case "command":
      return { type: "command", command: d.command, become: d.become, read_only: d.readOnly, timeout_s, when };
    case "script":
      return { type: "script", body: d.script, interpreter: d.interpreter, become: d.become, read_only: d.readOnly, timeout_s, when };
    case "package":
      return { type: "package", action: d.pkg.action, names: packageNames(d.pkg.names), allow_removals: d.pkg.allowRemovals, timeout_s, when };
    case "system_update":
      return { type: "system_update", mode: d.sys.mode, refresh: d.sys.refresh, reboot: d.sys.reboot, allow_removals: d.sys.allowRemovals, timeout_s, when };
    case "service": {
      const a = serviceActions.find((x) => x.value === d.svc.action)!;
      return { type: "service", name: d.svc.name.trim(), state: a.state, enabled: a.enabled, timeout_s, when };
    }
    case "reboot":
      return { type: "reboot", wait: d.rb.wait, timeout_s, when };
    case "file_deploy":
      return fileDeployStep(d.fd, timeout_s, when);
    case "wait":
      return { type: "wait", seconds: Number(d.seconds) || 0 };
    case "verify_host":
      return { type: "verify_host", timeout_s };
    case "verify_services":
      return { type: "verify_services", services: packageNames(d.services), timeout_s };
    case "verify_health":
      return { type: "verify_health", policy: d.policy, timeout_s };
  }
}

/** Why a draft can't be saved yet ("" when it can). */
export function draftProblem(d: StepDraft): string {
  if (d.raw) return "";
  switch (d.kind) {
    case "command":
      return d.command.trim() ? "" : "enter a command";
    case "script":
      return d.script.trim() ? "" : "enter a script";
    case "package":
      return packageNames(d.pkg.names).length ? "" : "name at least one package";
    case "service":
      return d.svc.name.trim() ? "" : "name the service";
    case "file_deploy":
      if (!d.fd.artifact) return "choose a file";
      if (!d.fd.path.trim().startsWith("/")) return "enter an absolute path";
      return !d.fd.serviceAction || d.fd.serviceName.trim() ? "" : "name the service to reload or restart";
    case "wait": {
      const n = Number(d.seconds);
      return Number.isInteger(n) && n >= 1 && n <= 3600 ? "" : "wait 1–3600 seconds";
    }
    case "verify_services":
      return packageNames(d.services).length ? "" : "name at least one service";
    default:
      return "";
  }
}

const checkTimeouts: Partial<Record<StepKind, number>> = { verify_host: 300, verify_services: 120, verify_health: 120 };

/** An ordered list of steps: add, edit, reorder, remove. No branches, no
 * conditions beyond what each step already supports. */
export function StepEditor({ steps, setSteps }: { steps: StepDraft[]; setSteps: (s: StepDraft[]) => void }) {
  const [add, setAdd] = useState<StepKind>("system_update");
  const [focusKey, setFocusKey] = useState<string>();
  const update = (key: string, patch: Partial<StepDraft>) => setSteps(steps.map((s) => (s.key === key ? { ...s, ...patch } : s)));
  const move = (i: number, by: number) => {
    const n = [...steps];
    const [s] = n.splice(i, 1);
    n.splice(i + by, 0, s!);
    setSteps(n);
  };
  return (
    <div className="space-y-3">
      {steps.length === 0 && <p className="text-sm text-muted">No steps yet. Add the first one below.</p>}
      <ol className="space-y-3">
        {steps.map((s, i) => (
          <li key={s.key} className="rounded-ui border border-line" aria-label={`Step ${i + 1}: ${stepLabel(s.kind)}`}>
            <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
              <span className="text-sm font-semibold">Step {i + 1}: {stepLabel(s.kind)}</span>
              {checkKinds.some((c) => c.kind === s.kind) && <span className="text-xs text-muted">checked by DROOL</span>}
              <span className="ml-auto flex gap-1">
                <Button type="button" size="sm" variant="ghost" disabled={i === 0} onClick={() => move(i, -1)} aria-label={`Move step ${i + 1} up`}>↑</Button>
                <Button type="button" size="sm" variant="ghost" disabled={i === steps.length - 1} onClick={() => move(i, 1)} aria-label={`Move step ${i + 1} down`}>↓</Button>
                <Button type="button" size="sm" variant="ghost" onClick={() => setSteps(steps.filter((x) => x.key !== s.key))} aria-label={`Remove step ${i + 1}`}>Remove</Button>
              </span>
            </div>
            <div className="p-3">
              <StepForm d={s} set={(p) => update(s.key, p)} autoFocus={focusKey === s.key} />
            </div>
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap items-end gap-2">
        <Field label="Add a step">
          <Select value={add} onChange={(e) => setAdd(e.target.value as StepKind)}>
            <optgroup label="On the host">
              {opKinds.map((o) => <option key={o.kind} value={o.kind}>{o.label}</option>)}
            </optgroup>
            <optgroup label="Checks (run by DROOL)">
              {checkKinds.map((c) => <option key={c.kind} value={c.kind}>{c.label}</option>)}
            </optgroup>
          </Select>
        </Field>
        <Button type="button" disabled={steps.length >= 10} onClick={() => { const d = newDraft(add); setFocusKey(d.key); setSteps([...steps, d]); }}>
          Add step
        </Button>
        {steps.length >= 10 && <span className="text-xs text-muted">At most 10 steps.</span>}
      </div>
    </div>
  );
}

function StepForm({ d, set, autoFocus }: { d: StepDraft; set: (p: Partial<StepDraft>) => void; autoFocus: boolean }) {
  if (d.raw) {
    return (
      <div className="space-y-1 text-sm">
        <p>{describeStep(d.raw) || (d.raw.type === "script" ? "A script with a variant per OS family" : d.raw.type)}</p>
        <p className="text-xs text-muted">Kept exactly as saved. To change it, remove it and add a new step.</p>
      </div>
    );
  }
  const timeout = (def: number, label = "Timeout (seconds)", hint?: string) => (
    <Field label={label} hint={hint ?? `Default ${def}.`}>
      <Input type="number" min={1} max={86400} className="max-w-[10rem]" value={d.timeout} onChange={(e) => set({ timeout: e.target.value })} />
    </Field>
  );
  const onlyOn = d.when && (d.when.os_family?.length || d.when.distro?.length)
    ? <p className="text-xs text-muted">Only on: {[...(d.when.os_family ?? []), ...(d.when.distro ?? [])].join(", ")}</p> : null;
  const asRoot = (
    <>
      <Checkbox checked={d.become} onChange={(become) => set({ become })} label="Run as root (sudo)" />
      <Checkbox checked={d.readOnly} onChange={(readOnly) => set({ readOnly })} label="Read-only"
        hint="Doesn't change the host, so it may run alongside other jobs on the same host." />
    </>
  );
  switch (d.kind) {
    case "command":
      return (
        <div className="space-y-3">
          <Field label="Command">
            <Input className="font-mono" value={d.command} autoFocus={autoFocus} placeholder="df -h" onChange={(e) => set({ command: e.target.value })} />
          </Field>
          {asRoot}{onlyOn}{timeout(defaultTimeouts.command)}
        </div>
      );
    case "script":
      return (
        <div className="space-y-3">
          <Field label="Script">
            <textarea className="h-40 w-full rounded-ui border border-line bg-surface p-3 font-mono text-xs" value={d.script}
              autoFocus={autoFocus} onChange={(e) => set({ script: e.target.value })} />
          </Field>
          <Field label="Interpreter">
            <Select className="max-w-[10rem]" value={d.interpreter} onChange={(e) => set({ interpreter: e.target.value as "sh" | "bash" })}>
              <option value="sh">sh</option>
              <option value="bash">bash</option>
            </Select>
          </Field>
          {asRoot}{onlyOn}{timeout(defaultTimeouts.script)}
        </div>
      );
    case "package":
      return <div className="space-y-3"><PackageForm v={d.pkg} set={(pkg) => set({ pkg })} autoFocus={autoFocus} />{onlyOn}{timeout(defaultTimeouts.package)}</div>;
    case "system_update":
      return <div className="space-y-3"><SystemUpdateForm v={d.sys} set={(sys) => set({ sys })} group={`mode-${d.key}`} />{onlyOn}{timeout(defaultTimeouts.system_update)}</div>;
    case "service":
      return <div className="space-y-3"><ServiceForm v={d.svc} set={(svc) => set({ svc })} autoFocus={autoFocus} />{onlyOn}{timeout(defaultTimeouts.service)}</div>;
    case "reboot":
      return <div className="space-y-3"><RebootForm v={d.rb} set={(rb) => set({ rb })} />{onlyOn}{timeout(defaultTimeouts.reboot)}</div>;
    case "file_deploy":
      return <div className="space-y-3"><FileDeployForm v={d.fd} set={(fd) => set({ fd })} />{onlyOn}{timeout(defaultTimeouts.file_deploy)}</div>;
    case "wait":
      return (
        <Field label="Seconds" hint="1–3600. Nothing runs on the host meanwhile; the host stays reserved for this job.">
          <Input type="number" min={1} max={3600} className="max-w-[10rem]" value={d.seconds} autoFocus={autoFocus} onChange={(e) => set({ seconds: e.target.value })} />
        </Field>
      );
    case "verify_host":
      return (
        <div className="space-y-3">
          <p className="text-sm text-muted">{checkKinds[1]!.help}</p>
          {timeout(checkTimeouts.verify_host!, "Keep trying for (seconds)", `Default ${checkTimeouts.verify_host}. The host fails this step if it doesn't answer in time.`)}
        </div>
      );
    case "verify_services":
      return (
        <div className="space-y-3">
          <Field label="Services" hint="systemd unit names separated by spaces; .service is added if omitted. Every one must be active.">
            <Input className="font-mono" value={d.services} autoFocus={autoFocus} placeholder="ssh jellyfin" onChange={(e) => set({ services: e.target.value })} />
          </Field>
          {timeout(checkTimeouts.verify_services!, "Keep trying for (seconds)", `Default ${checkTimeouts.verify_services}. A service still starting gets this long.`)}
        </div>
      );
    case "verify_health":
      return (
        <div className="space-y-3">
          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-sm font-medium">Passes when the host has</legend>
            {([["no_critical", "No active Critical health issues"], ["no_warning", "No active Warning or Critical health issues"]] as const).map(([v, label]) => (
              <label key={v} className="flex items-center gap-2 text-sm">
                <input type="radio" name={`policy-${d.key}`} checked={d.policy === v} onChange={() => set({ policy: v })} />
                {label}
              </label>
            ))}
          </fieldset>
          {timeout(checkTimeouts.verify_health!, "Keep trying for (seconds)", `Default ${checkTimeouts.verify_health}. Uses the same health rules as monitoring.`)}
        </div>
      );
  }
}
