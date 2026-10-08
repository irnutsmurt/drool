import type { ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { MonitoringTiles, fidelityText } from "@/components/monitoring/HostMonitoringCard";
import { useHostMonitoring, useRefreshHost, type MonHostOverview } from "@/components/monitoring/queries";
import { SeverityBadge } from "@/components/monitoring/Status";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { MetricsTab } from "./MetricsTab";
import { ServicesTab } from "./ServicesTab";
import { StorageTab } from "./StorageTab";
import { HardwareTab } from "./HardwareTab";
import { AgentTab } from "./AgentTab";

const tabs = [
  { key: "overview", label: "Overview" },
  { key: "metrics", label: "Metrics" },
  { key: "services", label: "Services" },
  { key: "storage", label: "Storage health" },
  { key: "hardware", label: "Sensors & GPU" },
  { key: "agent", label: "Collection & helpers" },
] as const;

export function HostMonitoringPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") ?? "overview";
  const host = useHost(id);
  const mon = useHostMonitoring(id);
  const refresh = useRefreshHost(id);
  const { can } = useAuth();
  const o = mon.data;
  const setTab = (t: string) => setParams((p) => {
    p.set("tab", t);
    return p;
  }, { replace: true });
  const onKey = (e: React.KeyboardEvent, i: number) => {
    const n = e.key === "ArrowRight" ? i + 1 : e.key === "ArrowLeft" ? i - 1 : e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : -2;
    if (n === -2) return;
    e.preventDefault();
    const k = tabs[(n + tabs.length) % tabs.length]!.key;
    setTab(k);
    document.getElementById(`montab-${k}`)?.focus();
  };
  return (
    <>
      <PageHeader
        title={`${host.data?.name ?? "Host"}: monitoring`}
        description={o?.has_data ? `${fidelityText(o)} · updated ${fmtSince(o.updated_at)}` : undefined}
        actions={
          <>
            <Link to={`/hosts/${id}`} className="inline-flex h-9 items-center rounded-ui border border-line bg-surface px-3.5 text-sm font-medium text-fg hover:bg-surface-2">Host page</Link>
            {can("monitoring.manage") && (
              <Button onClick={() => refresh.mutate()} disabled={refresh.isPending}>{refresh.isPending ? "Requesting…" : "Collect now"}</Button>
            )}
          </>
        }
      />
      {mon.error && <Alert>{errorMessage(mon.error)}</Alert>}
      {o?.stale && <div className="mb-4"><Alert tone="warn">No monitoring data since {fmtTime(o.updated_at)}. The host may be unreachable; values are from before.</Alert></div>}
      <div role="tablist" aria-label="Monitoring" className="mb-4 flex flex-wrap gap-1 border-b border-line">
        {tabs.map((t, i) => (
          <button key={t.key} id={`montab-${t.key}`} role="tab" type="button" aria-selected={tab === t.key} aria-controls="monpanel"
            tabIndex={tab === t.key ? 0 : -1} onKeyDown={(e) => onKey(e, i)} onClick={() => setTab(t.key)}
            className={cx("-mb-px border-b-2 px-3 py-2 text-sm", tab === t.key ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")}>
            {t.label}
          </button>
        ))}
      </div>
      <div id="monpanel" role="tabpanel" aria-labelledby={`montab-${tab}`}>
        {!o ? <p className="text-sm text-muted">Loading…</p> : <Panel tab={tab} id={id} o={o} />}
      </div>
    </>
  );
}

function Panel({ tab, id, o }: { tab: string; id: string; o: MonHostOverview }): ReactNode {
  switch (tab) {
    case "metrics":
      return <MetricsTab id={id} o={o} />;
    case "services":
      return <ServicesTab id={id} o={o} />;
    case "storage":
      return <StorageTab id={id} o={o} />;
    case "hardware":
      return <HardwareTab o={o} />;
    case "agent":
      return <AgentTab id={id} o={o} />;
  }
  return <OverviewTab id={id} o={o} />;
}

function OverviewTab({ id, o }: { id: string; o: MonHostOverview }) {
  if (!o.has_data) {
    return (
      <Card>
        <p className="text-sm text-muted">
          No monitoring data yet. Agent hosts report within a minute of connecting; SSH hosts on their next poll. Hosts with the DROOL
          agent are monitored continuously; SSH-only hosts are polled every few minutes.
        </p>
      </Card>
    );
  }
  return (
    <div className="space-y-4">
      <MonitoringTiles hostId={id} o={o} />
      <Card title="Active health issues">
        {o.issues.length === 0 ? (
          <p className="text-sm text-muted">None. DROOL checks sustained CPU and memory pressure, disk space, service failures, interfaces, temperatures (against the hardware's own limits) and storage health.</p>
        ) : (
          <ul className="divide-y divide-line">
            {o.issues.map((i) => (
              <li key={i.id} className="flex flex-wrap items-start gap-2 py-2 text-sm">
                <SeverityBadge severity={i.severity} />
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{i.title}</div>
                  {i.detail && <div className="text-xs text-muted">{i.detail}</div>}
                </div>
                <span className="text-xs text-muted">since {fmtTime(i.started_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <MetricsTab id={id} o={o} compact />
    </div>
  );
}
