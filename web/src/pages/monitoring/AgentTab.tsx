import { useState } from "react";
import { Link } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage, formatBytes } from "@/lib/format";
import { StatusBadge } from "@/components/monitoring/Status";
import { fmtTime } from "@/components/monitoring/format";
import { fidelityText } from "@/components/monitoring/HostMonitoringCard";
import { InstallHelperButton } from "@/components/monitoring/HelperInstall";
import { useDiagnostics, useSaveHostSettings, type MonHostOverview, type MonLimits } from "@/components/monitoring/queries";


function Budget({ id, o }: { id: string; o: MonHostOverview }) {
  const { can } = useAuth();
  const save = useSaveHostSettings(id);
  const a = o.state?.agent;
  const want = o.agent?.limits;
  const [form, setForm] = useState<MonLimits>(() => o.settings.limits ?? {});
  const num = (v: string) => (v === "" ? undefined : Number(v));
  return (
    <Card title="Agent resource budget">
      <p className="mb-3 text-sm text-muted">
        The managed workload wins: the agent runs with a low CPU weight, a hard CPU quota and a memory ceiling, and lowers its own
        monitoring fidelity before it competes with the host's applications. Package installs, scripts and firewall changes run
        outside this budget.
      </p>
      {a ? (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-4">
          <dt className="text-muted">Limits</dt>
          <dd>{a.enforced ? <StatusBadge status="ok">Enforced by systemd</StatusBadge> : <StatusBadge status="warning">Not enforced</StatusBadge>}</dd>
          <dt className="text-muted">CPU quota</dt><dd>{a.cpu_quota_percent == null ? "none" : `${Math.round(a.cpu_quota_percent)}% of one CPU`}</dd>
          <dt className="text-muted">CPU weight</dt><dd>{a.cpu_weight ?? "—"}</dd>
          <dt className="text-muted">Memory high / max</dt><dd>{a.memory_high_bytes == null ? "none" : formatBytes(a.memory_high_bytes)} / {a.memory_max_bytes == null ? "none" : formatBytes(a.memory_max_bytes)}</dd>
          <dt className="text-muted">CPU now</dt><dd>{a.cpu_percent == null ? "—" : `${a.cpu_percent.toFixed(1)}% of one CPU`}</dd>
          <dt className="text-muted">Memory now / peak</dt><dd>{a.memory_bytes == null ? "—" : formatBytes(a.memory_bytes)} / {a.memory_peak_bytes == null ? "—" : formatBytes(a.memory_peak_bytes)}</dd>
          <dt className="text-muted">Offline buffer</dt><dd>{formatBytes(a.buffer_bytes)} of {formatBytes(a.buffer_limit)}</dd>
          <dt className="text-muted">Discarded samples</dt><dd>{a.dropped_points}</dd>
          {a.throttled && (<><dt className="text-muted">Throttled</dt><dd>Yes (its CPU quota is holding it back)</dd></>)}
          {(a.oom_kills ?? 0) > 0 && (<><dt className="text-muted">Out-of-memory kills</dt><dd>{a.oom_kills}</dd></>)}
        </dl>
      ) : <p className="text-sm text-muted">No agent on this host: SSH monitoring runs no resident process.</p>}
      {a?.note && <p className="mt-2 text-xs text-muted">{a.note}</p>}
      {want && (
        <p className="mt-3 text-xs text-muted">
          DROOL asks for: {want.CPUQuotaPct}% CPU quota, weight {want.CPUWeight}, memory high {want.MemoryHighMB} MB, maximum {want.MemoryMaxMB} MB
          {o.agent?.overridden ? " (this host's own budget)" : " (fleet default)"}.
        </p>
      )}
      {can("monitoring.settings") && o.agent && (
        <details className="mt-3 text-sm">
          <summary className="cursor-pointer text-accent">Advanced: this host's budget</summary>
          <div className="mt-2 grid max-w-2xl gap-3 sm:grid-cols-4">
            <Field label="CPU quota (%)"><Input type="number" min={5} max={800} value={form.cpu_quota_percent ?? ""} placeholder={String(want?.CPUQuotaPct ?? "")} onChange={(e) => setForm({ ...form, cpu_quota_percent: num(e.target.value) })} /></Field>
            <Field label="CPU weight"><Input type="number" min={1} max={10000} value={form.cpu_weight ?? ""} placeholder={String(want?.CPUWeight ?? "")} onChange={(e) => setForm({ ...form, cpu_weight: num(e.target.value) })} /></Field>
            <Field label="Memory high (MB)"><Input type="number" min={48} value={form.memory_high_mb ?? ""} placeholder={String(want?.MemoryHighMB ?? "")} onChange={(e) => setForm({ ...form, memory_high_mb: num(e.target.value) })} /></Field>
            <Field label="Memory max (MB)"><Input type="number" min={64} value={form.memory_max_mb ?? ""} placeholder={String(want?.MemoryMaxMB ?? "")} onChange={(e) => setForm({ ...form, memory_max_mb: num(e.target.value) })} /></Field>
          </div>
          {save.error && <div className="mt-2"><Alert>{errorMessage(save.error)}</Alert></div>}
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="primary" disabled={save.isPending} onClick={() => save.mutate({ ...o.settings, limits: Object.values(form).some((v) => v != null) ? form : undefined })}>Save budget</Button>
            <Button size="sm" disabled={save.isPending} onClick={() => { setForm({}); save.mutate({ ...o.settings, limits: undefined }); }}>Use the fleet default</Button>
          </div>
        </details>
      )}
    </Card>
  );
}

function Diagnostics({ id }: { id: string }) {
  const [on, setOn] = useState(false);
  const ask = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/monitoring/diagnostics", { params: { path: { id } } })),
    onSuccess: () => setOn(true),
  });
  const d = useDiagnostics(id, on);
  return (
    <Card title="Agent diagnostics">
      <p className="mb-2 text-sm text-muted">The agent keeps recent routine diagnostics in a small memory ring instead of writing them to the host's disk.</p>
      {ask.error && <Alert>{errorMessage(ask.error)}</Alert>}
      <Button size="sm" onClick={() => ask.mutate()} disabled={ask.isPending}>Fetch from the agent</Button>
      {on && (
        <pre className="mt-2 max-h-72 overflow-auto rounded-ui bg-surface-2 p-2 font-mono text-xs">
          {(d.data?.entries ?? []).map((e) => `${e.time} ${e.level} ${e.message}`).join("\n") || "Waiting for the agent…"}
        </pre>
      )}
    </Card>
  );
}

export function AgentTab({ id, o }: { id: string; o: MonHostOverview }) {
  const { can } = useAuth();
  const st = o.state;
  return (
    <div className="space-y-4">
      <Card title="How this host is monitored">
        <p className="text-sm">{o.has_data ? fidelityText(o) : "No data yet."}</p>
        {st?.fidelity.reduced && (
          <div className="mt-2"><Alert tone="warn">Monitoring fidelity is reduced to save resources since {fmtTime((st.fidelity.since ?? 0) * 1000)}: {(st.fidelity.reasons ?? []).join("; ")}</Alert></div>
        )}
        {o.source === "ssh" && can("agents.enroll") && (
          <p className="mt-2 text-sm text-muted">
            Install the DROOL agent for continuous monitoring, kernel storage evidence and the flash probe: <Link className="text-accent" to={`/hosts/${id}`}>Host page → Management</Link>.
          </p>
        )}
        {(st?.collectors ?? []).length > 0 && (
          <ul className="mt-3 grid gap-1 text-sm sm:grid-cols-2">
            {st!.collectors.map((c) => (
              <li key={c.name} className="flex items-center gap-2">
                <StatusBadge status={c.state === "ok" ? "ok" : c.state === "failed" ? "critical" : c.state === "deferred" ? "warning" : "unknown"}>
                  {c.state === "ok" ? "OK" : c.state === "failed" ? "Failing" : c.state === "deferred" ? "Deferred" : "Unavailable"}
                </StatusBadge>
                <span>{c.name}</span>
                {c.note && <span className="text-xs text-muted">{c.note}</span>}
              </li>
            ))}
          </ul>
        )}
        {o.gaps.length > 0 && (
          <div className="mt-3 text-sm">
            <div className="text-xs text-muted">Telemetry gaps (the agent's offline buffer filled during a controller outage)</div>
            <ul>{o.gaps.slice(-5).map((g) => <li key={g.from}>{fmtTime(g.from * 1000)} – {fmtTime(g.to * 1000)}</li>)}</ul>
          </div>
        )}
      </Card>
      <Card title="Monitoring helpers">
        <p className="mb-3 text-sm text-muted">
          DROOL reads the kernel first, then tools already installed. These optional helpers add detail where they help; they are
          installed only when you approve it, as normal package jobs.
        </p>
        <ul className="space-y-2">
          {o.helpers.map((h) => (
            <li key={h.package} className="flex flex-wrap items-start gap-2 text-sm">
              <span className="w-32 font-mono text-xs">{h.package}</span>
              {h.installed ? <Badge tone="ok">✓ Installed</Badge> : h.useful ? <Badge tone="warn">Would help</Badge> : <Badge>Not needed here</Badge>}
              {h.requested && <Badge tone="accent">{h.requested === "requested" ? "Installation requested" : h.requested === "failed" ? "Installation failed" : "Installed by DROOL"}</Badge>}
              {h.job_id && <Link className="text-xs text-accent" to={`/jobs/${h.job_id}`}>job</Link>}
              {h.approved && <span className="text-xs text-muted">(standing approval: {h.approved})</span>}
              <span className="basis-full text-xs text-muted">{h.purpose}</span>
              {h.useful && !h.installed && h.requested !== "requested" && <InstallHelperButton hostId={id} pkg={h.package} />}
            </li>
          ))}
          {o.helpers.length === 0 && <li className="text-sm text-muted">Shown once the host has reported.</li>}
        </ul>
      </Card>
      {(o.agent || o.state?.agent) && <Budget id={id} o={o} />}
      <Card title="Health checks">
        <p className="text-sm text-muted">
          Which checks run on this host, and their thresholds, are under <Link className="text-accent" to="?tab=health">Health settings</Link>.
        </p>
      </Card>
      {o.agent?.connected && can("monitoring.manage") && <Diagnostics id={id} />}
    </div>
  );
}
