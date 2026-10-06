import { useMemo, useState, type FormEvent } from "react";
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

type Kind = "command" | "script";
type TargetSpec = Schemas["TargetSpec"];

export function NewJobPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const hosts = useHosts();
  const groups = useGroups();

  const [kind, setKind] = useState<Kind>("command");
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

  const targets: TargetSpec = useMemo(() => ({
    hosts: hostIds, groups: groupIds, tags: tags.split(/[,\s]+/).filter(Boolean),
  }), [hostIds, groupIds, tags]);
  const hasTargets = hostIds.length + groupIds.length + (targets.tags?.length ?? 0) > 0;

  const preview = useQuery({
    queryKey: ["jobs", "preview", targets],
    queryFn: () => unwrap(api.POST("/jobs/preview", { body: { targets } })),
    enabled: hasTargets,
  });

  // Idempotency key per form instance: a double submit can't create two jobs.
  const [idemKey] = useState(() => crypto.randomUUID());
  const submit = useMutation({
    mutationFn: () => {
      const timeout_s = timeout ? Number(timeout) : undefined;
      const step: Schemas["Operation"] = kind === "command"
        ? { type: "command", command, become, read_only: readOnly, timeout_s }
        : { type: "script", body: script, interpreter, become, read_only: readOnly, timeout_s };
      return unwrap(api.POST("/jobs", {
        params: { header: { "Idempotency-Key": idemKey } },
        body: {
          name: name || undefined, steps: [step], targets,
          strategy: { concurrency: Number(concurrency) || 10, failure_threshold: threshold ? Number(threshold) : null },
        },
      }));
    },
    onSuccess: (j) => {
      void qc.invalidateQueries({ queryKey: jk.jobs });
      navigate(`/jobs/${j.id}`);
    },
  });

  const count = preview.data?.hosts.length ?? 0;
  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!count) return;
    const what = kind === "command" ? `"${command}"` : "this script";
    if (confirm(`Run ${what} on ${count} host${count === 1 ? "" : "s"}${become ? " with sudo" : ""}?`)) submit.mutate();
  }

  const visibleHosts = (hosts.data?.items ?? []).filter(
    (h) => !h.jump_only && (!filter || `${h.name} ${h.address} ${h.tags.join(" ")}`.toLowerCase().includes(filter.toLowerCase())),
  );
  const toggle = (list: string[], set: (v: string[]) => void, id: string, on: boolean) =>
    set(on ? [...list, id] : list.filter((x) => x !== id));

  return (
    <>
      <PageHeader title="Run command" />
      <form onSubmit={onSubmit} className="grid gap-4 xl:grid-cols-[1fr_24rem]">
        <div className="space-y-4">
          <Card title="What to run">
            <div className="space-y-4">
              <div className="flex gap-2">
                {(["command", "script"] as const).map((k) => (
                  <Button key={k} type="button" size="sm" variant={kind === k ? "primary" : "secondary"} onClick={() => setKind(k)}>
                    {k === "command" ? "Command" : "Script"}
                  </Button>
                ))}
              </div>
              {kind === "command" ? (
                <Field label="Command">
                  <Input className="font-mono" value={command} required autoFocus placeholder="df -h"
                    onChange={(e) => setCommand(e.target.value)} />
                </Field>
              ) : (
                <>
                  <Field label="Script">
                    <textarea className="h-56 w-full rounded-ui border border-line bg-surface p-3 font-mono text-xs"
                      value={script} required onChange={(e) => setScript(e.target.value)} />
                  </Field>
                  <Field label="Interpreter">
                    <Select className="max-w-[10rem]" value={interpreter} onChange={(e) => setInterpreter(e.target.value as "sh" | "bash")}>
                      <option value="sh">sh</option>
                      <option value="bash">bash</option>
                    </Select>
                  </Field>
                </>
              )}
              <div className="space-y-2">
                <Checkbox checked={become} onChange={setBecome} label="Run as root (sudo)" />
                <Checkbox checked={readOnly} onChange={setReadOnly} label="Read-only"
                  hint="Doesn't change the host, so it may run alongside other jobs on the same host." />
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Job name" hint="Optional.">
                  <Input value={name} onChange={(e) => setName(e.target.value)} />
                </Field>
                <Field label="Timeout (seconds)" hint={kind === "command" ? "Default 300." : "Default 600."}>
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
          <Button type="submit" variant="primary" disabled={!count || submit.isPending}>
            {count ? `Run on ${count} host${count === 1 ? "" : "s"}` : "Select targets"}
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
            <Card title={`Will run on ${count} host${count === 1 ? "" : "s"}`}>
              {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
              <ul className="space-y-1 text-sm">
                {preview.data?.hosts.map((h) => (
                  <li key={h.id}>
                    {h.name}
                    {h.warning && <span className="block text-xs text-warn">{h.warning}</span>}
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
