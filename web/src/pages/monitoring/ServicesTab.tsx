import { useState } from "react";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { StatusBadge } from "@/components/monitoring/Status";
import { isCustomized, MonitorToggle, ServiceActionsDialog, ServiceMonitorDialog } from "@/components/monitoring/ServiceActions";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { useServiceHistory, useServices, type MonHostOverview, type MonService } from "@/components/monitoring/queries";

const filters = [
  { key: "all", label: "All" },
  { key: "running", label: "Running" },
  { key: "stopped", label: "Stopped" },
  { key: "failed", label: "Failed" },
  { key: "enabled", label: "Enabled" },
  { key: "disabled", label: "Disabled" },
  { key: "monitored", label: "Monitored" },
];

function StateBadge({ u }: { u: MonService }) {
  switch (u.state) {
    case "running":
      return <StatusBadge status="ok">Running</StatusBadge>;
    case "failed":
      return <StatusBadge status="critical">Failed</StatusBadge>;
    case "unknown":
      return <StatusBadge status="unknown">Unknown</StatusBadge>;
  }
  return <StatusBadge status="info">Stopped</StatusBadge>;
}

function History({ id, unit, onClose }: { id: string; unit: string; onClose: () => void }) {
  const h = useServiceHistory(id, unit);
  return (
    <Dialog title={`${unit}: state changes`} onClose={onClose}>
      {h.error && <Alert>{errorMessage(h.error)}</Alert>}
      {!h.data ? <p className="text-sm text-muted">Loading…</p> : h.data.events.length === 0 ? (
        <p className="text-sm text-muted">No state changes recorded (DROOL stores changes, not repeated samples).</p>
      ) : (
        <ol className="space-y-1 text-sm">
          {h.data.events.map((e, i) => (
            <li key={i} className="flex flex-wrap gap-x-3">
              <span className="text-muted">{fmtTime(e.ts)}</span>
              <span>{e.from || "new"} → <strong>{e.to || "removed"}</strong>{e.result && e.result !== "success" ? ` (${e.result})` : ""}</span>
            </li>
          ))}
        </ol>
      )}
      <div className="mt-4 flex justify-end"><Button onClick={onClose} data-autofocus>Close</Button></div>
    </Dialog>
  );
}

/** Whether the unit's state alerts, and why (monitored or not, ignored). */
function HealthLine({ u }: { u: MonService }) {
  if (!u.health_note) return null;
  const tone = u.health === "problem" ? "text-danger" : "text-muted";
  return <div className={`text-xs ${tone}`} data-testid="health-note">{u.health_note}</div>;
}

/** Failed, but nobody monitors it: a fact of the inventory, not a health
 * problem (shown without the alarm colour). */
function StateCell({ u }: { u: MonService }) {
  // Failed, and nobody monitors it: the fact, in grey, never an alert's red.
  if (u.state === "failed" && !u.monitored) {
    return (
      <span className="inline-flex items-center gap-1 whitespace-nowrap rounded bg-surface-2 px-1.5 py-0.5 text-xs font-medium text-muted">
        <span aria-hidden="true">✕</span>Failed
      </span>
    );
  }
  return <StateBadge u={u} />;
}

export function ServicesTab({ id, o, hostName }: { id: string; o: MonHostOverview; hostName: string }) {
  const [filter, setFilter] = useState("all");
  const [all, setAll] = useState(false);
  const [hist, setHist] = useState("");
  const [acting, setActing] = useState<MonService | null>(null);
  const [custom, setCustom] = useState<MonService | null>(null);
  const q = useServices(id, filter, all);
  const { can } = useAuth();
  const v = q.data;
  void o;
  return (
    <Card title="Services">
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {v && !v.supported && <p className="text-sm text-muted">{v.note || "No service inventory for this host."}</p>}
      {v?.supported && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <div role="group" aria-label="Show services" className="flex flex-wrap gap-1">
              {filters.map((f) => (
                <button key={f.key} type="button" aria-pressed={filter === f.key} onClick={() => setFilter(f.key)}
                  className={cx("rounded-ui border px-2.5 py-1 text-sm", filter === f.key ? "border-accent bg-accent/15 font-medium" : "border-line hover:bg-surface-2")}>
                  {f.label} <span className="text-muted">{v.counts[f.key] ?? 0}</span>
                </button>
              ))}
            </div>
            <Checkbox label={`Show all units (advanced${v.hidden ? `, ${v.hidden} hidden` : ""})`} checked={all} onChange={setAll} />
            <span className="ml-auto text-xs text-muted">{v.source === "ssh" ? "Polled over SSH" : "Reported by the Shepherd"} · {fmtSince(v.updated_at)}</span>
          </div>
          <div className="overflow-x-auto">
            <table className="rtable w-full text-left text-sm">
              <caption className="sr-only">Services ({filter})</caption>
              <thead>
                <tr className="border-b border-line text-xs text-muted">
                  <th className="px-2 py-1 font-medium">Service</th>
                  <th className="px-2 py-1 font-medium">State</th>
                  <th className="px-2 py-1 font-medium">Enabled</th>
                  <th className="px-2 py-1 font-medium">Since</th>
                  <th className="px-2 py-1 font-medium" title="Automatic restarts since the unit was last started by hand">Restarts</th>
                  <th className="px-2 py-1 font-medium">Monitor</th>
                  <th className="px-2 py-1 font-medium"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {v.units.length === 0 && <tr><td colSpan={7} className="rt-empty px-2 py-6 text-center text-muted">No services match.</td></tr>}
                {v.units.map((u) => (
                  <tr key={u.name} className="border-b border-line last:border-0">
                    <td data-primary="" className="px-2 py-1.5">
                      <div className="font-mono text-xs">{u.name}</div>
                      {u.description && <div className="text-xs text-muted">{u.description}</div>}
                    </td>
                    <td data-label="State" className="px-2 py-1.5"><span><StateCell u={u} /> <span className="text-xs text-muted">{u.active}/{u.sub}</span></span><HealthLine u={u} /></td>
                    <td data-label="Enabled" className="px-2 py-1.5 text-xs">{u.file_state || "—"}</td>
                    <td data-label="Since" className="px-2 py-1.5 text-xs">{u.since ? fmtTime(u.since * 1000) : "—"}</td>
                    <td data-label="Restarts" className="px-2 py-1.5 tabular-nums">{u.restarts ?? "—"}</td>
                    <td data-label="Monitor" className="px-2 py-1.5"><MonitorToggle hostId={id} unit={u} /></td>
                    <td data-label="" className="px-2 py-1.5 text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => setHist(u.name)}>History</Button>
                      <Button size="sm" variant="ghost" onClick={() => setCustom(u)} aria-label={`Customize monitoring of ${u.name}`}>
                        {isCustomized(u) ? "⚙ Customized" : "Customize…"}
                      </Button>
                      {can("jobs.execute") && (
                        <Button size="sm" onClick={() => setActing(u)} aria-label={`Actions for ${u.name}`}>Actions…</Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="text-xs text-muted">
            DROOL lists every service and its state, but alerts only on the ones you <strong>Monitor</strong>: a monitored service that
            stops, fails or isn&apos;t installed is a health issue. Without it, a failed service is shown as failed and never alerts.
            <strong> Customize…</strong> sets more: expected <em>stopped</em> (running is the problem) or <em>ignored</em> on this host, and
            how long before it alerts. For a pack&apos;s hosts, set it on the pack&apos;s page. Actions run as an ordinary run, DROOL checks the
            result on the host, and they never change what is monitored.
          </p>
        </div>
      )}
      {hist && <History id={id} unit={hist} onClose={() => setHist("")} />}
      {acting && <ServiceActionsDialog hostId={id} hostName={hostName} unit={acting} onClose={() => setActing(null)} />}
      {custom && <ServiceMonitorDialog hostId={id} hostName={hostName} unit={custom} onClose={() => setCustom(null)} />}
    </Card>
  );
}
