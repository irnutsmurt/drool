import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { StatusBadge } from "@/components/monitoring/Status";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { useSaveHostSettings, useServiceHistory, useServices, type MonHostOverview, type MonService } from "@/components/monitoring/queries";

const filters = [
  { key: "all", label: "All" },
  { key: "running", label: "Running" },
  { key: "stopped", label: "Stopped" },
  { key: "failed", label: "Failed" },
  { key: "enabled", label: "Enabled" },
  { key: "disabled", label: "Disabled" },
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

export function ServicesTab({ id, o }: { id: string; o: MonHostOverview }) {
  const [filter, setFilter] = useState("all");
  const [all, setAll] = useState(false);
  const [hist, setHist] = useState("");
  const q = useServices(id, filter, all);
  const save = useSaveHostSettings(id);
  const { can } = useAuth();
  const v = q.data;
  const watch = (unit: string, on: boolean) => {
    const w = new Set(o.settings.watched_services);
    if (on) w.add(unit);
    else w.delete(unit);
    save.mutate({ ...o.settings, watched_services: [...w] });
  };
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
            <span className="ml-auto text-xs text-muted">{v.source === "ssh" ? "Polled over SSH" : "Reported by the agent"} · {fmtSince(v.updated_at)}</span>
          </div>
          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <caption className="sr-only">Services ({filter})</caption>
              <thead>
                <tr className="border-b border-line text-xs text-muted">
                  <th className="px-2 py-1 font-medium">Service</th>
                  <th className="px-2 py-1 font-medium">State</th>
                  <th className="px-2 py-1 font-medium">Enabled</th>
                  <th className="px-2 py-1 font-medium">Since</th>
                  <th className="px-2 py-1 font-medium" title="Automatic restarts since the unit was last started by hand">Restarts</th>
                  <th className="px-2 py-1 font-medium">Watched</th>
                  <th className="px-2 py-1 font-medium"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {v.units.length === 0 && <tr><td colSpan={7} className="px-2 py-6 text-center text-muted">No services match.</td></tr>}
                {v.units.map((u) => (
                  <tr key={u.name} className="border-b border-line last:border-0">
                    <td className="px-2 py-1.5">
                      <div className="font-mono text-xs">{u.name}</div>
                      {u.description && <div className="text-xs text-muted">{u.description}</div>}
                    </td>
                    <td className="px-2 py-1.5"><StateBadge u={u} /> <span className="text-xs text-muted">{u.active}/{u.sub}</span></td>
                    <td className="px-2 py-1.5 text-xs">{u.file_state || "—"}</td>
                    <td className="px-2 py-1.5 text-xs">{u.since ? fmtTime(u.since * 1000) : "—"}</td>
                    <td className="px-2 py-1.5 tabular-nums">{u.restarts ?? "—"}</td>
                    <td className="px-2 py-1.5">
                      <Checkbox label={<span className="sr-only">Watch {u.name}</span>} checked={u.watched}
                        disabled={!can("monitoring.manage") || save.isPending} onChange={(on) => watch(u.name, on)} />
                    </td>
                    <td className="px-2 py-1.5 text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => setHist(u.name)}>History</Button>
                      {can("jobs.execute") && (
                        <Link className="ml-1 text-xs text-accent" to={`/jobs/new?hosts=${id}&op=service&service=${encodeURIComponent(u.name)}&action=${u.state === "running" ? "restart" : "start"}`}>
                          {u.state === "running" ? "Restart…" : "Start…"}
                        </Link>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="text-xs text-muted">
            A failed service is a health issue. Watch a service to also be told when it stays stopped (for two minutes by default).
            Starting, stopping and restarting use the normal service job.
          </p>
        </div>
      )}
      {hist && <History id={id} unit={hist} onClose={() => setHist("")} />}
    </Card>
  );
}
