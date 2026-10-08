import { useEffect, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useGroups } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { useApprovals, useMonInvalidate, useMonSettings, type MonSettings, type MonThresholds } from "@/components/monitoring/queries";

const thresholdFields: { key: keyof MonThresholds; label: string; unit: string }[] = [
  { key: "cpu_percent", label: "CPU busy", unit: "%" },
  { key: "cpu_minutes", label: "…for at least", unit: "min" },
  { key: "memory_percent", label: "Memory warning", unit: "%" },
  { key: "memory_critical_percent", label: "Memory critical", unit: "%" },
  { key: "memory_minutes", label: "…for at least", unit: "min" },
  { key: "disk_warning_percent", label: "Disk space warning", unit: "%" },
  { key: "disk_critical_percent", label: "Disk space critical", unit: "%" },
  { key: "inode_warning_percent", label: "Inodes warning", unit: "%" },
  { key: "inode_critical_percent", label: "Inodes critical", unit: "%" },
  { key: "service_failed_minutes", label: "Service failed for", unit: "min" },
  { key: "watched_stopped_minutes", label: "Watched service stopped for", unit: "min" },
  { key: "interface_down_minutes", label: "Interface down for", unit: "min" },
  { key: "temperature_minutes", label: "Above the hardware's limit for", unit: "min" },
  { key: "stale_intervals", label: "Stale after missed reports", unit: "×" },
];

export function MonitoringSettingsPage() {
  const { can } = useAuth();
  const q = useMonSettings();
  const inv = useMonInvalidate();
  const [s, setS] = useState<MonSettings>();
  useEffect(() => { if (q.data && !s) setS(q.data.settings); }, [q.data, s]);
  const save = useMutation({
    mutationFn: (body: MonSettings) => unwrap(api.PUT("/monitoring/settings", { body })),
    onSuccess: (r) => { setS(r.settings); inv(); },
  });
  const edit = can("monitoring.settings");
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!s || !q.data) return <p className="text-sm text-muted">Loading…</p>;
  const shown = s.profile === "custom" ? s.custom : q.data.recommended;
  const set = <K extends keyof MonSettings>(k: K, v: MonSettings[K]) => setS({ ...s, [k]: v });
  const int = (v: string) => Math.max(0, Number(v) || 0);
  return (
    <div className="space-y-4">
      <Card title="Monitoring">
        <div className="space-y-3">
          <Checkbox label="Monitor hosts (agents stream continuously; SSH hosts are polled)" checked={s.enabled} disabled={!edit} onChange={(v) => set("enabled", v)} />
          <Field label="Health thresholds">
            <Select value={s.profile} disabled={!edit} onChange={(e) => setS({ ...s, profile: e.target.value as "custom", custom: e.target.value === "custom" && s.profile !== "custom" ? q.data!.recommended : s.custom })} className="max-w-xs">
              <option value="recommended">Recommended</option>
              <option value="custom">Custom</option>
            </Select>
          </Field>
          <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-5">
            {thresholdFields.map((f) => (
              <Field key={f.key} label={`${f.label} (${f.unit})`}>
                <Input type="number" value={shown[f.key]} disabled={!edit || s.profile !== "custom"}
                  onChange={(e) => setS({ ...s, custom: { ...s.custom, [f.key]: Number(e.target.value) } })} />
              </Field>
            ))}
          </div>
          <p className="text-xs text-muted">
            Conditions must hold for the whole duration and clear below a lower level (hysteresis), so momentary spikes never count.
            Temperatures are judged only against each sensor's own hardware limits.
          </p>
        </div>
      </Card>
      <Card title="History">
        <Field label="Keep hourly history for (days)" hint="Raw points are kept for a day and five-minute averages for eight days.">
          <Input type="number" min={7} max={400} value={s.retention_days} disabled={!edit} onChange={(e) => set("retention_days", int(e.target.value))} className="max-w-xs" />
        </Field>
        {!q.data.last_prune.at.startsWith("0001") && (
          <p className="mt-2 text-xs text-muted">Last pruning: {q.data.last_prune.points} points, {q.data.last_prune.events} events removed in {q.data.last_prune.ms} ms.</p>
        )}
      </Card>
      <Card title="Agents (continuous)">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="CPU, memory, network, disk IO every (s)"><Input type="number" value={s.sample_s} disabled={!edit} onChange={(e) => set("sample_s", int(e.target.value))} /></Field>
          <Field label="Filesystems, sensors, GPU every (s)"><Input type="number" value={s.slow_s} disabled={!edit} onChange={(e) => set("slow_s", int(e.target.value))} /></Field>
          <Field label="Services every (s)"><Input type="number" value={s.services_s} disabled={!edit} onChange={(e) => set("services_s", int(e.target.value))} /></Field>
          <Field label="Storage devices every (s)"><Input type="number" value={s.storage_s} disabled={!edit} onChange={(e) => set("storage_s", int(e.target.value))} /></Field>
          <Field label="SMART / NVMe health every (s)"><Input type="number" value={s.smart_s} disabled={!edit} onChange={(e) => set("smart_s", int(e.target.value))} /></Field>
          <Field label="Offline buffer (KB)" hint="Memory only: when full, the oldest routine samples go and the gap is reported"><Input type="number" value={s.buffer_kb} disabled={!edit} onChange={(e) => set("buffer_kb", int(e.target.value))} /></Field>
        </div>
        <div className="mt-3 text-sm font-medium">Default agent resource budget</div>
        <div className="mt-1 grid gap-3 sm:grid-cols-4">
          {(["cpu_quota_percent", "cpu_weight", "memory_high_mb", "memory_max_mb"] as const).map((k) => (
            <Field key={k} label={{ cpu_quota_percent: "CPU quota (% of one CPU)", cpu_weight: "CPU weight", memory_high_mb: "Memory high (MB)", memory_max_mb: "Memory max (MB)" }[k]}>
              <Input type="number" placeholder={{ cpu_quota_percent: "25", cpu_weight: "20", memory_high_mb: "96", memory_max_mb: "160" }[k]} value={s.limits?.[k] ?? ""} disabled={!edit}
                onChange={(e) => setS({ ...s, limits: { ...s.limits, [k]: e.target.value === "" ? undefined : Number(e.target.value) } })} />
            </Field>
          ))}
        </div>
        <p className="mt-2 text-xs text-muted">Empty fields use the measured defaults. The budget limits monitoring only; package installs and other operations run outside it.</p>
      </Card>
      <Card title="SSH hosts (polled)">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Poll every (s)"><Input type="number" value={s.ssh_poll_s} disabled={!edit} onChange={(e) => set("ssh_poll_s", int(e.target.value))} /></Field>
          <Field label="Storage health every (s)"><Input type="number" value={s.ssh_smart_s} disabled={!edit} onChange={(e) => set("ssh_smart_s", int(e.target.value))} /></Field>
        </div>
        <div className="mt-3">
          <Checkbox label="Read SMART and kernel storage messages with sudo where the host allows it" hint="Only during the storage health check, so routine polls don't fill the host's auth log." checked={s.ssh_sudo} disabled={!edit} onChange={(v) => set("ssh_sudo", v)} />
        </div>
      </Card>
      {edit && (
        <div className="flex items-center gap-3">
          <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate(s)}>{save.isPending ? "Saving…" : "Save monitoring settings"}</Button>
          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          {save.isSuccess && <span className="text-sm text-ok">✓ Saved and sent to connected agents.</span>}
        </div>
      )}
      <Approvals />
    </div>
  );
}

function Approvals() {
  const { can } = useAuth();
  const edit = can("monitoring.settings");
  const a = useApprovals();
  const groups = useGroups();
  const inv = useMonInvalidate();
  const [scope, setScope] = useState("global");
  const [group, setGroup] = useState("");
  const [pkg, setPkg] = useState("smartmontools");
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/monitoring/approvals", { body: { scope: scope as "global", group_id: scope === "group" ? group : undefined, package: pkg as "smartmontools" } })),
    onSuccess: () => inv(),
  });
  const del = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/monitoring/approvals/{id}", { params: { path: { id } } })),
    onSuccess: () => inv(),
  });
  return (
    <Card title="Monitoring helper approvals">
      <p className="mb-3 text-sm text-muted">
        A standing approval lets DROOL install an allowlisted helper ({(a.data?.allowlist ?? []).join(", ")}) wherever it would add monitoring
        detail, as a normal package job on your behalf. Without one, helpers are installed only when someone clicks Install.
        Removing an approval never uninstalls anything.
      </p>
      {a.error && <Alert>{errorMessage(a.error)}</Alert>}
      <ul className="mb-3 space-y-1 text-sm">
        {(a.data?.approvals ?? []).map((x) => (
          <li key={x.id} className="flex items-center gap-2">
            <span className="font-mono text-xs">{x.package}</span>
            <span>{x.scope === "global" ? "on every host" : `in group ${x.group_name || x.group_id}`}</span>
            {edit && <Button size="sm" variant="ghost" onClick={() => del.mutate(x.id)}>Remove</Button>}
          </li>
        ))}
        {(a.data?.approvals ?? []).length === 0 && <li className="text-muted">No standing approvals.</li>}
      </ul>
      {edit && (
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Helper">
            <Select value={pkg} onChange={(e) => setPkg(e.target.value)}>
              {(a.data?.allowlist ?? []).map((p) => <option key={p} value={p}>{p}</option>)}
            </Select>
          </Field>
          <Field label="Where">
            <Select value={scope} onChange={(e) => setScope(e.target.value)}>
              <option value="global">Every host</option>
              <option value="group">A group</option>
            </Select>
          </Field>
          {scope === "group" && (
            <Field label="Group">
              <Select value={group} onChange={(e) => setGroup(e.target.value)}>
                <option value="">Choose…</option>
                {(groups.data?.items ?? []).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
              </Select>
            </Field>
          )}
          <Button onClick={() => add.mutate()} disabled={add.isPending || (scope === "group" && !group)}>Approve</Button>
          {add.error && <Alert>{errorMessage(add.error)}</Alert>}
        </div>
      )}
    </Card>
  );
}
