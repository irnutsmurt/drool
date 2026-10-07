import { useCallback, useMemo, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { jk, useGroups, useHosts } from "@/api/queries";
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
import { describeStep } from "./StepResult";
import { DeployPreviewPanel, FileDeployForm, emptyFileDeploy, fileDeployStep, type FileDeployState } from "../files/FileDeployForm";

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
  const [groupIds, setGroupIds] = useState<string[]>([]);
  const [tags, setTags] = useState("");
  const [concurrency, setConcurrency] = useState("10");
  const [threshold, setThreshold] = useState("");
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
  const stepReady = {
    command: command.trim() !== "",
    script: perOS ? Object.values(variants).some((v) => v.trim()) : script.trim() !== "",
    package: packageNames(pkg.names).length > 0,
    system_update: true,
    service: svc.name.trim() !== "",
    reboot: true,
    file_deploy: !!fd.artifact && fd.path.trim().startsWith("/") && (!fd.serviceAction || fd.serviceName.trim() !== ""),
  }[kind];

  const preview = useQuery({
    queryKey: ["jobs", "preview", targets, conditional && stepReady ? step : null],
    queryFn: () => unwrap(api.POST("/jobs/preview", {
      body: { targets, steps: conditional && stepReady ? [step] : undefined },
    })),
    enabled: hasTargets,
  });

  // Idempotency key per form instance: a double submit can't create two jobs.
  const [idemKey] = useState(() => crypto.randomUUID());
  const submit = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs", {
      params: { header: { "Idempotency-Key": idemKey } },
      body: {
        name: name || undefined, steps: [step], targets,
        strategy: { concurrency: Number(concurrency) || 10, failure_threshold: threshold ? Number(threshold) : null },
      },
    })),
    onSuccess: (j) => {
      void qc.invalidateQueries({ queryKey: jk.jobs });
      navigate(`/jobs/${j.id}`);
    },
  });

  const count = preview.data?.hosts.filter((h) => !h.skip).length ?? 0;
  const skipCount = preview.data?.hosts.filter((h) => h.skip).length ?? 0;
  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!count || !stepReady) return;
    const n = `${count} host${count === 1 ? "" : "s"}`;
    let question: string;
    switch (kind) {
      case "command":
      case "script": {
        const what = kind === "command" ? `"${command}"` : perOS ? "these scripts" : "this script";
        question = `Run ${what} on ${n}${become ? " with sudo" : ""}?`;
        break;
      }
      case "reboot":
        question = `Reboot ${n}? Everything running on ${count === 1 ? "it" : "them"} stops until ${count === 1 ? "it comes" : "they come"} back.`;
        break;
      case "file_deploy":
        question = `Deploy ${fd.artifact?.name || "this file"} to ${fd.path} on ${n}${fd.elevated ? " as root" : ""}?` +
          (fd.overwrite === "replace" ? "\nExisting files are replaced; their previous versions are kept for rollback." : "\nExisting files are left alone.") +
          (fd.validate ? "\nThe new file is validated first; hosts where validation fails are not changed." : "") +
          (fd.serviceAction ? `\nThen ${fd.serviceAction} ${fd.serviceName} where the file changed.` : "");
        break;
      case "system_update":
        question = `${describeStep(step)}\n\nRun on ${n}?` +
          (sys.allowRemovals ? "\nPackages may be removed where an upgrade needs it." : "") +
          (sys.reboot === "always" ? "\nEvery host reboots afterwards." : sys.reboot === "if_required" ? "\nHosts that need it reboot afterwards." : "");
        break;
      default:
        question = `${describeStep(step)}\n\nRun on ${n}?` +
          (kind === "package" && pkg.allowRemovals ? "\nOther packages may be removed as well." : "");
    }
    if (confirm(question)) submit.mutate();
  }

  const visibleHosts = (hosts.data?.items ?? []).filter(
    (h) => !h.jump_only && (!filter || `${h.name} ${h.address} ${h.tags.join(" ")}`.toLowerCase().includes(filter.toLowerCase())),
  );
  const toggle = (list: string[], set: (v: string[]) => void, id: string, on: boolean) =>
    set(on ? [...list, id] : list.filter((x) => x !== id));

  return (
    <>
      <PageHeader title="New job" />
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
              <div className="space-y-2">
                <div className="flex flex-wrap items-center gap-4 text-sm">
                  <span className="text-muted">Only run on:</span>
                  {(["debian", "redhat"] as const).map((f) => (
                    <Checkbox key={f} checked={onlyOn.includes(f)}
                      onChange={(on) => setOnlyOn((cur) => (on ? [...cur, f] : cur.filter((x) => x !== f)))}
                      label={f === "debian" ? "Debian / Ubuntu" : "RHEL family / Fedora"} />
                  ))}
                  {onlyOn.length === 0 && <span className="text-xs text-muted">(all hosts)</span>}
                </div>
                {(kind === "command" || kind === "script") ? (
                  <>
                    <Checkbox checked={become} onChange={setBecome} label="Run as root (sudo)" />
                    <Checkbox checked={readOnly} onChange={setReadOnly} label="Read-only"
                      hint="Doesn't change the host, so it may run alongside other jobs on the same host." />
                  </>
                ) : kind === "file_deploy" ? null : (
                  <p className="text-xs text-muted">Runs as root (directly or via the host's sudo setting).</p>
                )}
              </div>
              {kind === "file_deploy" && stepReady && step.type === "file_deploy" && (
                <DeployPreviewPanel targets={targets} step={step} hasTargets={hasTargets} />
              )}
              {(kind === "package" || kind === "system_update") && stepReady && (
                <SimulatePanel targets={targets} step={step} hasTargets={hasTargets}
                  allow={kind === "package" ? pkg.allowRemovals : sys.allowRemovals}
                  setAllow={kind === "package" ? setPkgAllow : setSysAllow} />
              )}
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Job name" hint="Optional.">
                  <Input value={name} onChange={(e) => setName(e.target.value)} />
                </Field>
                <Field label="Timeout (seconds)" hint={`Default ${defaultTimeouts[kind]}.`}>
                  <Input type="number" min={1} max={86400} value={timeout} onChange={(e) => setTimeoutS(e.target.value)} />
                </Field>
              </div>
            </div>
          </Card>

          <Card title="Rollout">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Hosts at a time">
                <Input type="number" min={1} max={100} value={concurrency} onChange={(e) => setConcurrency(e.target.value)} />
              </Field>
              <Field label="Stop after failures" hint="Leave empty to run on every host regardless.">
                <Input type="number" min={1} value={threshold} onChange={(e) => setThreshold(e.target.value)} />
              </Field>
            </div>
          </Card>
          {submit.error && <Alert>{errorMessage(submit.error)}</Alert>}
          <Button type="submit" variant={kind === "reboot" ? "danger" : "primary"} disabled={!count || !stepReady || submit.isPending}>
            {!count ? "Select targets" : `${kind === "reboot" ? "Reboot" : "Run on"} ${count} host${count === 1 ? "" : "s"}`}
          </Button>
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
                  <div className="text-xs font-medium uppercase text-muted">Groups</div>
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
    </>
  );
}
