import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Input, Select } from "@/components/ui/Field";
import { MenuButton, type MenuItem } from "@/components/ui/Menu";
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

/** How many services a page shows: a phone gets a short list it can
 * scroll, with the rest one tap away. */
const PAGE_PHONE = 20;
const PAGE_WIDE = 150;

export function ServicesTab({ id, o, hostName }: { id: string; o: MonHostOverview; hostName: string }) {
  const [params, setParams] = useSearchParams();
  // ?unit= (the navigation search): find that service, among all units if needed.
  const wanted = params.get("unit") ?? "";
  const [filter, setFilter] = useState("all");
  const [all, setAll] = useState(false);
  const [find, setFind] = useState(wanted);
  const [limit, setLimit] = useState(0);
  const [hist, setHist] = useState("");
  const [acting, setActing] = useState<MonService | null>(null);
  const [custom, setCustom] = useState<MonService | null>(null);
  const phone = useMediaQuery("(max-width: 767.98px)");
  const q = useServices(id, filter, all);
  const { can } = useAuth();
  const v = q.data;
  void o;
  // A unit the default list hides (inactive, "noise"): show all units once.
  const missing = !!wanted && !!v?.supported && !all && !v.units.some((u) => u.name === wanted);
  useEffect(() => {
    if (missing) setAll(true);
  }, [missing]);
  // Bring the unit the search opened into view, once it is listed.
  const scrolled = useRef(false);
  useEffect(() => {
    if (!wanted || scrolled.current || !v?.units.some((u) => u.name === wanted)) return;
    const el = [...document.querySelectorAll<HTMLElement>(`[data-unit="${CSS.escape(wanted)}"]`)].find((e) => e.offsetParent !== null);
    if (el) {
      scrolled.current = true;
      el.scrollIntoView({ block: "center" });
    }
  });
  const setFindText = (t: string) => {
    setFind(t);
    setLimit(0);
    if (wanted && t !== wanted) setParams((p) => {
      p.delete("unit");
      return p;
    }, { replace: true });
  };
  const words = find.toLowerCase().split(/\s+/).filter(Boolean);
  const units = (v?.units ?? []).filter((u) => words.every((w) => `${u.name} ${u.description}`.toLowerCase().includes(w)));
  const pageSize = limit || (phone ? PAGE_PHONE : PAGE_WIDE);
  const shown = units.slice(0, pageSize);

  const rowMenu = (u: MonService): MenuItem[] => [
    { label: "Customize monitoring…", hint: "Expected state, ignore, how long before it alerts", onSelect: () => setCustom(u) },
    { label: "State history", hint: "When it started, stopped or failed", onSelect: () => setHist(u.name) },
  ];
  const actions = (u: MonService) => (
    <span className="inline-flex items-center gap-1">
      {can("jobs.execute") && <Button size="sm" onClick={() => setActing(u)} aria-label={`Actions for ${u.name}`}>Actions…</Button>}
      <MenuButton label={<span aria-hidden="true">⋯</span>} ariaLabel={`More for ${u.name}`} ghost size="sm" items={rowMenu(u)} />
    </span>
  );
  const customized = (u: MonService) => isCustomized(u) && (
    <button type="button" className="text-xs text-muted underline underline-offset-2 hover:text-fg" onClick={() => setCustom(u)}
      aria-label={`Customize monitoring of ${u.name}`}>⚙ Customized</button>
  );
  return (
    <Card title="Services">
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {v && !v.supported && <p className="text-sm text-muted">{v.note || "No service inventory for this host."}</p>}
      {v?.supported && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <Input className="h-8 max-w-[16rem]" aria-label="Find a service" placeholder="Find a service…" value={find}
              onChange={(e) => setFindText(e.target.value)} />
            <div role="group" aria-label="Show services" className="hidden flex-wrap gap-1 md:flex">
              {filters.map((f) => (
                <button key={f.key} type="button" aria-pressed={filter === f.key} onClick={() => { setFilter(f.key); setLimit(0); }}
                  className={cx("rounded-ui border px-2.5 py-1 text-sm", filter === f.key ? "border-accent bg-accent/15 font-medium" : "border-line hover:bg-surface-2")}>
                  {f.label} <span className="text-muted">{v.counts[f.key] ?? 0}</span>
                </button>
              ))}
            </div>
            <Select className="h-8 max-w-[12rem] md:hidden" aria-label="Show services (phone)" value={filter} onChange={(e) => { setFilter(e.target.value); setLimit(0); }}>
              {filters.map((f) => <option key={f.key} value={f.key}>{f.label} ({v.counts[f.key] ?? 0})</option>)}
            </Select>
            <Checkbox label={`Show all units (advanced${v.hidden ? `, ${v.hidden} hidden` : ""})`} checked={all} onChange={(on) => { setAll(on); setLimit(0); }} />
            <span className="ml-auto text-xs text-muted">{v.source === "ssh" ? "Polled over SSH" : "Reported by the Shepherd"} · {fmtSince(v.updated_at)}</span>
          </div>
          <div className="hidden overflow-x-auto md:block">
            <table className="w-full text-left text-sm">
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
                {shown.length === 0 && <tr><td colSpan={7} className="px-2 py-6 text-center text-muted">No services match.</td></tr>}
                {shown.map((u) => (
                  <tr key={u.name} data-unit={u.name} className={cx("border-b border-line last:border-0", u.name === wanted && "bg-accent/10")}>
                    <td className="px-2 py-1.5">
                      <div className="font-mono text-xs">{u.name}</div>
                      {u.description && <div className="text-xs text-muted">{u.description}</div>}
                    </td>
                    <td className="px-2 py-1.5"><span><StateCell u={u} /> <span className="text-xs text-muted">{u.active}/{u.sub}</span></span><HealthLine u={u} /></td>
                    <td className="px-2 py-1.5 text-xs">{u.file_state || "—"}</td>
                    <td className="px-2 py-1.5 text-xs">{u.since ? fmtTime(u.since * 1000) : "—"}</td>
                    <td className="px-2 py-1.5 tabular-nums">{u.restarts ?? "—"}</td>
                    <td className="px-2 py-1.5"><span className="flex flex-col items-start gap-0.5"><MonitorToggle hostId={id} unit={u} />{customized(u)}</span></td>
                    <td className="whitespace-nowrap px-2 py-1.5 text-right">{actions(u)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {/* Phones: one compact entry per service, the essentials only. */}
          <ul className="divide-y divide-line md:hidden" aria-label="Services">
            {shown.length === 0 && <li className="py-6 text-center text-sm text-muted">No services match.</li>}
            {shown.map((u) => (
              <li key={u.name} data-unit={u.name} className={cx("py-2.5", u.name === wanted && "bg-accent/10")}>
                <div className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="break-all font-mono text-xs">{u.name}</div>
                    {u.description && <div className="truncate text-xs text-muted">{u.description}</div>}
                  </div>
                  <StateCell u={u} />
                </div>
                <HealthLine u={u} />
                <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
                  <MonitorToggle hostId={id} unit={u} />
                  {customized(u)}
                  <span className="ml-auto">{actions(u)}</span>
                </div>
              </li>
            ))}
          </ul>
          {units.length > shown.length && (
            <div className="flex flex-wrap items-center gap-3 text-xs text-muted">
              <span>Showing {shown.length} of {units.length}</span>
              <Button size="sm" onClick={() => setLimit(pageSize + (phone ? PAGE_PHONE : PAGE_WIDE))}>Show more</Button>
              <Button size="sm" variant="ghost" onClick={() => setLimit(units.length)}>Show all {units.length}</Button>
            </div>
          )}
          <p className="text-xs text-muted">
            DROOL lists every service and its state, but alerts only on the ones you <strong>Monitor</strong>: a monitored service that
            stops, fails or isn&apos;t installed is a health issue. Without it, a failed service is shown as failed and never alerts.
            <strong> Customize monitoring…</strong> (under ⋯) sets more: expected <em>stopped</em> (running is the problem) or <em>ignored</em> on
            this host, and how long before it alerts. For a pack&apos;s hosts, set it on the pack&apos;s page. Actions run as an ordinary run, DROOL
            checks the result on the host, and they never change what is monitored.
          </p>
        </div>
      )}
      {hist && <History id={id} unit={hist} onClose={() => setHist("")} />}
      {acting && <ServiceActionsDialog hostId={id} hostName={hostName} unit={acting} onClose={() => setActing(null)} />}
      {custom && <ServiceMonitorDialog hostId={id} hostName={hostName} unit={custom} onClose={() => setCustom(null)} />}
    </Card>
  );
}
