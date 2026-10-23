import type { ReactNode } from "react";
import { Link } from "react-router";
import type { Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { useHostAgent } from "@/components/agents/queries";
import { useAlerts, useAlertSummary } from "@/components/alerts/queries";
import { AlertLine } from "@/components/alerts/AlertLine";
import { useDesiredHost } from "@/components/desired/queries";
import { useFwHost } from "@/components/firewall/queries";
import { Card } from "@/components/ui/Card";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { connectionReasons } from "./health";

interface Attention {
  key: string;
  tone: "danger" | "warn";
  text: ReactNode;
  to?: string;
  action?: string;
}

/** What is failing or waiting for a decision on this host, across areas,
 * most serious first, each with the next step: what keeps DROOL from the
 * host, its open alerts by their own words (with the service fix), Desired
 * State, the firewall and management. Honest while the answers are still
 * loading. Updates and reboots are in the status line below; acknowledged
 * and snoozed alerts are counted, not listed. */
export function HostAttention({ h, hideStatus }: { h: Host; hideStatus: boolean }) {
  const { can } = useAuth();
  const alerts = useAlertSummary(can("alerts.read"));
  const open = useAlerts({ view: "active", host_id: h.id, suppressed: false }, can("alerts.read"));
  const desired = useDesiredHost(h.id, can("desired.read"));
  const fw = useFwHost(h.id, can("firewall.read"));
  const agent = useHostAgent(h.id);
  const items: Attention[] = [];
  // hideStatus: the page already explains the connection status above.
  const status = new Set(["host key changed", "access denied", "needs host key approval", "offline"]);

  const reasons = connectionReasons(h);
  for (const r of reasons) {
    if (hideStatus && status.has(r)) continue;
    items.push({ key: r, tone: status.has(r) ? "danger" : "warn", text: r });
  }
  // "No monitoring data" says what the connection already says.
  const quiet = hideStatus || reasons.length > 0;
  for (const x of (open.data?.pages[0]?.items ?? []).filter((x) => !(quiet && x.check === "stale"))) {
    items.push({ key: "alert" + x.id, tone: x.severity === "critical" ? "danger" : "warn", text: <AlertLine a={x} /> });
  }
  const a = alerts.data?.hosts[h.id];
  const d = desired.data;
  if (d && d.counts.conflict + d.counts.drift + d.counts.failed > 0) {
    const parts = [d.counts.drift && `${d.counts.drift} drifted`, d.counts.conflict && `${d.counts.conflict} in conflict`, d.counts.failed && `${d.counts.failed} failed to restore`].filter(Boolean);
    items.push({ key: "desired", tone: d.counts.conflict || d.counts.failed ? "danger" : "warn", to: `/hosts/${h.id}/desired`, action: "View desired state",
      text: <>Desired State: {parts.join(", ")}</> });
  }
  const p = fw.data;
  if (p) {
    if (p.applied.drift) items.push({ key: "fw-drift", tone: "danger", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: "Firewall: DROOL's rules were changed outside DROOL (drift)" });
    if ((p.policy.conflicts ?? []).length > 0) items.push({ key: "fw-mgmt", tone: "danger", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: "Firewall: a policy would cut DROOL's management path (applying is refused)" });
    if (p.summary.status === "changes_pending") items.push({ key: "fw-pending", tone: "warn", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: "Firewall: policy changes not applied yet" });
    if (p.pending_transaction) items.push({ key: "fw-tx", tone: "warn", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: `Firewall transaction ${p.pending_transaction.state}` });
  }
  const m = agent.data?.management ?? h.management;
  const ident = agent.data?.identity;
  if (m.blocked) items.push({ key: "mgmt", tone: "danger", to: `/hosts/${h.id}/management`, action: "Management", text: <>DROOL can&apos;t manage this host right now{m.reason ? `: ${m.reason}` : ""}</> });
  else if (m.active === "none" && m.reason) items.push({ key: "mgmt", tone: "warn", to: `/hosts/${h.id}/management`, action: "Management", text: m.reason });
  else if (ident?.status === "quarantined") items.push({ key: "quarantine", tone: "danger", to: `/hosts/${h.id}/management`, action: "Management", text: "Shepherd identity conflict: management is paused" });

  // A source that couldn't be read is never "nothing to see" (DESIGN-STANDARD §8).
  const failed = [can("alerts.read") && (alerts.isError || open.isError) && "alerts", can("desired.read") && desired.isError && "Desired State",
    can("firewall.read") && fw.isError && "the firewall", agent.isError && "the Shepherd"].filter(Boolean) as string[];
  if (failed.length > 0) items.push({ key: "unchecked", tone: "warn", text: <>Couldn&apos;t check {failed.join(", ")}: this list may be incomplete.</> });
  const loading = (can("alerts.read") && (alerts.isPending || open.isPending)) || (can("desired.read") && desired.isPending) || (can("firewall.read") && fw.isPending) || agent.isPending;
  items.sort((x, y) => (x.tone === y.tone ? 0 : x.tone === "danger" ? -1 : 1));
  // Alerts someone took on, or that maintenance holds back: counted.
  const others = a && (a.acknowledged > 0 || a.suppressed > 0) && (
    <p className="text-xs text-muted">
      {a.acknowledged > 0 && <>{a.acknowledged} acknowledged alert{a.acknowledged === 1 ? "" : "s"}: someone is on it. </>}
      {a.suppressed > 0 && <>{a.suppressed} alert{a.suppressed === 1 ? "" : "s"} suppressed by maintenance. </>}
      <Link className={plainLink} to={`/alerts?view=open&host_id=${h.id}`}>Open alerts of {h.name}</Link>
    </p>
  );
  if (items.length === 0) {
    return (
      <div className="space-y-1 rounded-ui border border-line bg-surface px-3 py-2" data-testid="host-attention">
        <p className="text-sm" role="status">
          {loading ? <span className="text-muted">Checking what needs attention…</span> : <><span className="text-ok" aria-hidden="true">✓ </span>Nothing is failing or waiting for a decision on this host.</>}
        </p>
        {others}
      </div>
    );
  }
  return (
    <Card title={`Needs attention (${items.length})`}>
      <ul className="space-y-2 text-sm" data-testid="host-attention">
        {items.map((x) => (
          <li key={x.key} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
            <span className={cx("font-medium", x.tone === "danger" ? "text-danger" : "text-warn")}>
              <span aria-hidden="true">{x.tone === "danger" ? "✕ " : "⚠ "}</span>{x.text}
            </span>
            {x.to && <Link className={plainLink} to={x.to}>{x.action}</Link>}
          </li>
        ))}
      </ul>
      {others && <div className="mt-2">{others}</div>}
      {loading && <p className="mt-2 text-xs text-muted">Still checking some areas…</p>}
    </Card>
  );
}
