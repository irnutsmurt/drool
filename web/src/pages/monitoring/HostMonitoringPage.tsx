import type { ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { SectionHeader } from "@/components/ui/SectionHeader";
import { TabPanel, Tabs } from "@/components/ui/Tabs";
import { errorMessage } from "@/lib/format";
import { MonitoringTiles, fidelityText } from "@/components/monitoring/HostMonitoringSummary";
import { HostMaintenanceBanner } from "@/components/alerts/HostAlerts";
import { useHostMonitoring, useRefreshHost, type MonHostOverview } from "@/components/monitoring/queries";
import { SeverityBadge } from "@/components/monitoring/Status";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { MetricsTab } from "./MetricsTab";
import { ServicesTab } from "./ServicesTab";
import { StorageTab } from "./StorageTab";
import { HardwareTab } from "./HardwareTab";
import { AgentTab } from "./AgentTab";
import { HostHealthPanel } from "@/components/health/HostHealth";
import { issueLink } from "@/components/monitoring/links";

const tabs = [
  // "Summary": the workspace already has an Overview (old links keep ?tab=overview).
  { key: "overview", label: "Summary" },
  { key: "metrics", label: "Metrics" },
  { key: "services", label: "Services" },
  { key: "storage", label: "Storage health" },
  { key: "hardware", label: "Sensors & GPU" },
  { key: "health", label: "Health checks" },
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
  const current = (tabs.find((t) => t.key === tab) ?? tabs[0]).key;
  return (
    <>
      <SectionHeader
        title="Monitoring"
        description={o?.has_data ? `${fidelityText(o)} · updated ${fmtSince(o.updated_at)}` : undefined}
        actions={
          <>
            {can("monitoring.manage") && (
              <Button onClick={() => refresh.mutate()} disabled={refresh.isPending}>{refresh.isPending ? "Requesting…" : "Collect now"}</Button>
            )}
          </>
        }
      />
      {can("alerts.read") && <HostMaintenanceBanner hostId={id} />}
      {mon.error && <Alert>{errorMessage(mon.error)}</Alert>}
      {o?.stale && <div className="mb-4"><Alert tone="warn">No monitoring data since {fmtTime(o.updated_at)}. The host may be unreachable; values are from before.</Alert></div>}
      {/* The section's own views, a level below the host's sections; on a
          phone the row scrolls sideways (local navigation, §6). */}
      <Tabs label="Monitoring views" id="mon" small className="-mx-1 mb-4 overflow-x-auto px-1" current={current}
        tabs={tabs.map((t) => ({ key: t.key, label: t.label }))} onSelect={setTab} />
      <TabPanel id="mon" current={current}>
        {!o ? (!mon.error && <p className="text-sm text-muted">Loading…</p>) : <Panel tab={current} id={id} o={o} hostName={host.data?.name ?? id} />}
      </TabPanel>
    </>
  );
}

function Panel({ tab, id, o, hostName }: { tab: string; id: string; o: MonHostOverview; hostName: string }): ReactNode {
  switch (tab) {
    case "metrics":
      return <MetricsTab id={id} o={o} />;
    case "services":
      return <ServicesTab id={id} o={o} hostName={hostName} />;
    case "storage":
      return <StorageTab id={id} o={o} />;
    case "hardware":
      return <HardwareTab o={o} />;
    case "health":
      return <HostHealthPanel hostId={id} />;
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
          No monitoring data yet. Hosts with a Shepherd (DROOL agent) report within a minute of connecting and are monitored
          continuously; SSH-only hosts are polled every few minutes.
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
                <Link className="basis-full text-xs text-accent sm:basis-auto" to={issueLink({ ...i, host_id: id }).to}>{issueLink({ ...i, host_id: id }).label}</Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <MetricsTab id={id} o={o} compact />
    </div>
  );
}
