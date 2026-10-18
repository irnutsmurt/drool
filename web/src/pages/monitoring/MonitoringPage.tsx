import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button, primaryLink } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Card } from "@/components/ui/Card";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { useFleet, type MonFleetHost } from "@/components/monitoring/queries";
import { deviceHealth, HealthBadge, SeverityBadge, StatusBadge } from "@/components/monitoring/Status";
import { fmtPct, fmtSince, fmtTemp } from "@/components/monitoring/format";
import { HelperInstallDialog } from "@/components/monitoring/HelperInstall";
import { ConvertDialog } from "@/components/monitoring/ConvertDialog";
import { useAlertSummary } from "@/components/alerts/queries";
import { AlertCounts } from "@/components/alerts/ui";
import { MonitoringTabs } from "./MonitoringTabs";
import { emptyText } from "@/components/ui/States";

const categories = [
  { key: "unhealthy", label: "Needs attention", group: "main" },
  { key: "stale", label: "Unreachable / stale", group: "main" },
  { key: "cpu", label: "CPU", group: "health" },
  { key: "memory", label: "Memory", group: "health" },
  { key: "disk", label: "Low disk space", group: "health" },
  { key: "storage", label: "Storage health", group: "health" },
  { key: "services", label: "Service failures", group: "health" },
  { key: "network", label: "Interfaces", group: "health" },
  { key: "temperature", label: "Temperature", group: "health" },
  { key: "fidelity", label: "Reduced fidelity", group: "collection" },
  { key: "helpers", label: "Missing helpers", group: "collection" },
  { key: "no_data", label: "No data yet", group: "collection" },
  { key: "all", label: "All hosts", group: "main" },
] as const;

const PAGE = 100;

function Worst({ r }: { r: MonFleetHost }) {
  return <HealthBadge hasData={r.source !== ""} stale={r.stale} issues={r.issues} worst={r.worst} />;
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
  const alerts = useAlertSummary(can("alerts.read"));
  const as = alerts.data;
  const d = f.data;
  const setCat = (c: string) => {
    setOffset(0);
    setParams((p) => { p.set("category", c); return p; }, { replace: true });
  };
  const toggle = (id: string) => setSel((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const rows = d?.rows ?? [];
  const allSel = rows.length > 0 && rows.every((r) => sel.has(r.host_id));
  if (d && d.hosts === 0) {
    return (
      <>
        <PageHeader title="Monitoring" description="Health, metrics, services and hardware of every host." />
        <MonitoringTabs />
        <EmptyState title="Nothing to monitor yet"
          body="Monitoring shows every host's health — CPU, memory, disks, services, temperatures and storage health — and turns confirmed problems into alerts. Add a host and it appears here within minutes."
          action={can("hosts.write") ? <Link className={primaryLink} to="/hosts/new">Add host</Link> : undefined} />
      </>
    );
  }
  return (
    <>
      <PageHeader title="Monitoring"
        description={d ? `${d.monitored} of ${d.hosts} hosts reporting: ${d.agent} with a Shepherd (agent, continuous), ${d.ssh} over SSH (polled)` : "Health, metrics, services and hardware of every host."} />
      <MonitoringTabs />
      {f.error && <Alert>{errorMessage(f.error)}</Alert>}

      {as && (
        <p className="mb-3 rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="status">
          <Link className="font-medium text-accent" to="/alerts">Alerts</Link>:{" "}
          {as.active + as.acknowledged === 0 ? "none open" : <><AlertCounts critical={as.critical} warning={as.warning} /> {as.acknowledged > 0 && <span className="text-muted">({as.acknowledged} acknowledged)</span>}</>}
          {Object.keys(as.maintenance).length > 0 && <span className="text-muted"> · {Object.keys(as.maintenance).length} host{Object.keys(as.maintenance).length === 1 ? "" : "s"} in maintenance</span>}
        </p>
      )}
      <FleetFilters category={category} counts={d?.counts} hosts={d?.hosts} setCat={setCat} q={q}
        reset={() => { setQ(""); setCat("all"); }} />
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
          {(can("jobs.execute") || can("agents.enroll")) && (
            <div className="ml-auto flex flex-wrap items-center gap-2" data-testid="fleet-bulk">
              {sel.size === 0 ? <span className="text-xs text-muted">Select hosts to install monitoring helpers or convert them to Shepherds.</span> : (
                <>
                  <span className="text-sm">{sel.size} selected</span>
                  {can("jobs.execute") && <Button size="sm" onClick={() => setDialog("helpers")}>Install recommended helpers</Button>}
                  {can("agents.enroll") && <Button size="sm" onClick={() => setDialog("convert")}>Convert to Shepherd</Button>}
                  <Button size="sm" variant="ghost" onClick={() => setSel(new Set())}>Clear</Button>
                </>
              )}
            </div>
          )}
        </div>
        <div className="overflow-x-auto">
          <table className="rtable w-full text-left text-sm">
            <caption className="sr-only">Hosts ({category})</caption>
            <thead>
              <tr className="border-b border-line text-xs text-muted">
                <th className="px-2 py-1">
                  <label className="inline-flex items-center gap-2">
                    <input type="checkbox" aria-label="Select all shown hosts" checked={allSel}
                      onChange={() => setSel((s) => { const n = new Set(s); rows.forEach((r) => (allSel ? n.delete(r.host_id) : n.add(r.host_id))); return n; })} />
                    {/* In cards this cell stays above them (index.css). */}
                    <span aria-hidden="true" className="rt-card-only text-xs font-normal normal-case text-muted">Select all shown</span>
                  </label>
                </th>
                <th className="px-2 py-1 font-medium">Host</th>
                <th className="px-2 py-1 font-medium">Health</th>
                {as && <th className="px-2 py-1 font-medium">Alerts</th>}
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
              {rows.length === 0 && <tr><td colSpan={as ? 12 : 11} className="rt-empty px-2 py-8 text-center text-muted">{d ? "No hosts in this view." : "Loading…"}</td></tr>}
              {rows.map((r) => (
                <tr key={r.host_id} className="border-b border-line last:border-0">
                  <td data-label="" className="px-2 py-1.5">
                    <label className="inline-flex items-center gap-2">
                      <input type="checkbox" aria-label={`Select ${r.name}`} checked={sel.has(r.host_id)} onChange={() => toggle(r.host_id)} />
                      {/* In a card (a phone, a narrow window) the column header is gone. */}
                      <span aria-hidden="true" className="rt-card-only text-xs text-muted">Select</span>
                    </label>
                  </td>
                  <td data-primary="" className="px-2 py-1.5"><Link className="text-accent" to={`/hosts/${r.host_id}/monitoring`}>{r.name}</Link></td>
                  <td data-label="Health" className="px-2 py-1.5"><Worst r={r} /></td>
                  {as && (
                    <td data-label="Alerts" className="px-2 py-1.5">
                      <AlertCounts critical={as.hosts[r.host_id]?.critical ?? 0} warning={as.hosts[r.host_id]?.warning ?? 0} hostId={r.host_id} />
                      {as.maintenance[r.host_id] && <div className="text-xs text-muted" title={as.maintenance[r.host_id]!.reason}>⏸ maintenance</div>}
                    </td>
                  )}
                  <td data-label="Monitoring" className="px-2 py-1.5 text-xs">
                    {r.mode === "continuous" ? "Shepherd · continuous" : r.mode === "polled" ? "SSH · polled" : "—"}
                    {r.reduced && <div className="text-warn">⚠ reduced fidelity</div>}
                    {r.helpers.length > 0 && <div className="text-muted">helpers would help: {r.helpers.join(", ")}</div>}
                  </td>
                  <td data-label="CPU" className="px-2 py-1.5 tabular-nums">{fmtPct(r.cpu)}</td>
                  <td data-label="Memory" className="px-2 py-1.5 tabular-nums">{fmtPct(r.memory)}</td>
                  <td data-label="Disk" className="px-2 py-1.5 tabular-nums">{fmtPct(r.disk)}{r.disk_mount && <span className="text-xs text-muted"> {r.disk_mount}</span>}</td>
                  <td data-label="Temperature" className="px-2 py-1.5 tabular-nums">{fmtTemp(r.temperature)}</td>
                  <td data-label="Services" className="px-2 py-1.5 tabular-nums">
                    {r.services === 0 ? "—" : r.service_issues ? <span className="text-danger">✕ {r.service_issues} monitored {r.service_issues === 1 ? "problem" : "problems"}</span> : `${r.services} listed`}
                    {r.failed_services > 0 && !r.service_issues && <div className="text-xs text-muted">{r.failed_services} failed, not alerting</div>}
                  </td>
                  <td data-label="Storage devices" className="px-2 py-1.5">{r.storage ? <StatusBadge status={deviceHealth[r.storage]?.status ?? "unknown"}>{deviceHealth[r.storage]?.word ?? r.storage}</StatusBadge> : "—"}</td>
                  <td data-label="Updated" className="px-2 py-1.5 text-xs text-muted">{r.updated_at ? fmtSince(r.updated_at) : "never"}</td>
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
        {(d?.issues ?? []).length === 0 ? <p className="text-sm text-muted">{emptyText("health issues", f, "None.")}</p> : (
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
        <p className="mt-2 text-xs text-muted">
          What counts as an issue is set under <Link className="text-accent" to="/monitoring/settings">Health checks and settings</Link>.
          Warning and Critical health issues become <Link className="text-accent" to="/alerts">alerts</Link>, which notify the destinations set up under{" "}
          <Link className="text-accent" to="/settings/notifications">Settings → Notifications</Link>. Informational issues are shown here only.
        </p>
      </Card>
      {dialog === "helpers" && <HelperInstallDialog hostIds={[...sel]} onClose={() => setDialog("")} />}
      {dialog === "convert" && <ConvertDialog hostIds={[...sel]} onClose={() => setDialog("")} />}
    </>
  );
}

/** Which hosts to show: the views people use most (everything, what needs
 * attention, what can't be reached) always in sight, and the health or
 * collection problems that some hosts have right now; the rest under "More
 * filters". The active one always shows, with a way back to every host. */
function FleetFilters({ category, counts, hosts, setCat, q, reset }: {
  category: string;
  counts?: Record<string, number>;
  hosts?: number;
  setCat: (c: string) => void;
  q: string;
  reset: () => void;
}) {
  const [more, setMore] = useState(false);
  // Not loaded yet: no count, rather than a zero that reads as "none".
  const n = (key: string) => (key === "all" ? hosts : counts?.[key]);
  const main = categories.filter((c) => c.group === "main").sort((a, b) => (a.key === "all" ? -1 : b.key === "all" ? 1 : 0));
  const others = categories.filter((c) => c.group !== "main");
  const visible = [...main, ...others.filter((c) => (n(c.key) ?? 0) > 0 || c.key === category)];
  const chip = (c: (typeof categories)[number]) => (
    <button key={c.key} type="button" aria-pressed={category === c.key} onClick={() => setCat(c.key)}
      className={cx("rounded-ui border px-3 py-1.5 text-sm", category === c.key ? "border-accent bg-accent/15 font-medium" : "border-line hover:bg-surface-2")}>
      {c.label} <span className={cx("ml-1 tabular-nums", n(c.key) ? "font-semibold" : "text-muted")}>{n(c.key) ?? "…"}</span>
    </button>
  );
  return (
    <div className="mb-4 space-y-2" data-testid="fleet-filters">
      <div role="group" aria-label="Show hosts" className="flex flex-wrap items-center gap-2">
        {visible.map(chip)}
        <button type="button" aria-expanded={more} aria-controls="fleet-more-filters" onClick={() => setMore((v) => !v)}
          className="rounded-ui px-2 py-1.5 text-sm text-muted underline underline-offset-2 hover:text-fg pointer-coarse:min-h-11">
          {more ? "Fewer filters" : `More filters (${others.length})`}
        </button>
        {(category !== "all" || q) && (
          <button type="button" onClick={reset} className="rounded-ui px-2 py-1.5 text-sm text-muted underline underline-offset-2 hover:text-fg pointer-coarse:min-h-11">Reset filters</button>
        )}
      </div>
      {more && (
        <div id="fleet-more-filters" className="grid gap-3 rounded-ui border border-line bg-surface p-3 sm:grid-cols-2">
          <div>
            <div className="mb-1.5 text-xs font-semibold uppercase tracking-wider text-muted">Health problems</div>
            <div role="group" aria-label="Hosts with a health problem" className="flex flex-wrap gap-2">{others.filter((c) => c.group === "health").map(chip)}</div>
          </div>
          <div>
            <div className="mb-1.5 text-xs font-semibold uppercase tracking-wider text-muted">How hosts are monitored</div>
            <div role="group" aria-label="Hosts by how they are monitored" className="flex flex-wrap gap-2">{others.filter((c) => c.group === "collection").map(chip)}</div>
          </div>
        </div>
      )}
    </div>
  );
}
