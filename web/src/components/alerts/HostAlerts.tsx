import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert as Notice } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { errorMessage } from "@/lib/format";
import { fmtSince } from "@/components/monitoring/format";
import { useAlerts, useAlertSummary, useEndMaintenance } from "./queries";
import { AlertSeverity, MaintenanceNote, NotificationBadge, untilText } from "./ui";
import { MaintenanceDialog } from "./MaintenanceDialog";
import { emptyText } from "@/components/ui/States";

/** "In maintenance until 18:00 (Kernel upgrade)" with an End button. */
export function HostMaintenanceBanner({ hostId }: { hostId: string }) {
  const { can } = useAuth();
  const sum = useAlertSummary(can("alerts.read"));
  const end = useEndMaintenance();
  const m = sum.data?.maintenance[hostId];
  if (!m) return null;
  return (
    <div className="mb-4">
      <Notice tone="warn">
        <span aria-hidden="true">⏸ </span>In maintenance {untilText(m.ends_at)}{m.reason ? ` — ${m.reason}` : ""}. Notifications are paused;
        monitoring and alerts continue.
        {can("maintenance.manage") && (
          <Button size="sm" className="ml-3" onClick={() => end.mutate(m.id)} disabled={end.isPending}>End maintenance</Button>
        )}
        {end.error && <span className="ml-2">{errorMessage(end.error)}</span>}
      </Notice>
    </div>
  );
}

/** A host's open alerts, and maintenance for it. */
export function HostAlertsCard({ hostId }: { hostId: string }) {
  const { can } = useAuth();
  const q = useAlerts({ view: "open", host_id: hostId });
  const [open, setOpen] = useState(false);
  const rows = q.data?.pages[0]?.items ?? [];
  return (
    <Card title="Alerts"
      actions={
        <div className="flex items-center gap-3">
          <Link className="text-sm text-accent" to={`/alerts?view=resolved&host_id=${hostId}`}>History</Link>
          {can("maintenance.manage") && <Button size="sm" onClick={() => setOpen(true)}>Start maintenance</Button>}
        </div>
      }>
      {q.error && <Notice>{errorMessage(q.error)}</Notice>}
      {rows.length === 0 ? (
        <p className="text-sm text-muted">{emptyText("alerts", q, "No open alerts.", true)}</p>
      ) : (
        <ul className="space-y-2 text-sm">
          {rows.map((a) => (
            <li key={a.id} className="flex flex-wrap items-center gap-2">
              <AlertSeverity severity={a.severity} />
              <Link className="text-accent" to={`/alerts/${a.id}`}>{a.title}</Link>
              <span className="text-xs text-muted">{fmtSince(a.observed_at ?? a.issue_started_at)}</span>
              {a.acknowledgement && <span className="text-xs text-muted">· acknowledged by {a.acknowledgement.by}</span>}
              <MaintenanceNote a={a} />
              <NotificationBadge a={a} />
            </li>
          ))}
        </ul>
      )}
      {open && <MaintenanceDialog hostIds={[hostId]} onClose={() => setOpen(false)} />}
    </Card>
  );
}
