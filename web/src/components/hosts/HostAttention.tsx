import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import type { Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { useHostAgent } from "@/components/agents/queries";
import { useAlertSummary } from "@/components/alerts/queries";
import { useDesiredHost } from "@/components/desired/queries";
import { useFwHost } from "@/components/firewall/queries";
import { Card } from "@/components/ui/Card";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { attentionReasons } from "./health";
import { InstallUpdatesDialog } from "@/components/jobs/InstallUpdatesDialog";

interface Attention {
  key: string;
  tone: "danger" | "warn";
  text: ReactNode;
  to?: string;
  action?: string;
  /** An action done here (a dialog) instead of a link. */
  onAct?: () => void;
}

/** What needs a person on this host, across areas, most serious first —
 * honest while the answers are still loading. */
export function HostAttention({ h, hideStatus }: { h: Host; hideStatus: boolean }) {
  const { can } = useAuth();
  const alerts = useAlertSummary(can("alerts.read"));
  const desired = useDesiredHost(h.id, can("desired.read"));
  const fw = useFwHost(h.id, can("firewall.read"));
  const agent = useHostAgent(h.id);
  const [installing, setInstalling] = useState(false);
  const items: Attention[] = [];
  const status = new Set(["host key changed", "access denied", "needs host key approval", "offline"]);

  for (const r of attentionReasons(h)) {
    if (hideStatus && status.has(r)) continue;
    if (/security update/.test(r)) {
      items.push({ key: r, tone: "warn", text: r, onAct: can("jobs.execute") ? () => setInstalling(true) : undefined, action: "Install updates…" });
    } else if (r === "reboot required") {
      items.push({ key: r, tone: "warn", text: "Reboot required", to: can("jobs.execute") ? `/jobs/new?hosts=${h.id}&op=reboot` : undefined, action: "Reboot" });
    } else if (/full$/.test(r)) {
      items.push({ key: r, tone: "danger", text: r, to: can("monitoring.read") ? `/hosts/${h.id}/monitoring?tab=metrics` : undefined, action: "View storage" });
    } else {
      items.push({ key: r, tone: status.has(r) ? "danger" : "warn", text: r });
    }
  }
  const a = alerts.data?.hosts[h.id];
  if (a && a.critical + a.warning > 0) {
    const open = a.critical + a.warning;
    items.push({ key: "alerts", tone: a.critical ? "danger" : "warn", to: `/alerts?host_id=${h.id}`, action: "View alerts",
      text: <>{open} open alert{open === 1 ? "" : "s"}{a.critical ? ` (${a.critical} critical)` : ""}{a.acknowledged ? `, ${a.acknowledged} acknowledged` : ""}</> });
  }
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
  const failed = [can("alerts.read") && alerts.isError && "alerts", can("desired.read") && desired.isError && "Desired State",
    can("firewall.read") && fw.isError && "the firewall", agent.isError && "the Shepherd"].filter(Boolean) as string[];
  if (failed.length > 0) items.push({ key: "unchecked", tone: "warn", text: <>Couldn&apos;t check {failed.join(", ")}: this list may be incomplete.</> });
  const loading = (can("alerts.read") && alerts.isPending) || (can("desired.read") && desired.isPending) || (can("firewall.read") && fw.isPending) || agent.isPending;
  items.sort((x, y) => (x.tone === y.tone ? 0 : x.tone === "danger" ? -1 : 1));
  if (items.length === 0) {
    return (
      <p className="rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="status" data-testid="host-attention">
        {loading ? <span className="text-muted">Checking what needs attention…</span> : <><span className="text-ok" aria-hidden="true">✓ </span>Nothing needs attention on this host right now.</>}
      </p>
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
            {x.onAct && <button type="button" className={plainLink} onClick={x.onAct}>{x.action}</button>}
          </li>
        ))}
      </ul>
      {installing && <InstallUpdatesDialog targets={{ hosts: [h.id] }} label={h.name} onClose={() => setInstalling(false)} />}
      {loading && <p className="mt-2 text-xs text-muted">Still checking some areas…</p>}
    </Card>
  );
}
