import { useCallback, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { jk, useGroups, useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { RolloutAdvice, RolloutFields, WavesList, defaultRollout, mutates, recommendedRollout, rolloutText, strategyFrom, type RolloutState } from "@/components/automation/Rollout";
import { RunConfirmDialog, type SimSummary } from "@/components/jobs/RunConfirmDialog";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { errorMessage } from "@/lib/format";
import {
  PackageForm, RebootForm, ServiceForm, SimulatePanel, SystemUpdateForm, defaultTimeouts, opKinds, packageNames, serviceActions,
  type OpKind, type PackageState, type RebootState, type ServiceAction, type ServiceState, type SystemUpdateState,
} from "./OperationForms";
import { DeployPreviewPanel, FileDeployForm, emptyFileDeploy, fileDeployStep, type DeploySummary, type FileDeployState } from "../files/FileDeployForm";

type TargetSpec = Schemas["TargetSpec"];

const isKind = (k: string | null): k is OpKind => opKinds.some((o) => o.kind === k);

export function NewJobPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const hosts = useHosts();
  const groups = useGroups();

  const [kind, setKind] = useState<OpKind>(() => { const op = params.get("op"); return isKind(op) ? op : "command"; });
  const [command, setCommand] = useState("");
  const [script, setScript] = useState("#!/bin/sh\nset -e\n\n");
  const [interpreter, setInterpreter] = useState<"sh" | "bash">("sh");
  const [become, setBecome] = useState(false);
  const [readOnly, setReadOnly] = useState(false);
  const [timeout, setTimeoutS] = useState("");
  const [name, setName] = useState("");
  const [hostIds, setHostIds] = useState<string[]>(() => params.get("hosts")?.split(",").filter(Boolean) ?? []);
  const [groupIds, setGroupIds] = useState<string[]>(() => params.get("groups")?.split(",").filter(Boolean) ?? []);
  const [tags, setTags] = useState("");
  // The rollout: the recommended one for a change to several hosts unless
  // the user chose another (then theirs is kept, never overridden).
  const [rollout, setRolloutState] = useState<RolloutState>(defaultRollout);
  const [rolloutTouched, setRolloutTouched] = useState(false);
  const [hostCount, setHostCount] = useState(0);
  const [advanced, setAdvanced] = useState(false);
  const [editRollout, setEditRollout] = useState(false);
  const [reviewing, setReviewing] = useState(false);
  const [sim, setSim] = useState<SimSummary | null>(null);
  const [deploy, setDeploy] = useState<DeploySummary | null>(null);
  const { can } = useAuth();
  const [filter, setFilter] = useState("");
  const [onlyOn, setOnlyOn] = useState<("debian" | "redhat")[]>([]);
  const [perOS, setPerOS] = useState(false);
  const [pkg, setPkg] = useState<PackageState>({ action: "install", names: params.get("packages") ?? "", allowRemovals: false });
  const [sys, setSys] = useState<SystemUpdateState>({ mode: "upgrade", refresh: true, reboot: "never", allowRemovals: false });
  const [svc, setSvc] = useState<ServiceState>(() => ({
    name: params.get("service") ?? "",
    action: serviceActions.some((a) => a.value === params.get("action")) ? params.get("action") as ServiceAction : "restart",
  }));
  const [rb, setRb] = useState<RebootState>({ wait: true });
  const [fd, setFd] = useState<FileDeployState>(() => ({ ...emptyFileDeploy, path: params.get("path") ?? "" }));
  const setPkgAllow = useCallback((allowRemovals: boolean) => setPkg((v) => ({ ...v, allowRemovals })), []);
  const setSysAllow = useCallback((allowRemovals: boolean) => setSys((v) => ({ ...v, allowRemovals })), []);
  const [variants, setVariants] = useState<Record<"debian" | "redhat" | "default", string>>({ debian: "", redhat: "", default: "" });

  const targets: TargetSpec = useMemo(() => ({
    hosts: hostIds, groups: groupIds, tags: tags.split(/[,\s]+/).filter(Boolean),
  }), [hostIds, groupIds, tags]);
  const hasTargets = hostIds.length + groupIds.length + (targets.tags?.length ?? 0) > 0;

  const step = useMemo((): Schemas["Operation"] => {
    const timeout_s = timeout ? Number(timeout) : undefined;
    const when = onlyOn.length ? { os_family: onlyOn } : undefined;
    switch (kind) {
      case "command":
        return { type: "command", command, become, read_only: readOnly, timeout_s, when };
      case "package":
        return { type: "package", action: pkg.action, names: packageNames(pkg.names), allow_removals: pkg.allowRemovals, timeout_s, when };
      case "system_update":
        return { type: "system_update", mode: sys.mode, refresh: sys.refresh, reboot: sys.reboot, allow_removals: sys.allowRemovals, timeout_s, when };
      case "service": {
        const a = serviceActions.find((x) => x.value === svc.action)!;
        return { type: "service", name: svc.name.trim(), state: a.state, enabled: a.enabled, timeout_s, when };
      }
      case "reboot":
        return { type: "reboot", wait: rb.wait, timeout_s, when };
      case "file_deploy":
        return fileDeployStep(fd, timeout_s, when);
    }
    const body = perOS
      ? Object.fromEntries(Object.entries(variants).filter(([, v]) => v.trim() !== ""))
      : script;
    return { type: "script", body, interpreter, become, read_only: readOnly, timeout_s, when };
  }, [kind, command, script, interpreter, become, readOnly, timeout, onlyOn, perOS, variants, pkg, sys, svc, rb, fd]);
  const conditional = onlyOn.length > 0 || (kind === "script" && perOS);
  const autoRollout = !rolloutTouched && hostCount >= 2 && mutates([step]);
  const effective = rolloutTouched ? rollout : autoRollout ? recommendedRollout : defaultRollout;
  const strategy = useMemo(() => strategyFrom(effective), [effective]);
  const setRollout = (v: RolloutState) => {
    setRolloutState(v);
    setRolloutTouched(true);
  };
  // What a schedule or workflow made from this run starts with: the rollout
  // chosen or recommended here; otherwise none, so it recommends its own.
  const carried = rolloutTouched || autoRollout ? strategy : undefined;
  const stepReady = {
    command: command.trim() !== "",
    script: perOS ? Object.values(variants).some((v) => v.trim()) : script.trim() !== "",
    package: packageNames(pkg.names).length > 0,
    system_update: true,
    service: svc.name.trim() !== "",
    reboot: true,
    file_deploy: !!fd.artifact && fd.path.trim().startsWith("/") && (!fd.serviceAction || fd.serviceName.trim() !== ""),
  }[kind];

  // The step goes with the preview when it decides where it runs (OS
  // conditions) or could contradict desired state (warnings, M14).
  const sendStep = stepReady && (conditional || kind === "service" || kind === "package" || kind === "file_deploy");
  const preview = useQuery({
    queryKey: ["jobs", "preview", targets, sendStep ? step : null, strategy],
    queryFn: () => unwrap(api.POST("/jobs/preview", {
      body: { targets, steps: sendStep ? [step] : undefined, strategy },
    })),
    enabled: hasTargets,
    placeholderData: (prev) => prev,
  });

  // Idempotency key per form instance: a double submit can't create two jobs.
  const [idemKey] = useState(() => crypto.randomUUID());
  const submit = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs", {
      params: { header: { "Idempotency-Key": idemKey } },
      body: {
        name: name || undefined, steps: [step], targets, strategy,
      },
    })),
    onSuccess: (j) => {
      void qc.invalidateQueries({ queryKey: jk.jobs });
      navigate(`/jobs/${j.id}`);
    },
  });

  const count = preview.data?.hosts.filter((h) => !h.skip).length ?? 0;
  const skipCount = preview.data?.hosts.filter((h) => h.skip).length ?? 0;
  if (count !== hostCount) setHostCount(count);
  /** What happens beyond the step itself, in plain words, for the review. */
  const consequences = (): string[] => {
    const them = count === 1 ? "it" : "them";
    switch (kind) {
      case "reboot":
        return [`Everything running on ${them} stops until ${count === 1 ? "it comes" : "they come"} back.`, rb.wait ? "DROOL waits for each host to come back." : "DROOL doesn't wait for the hosts to come back."];
      case "file_deploy":
        return [
          `${fd.artifact?.name || "The file"} goes to ${fd.path}${fd.elevated ? " as root" : ""}.`,
          fd.overwrite === "replace" ? "Existing files are replaced; their previous versions are kept for rollback." : "Existing files are left alone.",
          ...(fd.validate ? ["The new file is validated first; hosts where validation fails are not changed."] : []),
          ...(fd.serviceAction ? [`Then ${fd.serviceAction} ${fd.serviceName} where the file changed.`] : []),
        ];
      case "system_update":
        return [
          ...(sys.allowRemovals ? ["Packages may be removed where an upgrade needs it."] : []),
          ...(sys.reboot === "always" ? ["Every host reboots afterwards."] : sys.reboot === "if_required" ? ["Hosts that need it reboot afterwards."] : ["No host reboots."]),
        ];
      case "package":
        return pkg.allowRemovals ? ["Other packages may be removed as well."] : [];
      case "command":
      case "script":
        return [become ? `It runs as root (sudo) on ${them}.` : `It runs as the SSH user on ${them}.`, ...(readOnly ? ["Marked read-only: it may run alongside other jobs on the same host."] : [])];
    }
    return [];
  };
  function onSubmit(e: FormEvent) {
    e.preventDefault();
    // The review shows the preview of exactly these targets, not the last one.
    if (!count || !stepReady || !preview.data || preview.isPlaceholderData) return;
    submit.reset();
    setReviewing(true);
  }

  const visibleHosts = (hosts.data?.items ?? []).filter(
    (h) => !h.jump_only && (!filter || `${h.name} ${h.address} ${h.tags.join(" ")}`.toLowerCase().includes(filter.toLowerCase())),
  );
  const toggle = (list: string[], set: (v: string[]) => void, id: string, on: boolean) =>
    set(on ? [...list, id] : list.filter((x) => x !== id));

  return (
    <>
      <PageHeader crumbs={[{ label: "Runs", to: "/jobs" }]} title="New run" description="Run a command, script, update, package or service change, reboot or file deployment on hosts — with a preview, and canaries and batches when you want them." />
      <form onSubmit={onSubmit} className="grid gap-4 xl:grid-cols-[1fr_24rem]">
        <div className="space-y-4">
          <Card title="What to do">
            <div className="space-y-4">
              <div className="flex flex-wrap gap-2">
                {opKinds.map(({ kind: k, label }) => (
                  <Button key={k} type="button" size="sm" variant={kind === k ? "primary" : "secondary"} onClick={() => setKind(k)}>
                    {label}
                  </Button>
                ))}
              </div>
              {kind === "package" ? <PackageForm v={pkg} set={setPkg} />
              : kind === "system_update" ? <SystemUpdateForm v={sys} set={setSys} />
              : kind === "service" ? <ServiceForm v={svc} set={setSvc} />
              : kind === "reboot" ? <RebootForm v={rb} set={setRb} />
              : kind === "file_deploy" ? <FileDeployForm v={fd} set={setFd} />
              : kind === "command" ? (
                <Field label="Command">
                  <Input className="font-mono" value={command} required autoFocus placeholder="df -h"
                    onChange={(e) => setCommand(e.target.value)} />
                </Field>
              ) : (
                <>
                  <Checkbox checked={perOS} onChange={setPerOS} label="Different script per OS family"
                    hint="Each host runs the variant for its discovered OS; hosts without a matching variant use the default or are skipped." />
                  {perOS ? (
                    (["debian", "redhat", "default"] as const).map((k) => (
                      <Field key={k} label={k === "debian" ? "Debian / Ubuntu" : k === "redhat" ? "RHEL / Rocky / Alma / Fedora" : "Default (any other OS, optional)"}>
                        <textarea className="h-32 w-full rounded-ui border border-line bg-surface p-3 font-mono text-xs"
                          value={variants[k]} onChange={(e) => setVariants((v) => ({ ...v, [k]: e.target.value }))} />
                      </Field>
                    ))
                  ) : (
                    <Field label="Script">
                      <textarea className="h-56 w-full rounded-ui border border-line bg-surface p-3 font-mono text-xs"
                        value={script} required onChange={(e) => setScript(e.target.value)} />
                    </Field>
                  )}
                  <Field label="Interpreter">
                    <Select className="max-w-[10rem]" value={interpreter} onChange={(e) => setInterpreter(e.target.value as "sh" | "bash")}>
                      <option value="sh">sh</option>
                      <option value="bash">bash</option>
                    </Select>
                  </Field>
                </>
              )}
              {(kind === "command" || kind === "script") ? (
                <Checkbox checked={become} onChange={setBecome} label="Run as root (sudo)" />
              ) : kind === "file_deploy" ? null : (
                <p className="text-xs text-muted">Runs as root (directly or via the host's sudo setting).</p>
              )}
              {kind === "file_deploy" && stepReady && step.type === "file_deploy" && (
                <DeployPreviewPanel targets={targets} step={step} hasTargets={hasTargets} onResult={setDeploy} />
              )}
              {(kind === "package" || kind === "system_update") && stepReady && (
                <SimulatePanel targets={targets} step={step} hasTargets={hasTargets}
                  allow={kind === "package" ? pkg.allowRemovals : sys.allowRemovals}
                  setAllow={kind === "package" ? setPkgAllow : setSysAllow} onResult={setSim} />
              )}
              <Field label="Name" hint="Optional: shown under Runs.">
                <Input value={name} onChange={(e) => setName(e.target.value)} />
              </Field>
              {/* Less common: the timeout, OS conditions, read-only. Closed, the
                  header still lists what is set, so nothing changes unseen. */}
              <div className="rounded-ui border border-line">
                <button type="button" aria-expanded={advanced} aria-controls="run-advanced"
                  onClick={() => setAdvanced((v) => !v)} className="flex w-full items-center justify-between px-3 py-2 text-left text-sm font-medium">
                  <span>Advanced options
                    <span className="ml-2 font-normal text-muted">
                      {[timeout && `timeout ${timeout} s`, onlyOn.length > 0 && "only some OS families", readOnly && "read-only"].filter(Boolean).join(" · ") || "timeout, OS families, read-only"}
                    </span>
                  </span>
                  <span aria-hidden="true">{advanced ? "▴" : "▾"}</span>
                </button>
                {advanced && (
                  <div id="run-advanced" className="space-y-3 border-t border-line px-3 py-3">
                    <Field label="Timeout (seconds)" hint={`Default ${defaultTimeouts[kind]}.`}>
                      <Input className="max-w-[10rem]" type="number" min={1} max={86400} value={timeout} onChange={(e) => setTimeoutS(e.target.value)} />
                    </Field>
                    <div className="flex flex-wrap items-center gap-4 text-sm">
                      <span className="text-muted">Only run on:</span>
                      {(["debian", "redhat"] as const).map((f) => (
                        <Checkbox key={f} checked={onlyOn.includes(f)}
                          onChange={(on) => setOnlyOn((cur) => (on ? [...cur, f] : cur.filter((x) => x !== f)))}
                          label={f === "debian" ? "Debian / Ubuntu" : "RHEL family / Fedora"} />
                      ))}
                      {onlyOn.length === 0 && <span className="text-xs text-muted">(all hosts)</span>}
                    </div>
                    {(kind === "command" || kind === "script") && (
                      <Checkbox checked={readOnly} onChange={setReadOnly} label="Read-only"
                        hint="Doesn't change the host, so it may run alongside other jobs on the same host (and needs no rollout)." />
                    )}
                  </div>
                )}
              </div>
            </div>
          </Card>

          <Card title="Rollout">
            <div className="space-y-3" data-testid="run-rollout">
              {count < 2 ? (
                <p className="text-sm text-muted">{count === 1 ? "One host: no rollout to plan." : "Choose targets to see how the run rolls out."}</p>
              ) : (
                <>
                  <p className="text-sm">
                    {autoRollout ? <><strong>Recommended for {count} hosts:</strong> </> : <><strong>Rollout:</strong> </>}
                    {rolloutText(strategy)}.
                    {autoRollout && <span className="text-muted"> A bad change stops early instead of reaching every host.</span>}
                  </p>
                  <RolloutAdvice hosts={count} steps={[step]} strategy={strategy} apply={() => setRollout(recommendedRollout)} />
                  {(preview.data?.waves.length ?? 0) > 1 && (
                    <div>
                      <div className="mb-1 text-sm font-medium">Waves</div>
                      <WavesList waves={preview.data!.waves} />
                    </div>
                  )}
                  <button type="button" className="text-sm text-accent underline underline-offset-2" aria-expanded={editRollout}
                    onClick={() => {
                      if (!editRollout && !rolloutTouched) setRolloutState(effective); // start from what is shown
                      setEditRollout((v) => !v);
                    }}>
                    {editRollout ? "Hide rollout settings" : "Change rollout…"}
                  </button>
                  {editRollout && <RolloutFields v={rolloutTouched ? rollout : effective} set={setRollout} hosts={(preview.data?.hosts ?? []).filter((h) => !h.skip).map((h) => ({ id: h.id, name: h.name }))} />}
                </>
              )}
            </div>
          </Card>
          {(preview.data?.desired_warnings ?? []).length > 0 && (
            <Alert tone="warn">
              <strong>This contradicts desired state.</strong>
              <ul className="mt-1 list-disc space-y-1 pl-5">
                {preview.data!.desired_warnings.map((w) => <li key={w.step + w.key + w.message}>{w.message}</li>)}
              </ul>
              <span className="mt-1 block text-xs">You can still run it; the hosts will then show drift. <Link className="underline" to="/desired">Desired state</Link></span>
            </Alert>
          )}
          {submit.error && !reviewing && <Alert>{errorMessage(submit.error)}</Alert>}
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" variant={kind === "reboot" ? "danger" : "primary"} disabled={!count || !stepReady || submit.isPending || preview.isPlaceholderData}>
              {!count ? "Select targets" : `Review: ${kind === "reboot" ? "reboot" : "run on"} ${count} host${count === 1 ? "" : "s"}…`}
            </Button>
            {can("schedules.manage") && (
              <Button type="button" disabled={!stepReady || !hasTargets}
                onClick={() => navigate("/jobs/schedules/new", { state: { name, steps: [step], targets, strategy: carried } })}>
                Schedule this run
              </Button>
            )}
            {can("workflows.manage") && (
              <Button type="button" variant="ghost" disabled={!stepReady}
                onClick={() => navigate("/jobs/workflows/new", { state: { name, steps: [step], strategy: carried } })}>
                Save as workflow
              </Button>
            )}
          </div>
        </div>

        <div className="space-y-4">
          <Card title="Targets">
            <div className="space-y-4">
              <Input placeholder="Filter hosts…" value={filter} onChange={(e) => setFilter(e.target.value)} />
              <div className="max-h-64 space-y-1.5 overflow-y-auto">
                {visibleHosts.map((h) => (
                  <Checkbox key={h.id} checked={hostIds.includes(h.id)} onChange={(on) => toggle(hostIds, setHostIds, h.id, on)}
                    label={<span className="inline-flex items-center gap-2">{h.name} <HostStatusBadge status={h.status} /></span>} />
                ))}
                {visibleHosts.length === 0 && <p className="text-sm text-muted">No hosts.</p>}
              </div>
              {(groups.data?.items.length ?? 0) > 0 && (
                <div className="space-y-1.5 border-t border-line pt-3">
                  <div className="text-xs font-medium uppercase text-muted">Packs</div>
                  {groups.data!.items.map((g) => (
                    <Checkbox key={g.id} checked={groupIds.includes(g.id)} onChange={(on) => toggle(groupIds, setGroupIds, g.id, on)}
                      label={`${g.name} (${g.host_ids.length})`} />
                  ))}
                </div>
              )}
              <Field label="Tags" hint="Hosts with any of these tags.">
                <Input value={tags} placeholder="role:web" onChange={(e) => setTags(e.target.value)} />
              </Field>
            </div>
          </Card>
          {hasTargets && (
            <Card title={`Will run on ${count} host${count === 1 ? "" : "s"}${skipCount ? `, skip ${skipCount}` : ""}`}>
              {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
              <ul className="space-y-1 text-sm">
                {preview.data?.hosts.map((h) => (
                  <li key={h.id} className={h.skip ? "text-muted line-through" : undefined}>
                    {h.name}
                    {h.warning && <span className={h.skip ? "block text-xs text-muted no-underline" : "block text-xs text-warn"}>{h.warning}</span>}
                  </li>
                ))}
              </ul>
              {preview.data?.warnings.map((w) => <p key={w} className="mt-2 text-xs text-muted">{w}</p>)}
            </Card>
          )}
        </div>
      </form>
      {reviewing && preview.data && (
        <RunConfirmDialog step={step} preview={preview.data} strategy={strategy} consequences={consequences()}
          sim={(kind === "package" || kind === "system_update") ? sim : null} deploy={kind === "file_deploy" ? deploy : null}
          busy={submit.isPending} error={submit.error} onConfirm={() => submit.mutate()} onClose={() => setReviewing(false)} />
      )}
    </>
  );
}
