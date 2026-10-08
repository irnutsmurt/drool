import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { useFleet, type MonFleetHost } from "@/components/monitoring/queries";
import { deviceHealth, SeverityBadge, StatusBadge, statusOf } from "@/components/monitoring/Status";
import { fmtPct, fmtSince, fmtTemp } from "@/components/monitoring/format";
import { HelperInstallDialog } from "@/components/monitoring/HelperInstall";
import { ConvertDialog } from "@/components/monitoring/ConvertDialog";

const categories = [
  { key: "unhealthy", label: "Needs attention" },
  { key: "stale", label: "Unreachable / stale" },
  { key: "cpu", label: "CPU" },
  { key: "memory", label: "Memory" },
  { key: "disk", label: "Low disk space" },
  { key: "storage", label: "Storage health" },
  { key: "services", label: "Service failures" },
  { key: "network", label: "Interfaces" },
  { key: "temperature", label: "Temperature" },
  { key: "fidelity", label: "Reduced fidelity" },
  { key: "helpers", label: "Missing helpers" },
  { key: "no_data", label: "No data yet" },
  { key: "all", label: "All hosts" },
];

const PAGE = 100;

function Worst({ r }: { r: MonFleetHost }) {
  if (r.source === "") return <StatusBadge status="unknown">No data</StatusBadge>;
  if (r.stale) return <StatusBadge status="warning">Stale</StatusBadge>;
  const s = statusOf(r.worst);
  return <StatusBadge status={s}>{r.issues === 0 ? "Healthy" : `${r.issues} issue${r.issues === 1 ? "" : "s"}`}</StatusBadge>;
}

export function MonitoringPage() {
  const [params, setParams] = useSearchParams();
  const category = params.get("category") ?? "all";
  const [q, setQ] = useState("");
  const [sort, setSort] = useState("attention");
  const [offset, setOffset] = useState(0);
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [dialog, setDialog] = useState<"" | "helpers" | "convert">("");
  const { can } = useAuth();
  const f = useFleet({ category, q: q || undefined, sort, limit: PAGE, offset });
  const d = f.data;
  const setCat = (c: string) => {
    setOffset(0);
    setParams((p) => { p.set("category", c); return p; }, { replace: true });
  };
  const toggle = (id: string) => setSel((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const rows = d?.rows ?? [];
  const allSel = rows.length > 0 && rows.every((r) => sel.has(r.host_id));
  return (
    <>
      <PageHeader title="Monitoring"
        description={d ? `${d.monitored} of ${d.hosts} hosts reporting: ${d.agent} with the agent (continuous), ${d.ssh} over SSH (polled)` : undefined} />
      {f.error && <Alert>{errorMessage(f.error)}</Alert>}
      <div role="group" aria-label="Show hosts" className="mb-4 flex flex-wrap gap-2">
        {categories.map((c) => {
          const n = c.key === "all" ? d?.hosts : d?.counts[c.key];
          return (
            <button key={c.key} type="button" aria-pressed={category === c.key} onClick={() => setCat(c.key)}
              className={cx("rounded-ui border px-3 py-1.5 text-sm", category === c.key ? "border-accent bg-accent/15 font-medium" : "border-line hover:bg-surface-2",
                c.key !== "all" && n ? "" : "")}>
              {c.label} <span className={cx("ml-1 tabular-nums", n ? "font-semibold" : "text-muted")}>{n ?? 0}</span>
            </button>
          );
        })}
      </div>
      <Card>
        <div className="mb-3 flex flex-wrap items-end gap-3">
          <Field label="Host"><Input value={q} placeholder="Name contains…" onChange={(e) => { setQ(e.target.value); setOffset(0); }} /></Field>
          <Field label="Sort by">
            <Select value={sort} onChange={(e) => setSort(e.target.value)}>
              <option value="attention">Needs attention first</option>
              <option value="name">Name</option>
              <option value="cpu">CPU</option>
              <option value="memory">Memory</option>
              <option value="disk">Disk space used</option>
              <option value="temperature">Temperature</option>
            </Select>
          </Field>
          <div className="ml-auto flex gap-2">
            {can("jobs.execute") && <Button disabled={sel.size === 0} onClick={() => setDialog("helpers")}>Install recommended helpers{sel.size ? ` (${sel.size})` : ""}</Button>}
            {can("agents.enroll") && <Button disabled={sel.size === 0} onClick={() => setDialog("convert")}>Convert to agent{sel.size ? ` (${sel.size})` : ""}</Button>}
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Hosts ({category})</caption>
            <thead>
              <tr className="border-b border-line text-xs text-muted">
                <th className="px-2 py-1"><input type="checkbox" aria-label="Select all shown hosts" checked={allSel}
                  onChange={() => setSel((s) => { const n = new Set(s); rows.forEach((r) => (allSel ? n.delete(r.host_id) : n.add(r.host_id))); return n; })} /></th>
                <th className="px-2 py-1 font-medium">Host</th>
                <th className="px-2 py-1 font-medium">Health</th>
                <th className="px-2 py-1 font-medium">Monitoring</th>
                <th className="px-2 py-1 font-medium">CPU</th>
                <th className="px-2 py-1 font-medium">Memory</th>
                <th className="px-2 py-1 font-medium">Disk</th>
                <th className="px-2 py-1 font-medium">Temp.</th>
                <th className="px-2 py-1 font-medium">Services</th>
                <th className="px-2 py-1 font-medium">Storage devices</th>
                <th className="px-2 py-1 font-medium">Updated</th>
              </tr>
            </thead>
            <tbody>
              {rows.length === 0 && <tr><td colSpan={11} className="px-2 py-8 text-center text-muted">{d ? "No hosts in this view." : "Loading…"}</td></tr>}
              {rows.map((r) => (
                <tr key={r.host_id} className="border-b border-line last:border-0">
                  <td className="px-2 py-1.5"><input type="checkbox" aria-label={`Select ${r.name}`} checked={sel.has(r.host_id)} onChange={() => toggle(r.host_id)} /></td>
                  <td className="px-2 py-1.5"><Link className="text-accent" to={`/hosts/${r.host_id}/monitoring`}>{r.name}</Link></td>
                  <td className="px-2 py-1.5"><Worst r={r} /></td>
                  <td className="px-2 py-1.5 text-xs">
                    {r.mode === "continuous" ? "Agent · continuous" : r.mode === "polled" ? "SSH · polled" : "—"}
                    {r.reduced && <div className="text-warn">⚠ reduced fidelity</div>}
                    {r.helpers.length > 0 && <div className="text-muted">helpers would help: {r.helpers.join(", ")}</div>}
                  </td>
                  <td className="px-2 py-1.5 tabular-nums">{fmtPct(r.cpu)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{fmtPct(r.memory)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{fmtPct(r.disk)}{r.disk_mount && <span className="text-xs text-muted"> {r.disk_mount}</span>}</td>
                  <td className="px-2 py-1.5 tabular-nums">{fmtTemp(r.temperature)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{r.services === 0 ? "—" : r.failed_services ? <span className="text-danger">✕ {r.failed_services} failed</span> : `${r.services} ok`}</td>
                  <td className="px-2 py-1.5">{r.storage ? <StatusBadge status={deviceHealth[r.storage]?.status ?? "unknown"}>{deviceHealth[r.storage]?.word ?? r.storage}</StatusBadge> : "—"}</td>
                  <td className="px-2 py-1.5 text-xs text-muted">{r.updated_at ? fmtSince(r.updated_at) : "never"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {d && d.total > PAGE && (
          <div className="mt-3 flex items-center gap-2 text-sm">
            <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous</Button>
            <span className="text-muted">{offset + 1}–{Math.min(d.total, offset + PAGE)} of {d.total}</span>
            <Button size="sm" disabled={offset + PAGE >= d.total} onClick={() => setOffset(offset + PAGE)}>Next</Button>
          </div>
        )}
      </Card>
      <Card title="Active health issues" className="mt-4">
        {(d?.issues ?? []).length === 0 ? <p className="text-sm text-muted">None.</p> : (
          <ul className="divide-y divide-line">
            {d!.issues.map((i) => (
              <li key={i.id} className="flex flex-wrap items-start gap-2 py-2 text-sm">
                <SeverityBadge severity={i.severity} />
                <Link className="text-accent" to={`/hosts/${i.host_id}/monitoring`}>{i.host_name}</Link>
                <span className="min-w-0 flex-1">{i.title}{i.detail && <span className="block text-xs text-muted">{i.detail}</span>}</span>
                <span className="text-xs text-muted">{fmtSince(i.started_at)}</span>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-2 text-xs text-muted">Health issues are what M12 alerting will notify about. Nothing is sent anywhere yet.</p>
      </Card>
      {dialog === "helpers" && <HelperInstallDialog hostIds={[...sel]} onClose={() => setDialog("")} />}
      {dialog === "convert" && <ConvertDialog hostIds={[...sel]} onClose={() => setDialog("")} />}
    </>
  );
}
