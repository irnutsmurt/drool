import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { useGroups, useHosts } from "@/api/queries";
import { Alert as Notice } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { useAlerts, useAlertSummary, useEndMaintenance, useMaintenance, type Alert, type AlertCategory, type AlertFilter } from "@/components/alerts/queries";
import { AlertSeverity, AlertStateBadge, categoryLabel, fmtDuration, MaintenanceNote, NotificationBadge, untilText } from "@/components/alerts/ui";
import { MaintenanceDialog } from "@/components/alerts/MaintenanceDialog";
import { useSchedules, minutesText } from "@/components/automation/queries";
import { ScheduleStateBadge } from "@/components/automation/ui";
import { plainLink } from "@/components/ui/links";
import { secondaryLink } from "@/components/ui/Button";

const tabs = [
  { key: "active", label: "Active" },
  { key: "acknowledged", label: "Acknowledged" },
  { key: "resolved", label: "Resolved / history" },
  { key: "maintenance", label: "Maintenance" },
] as const;

export function AlertsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get("view") ?? "active";
  const sum = useAlertSummary();
  const set = (k: string, v: string | undefined) => setParams((p) => {
    if (v) p.set(k, v);
    else p.delete(k);
    return p;
  }, { replace: true });
  const onKey = (e: React.KeyboardEvent, i: number) => {
    const n = e.key === "ArrowRight" ? i + 1 : e.key === "ArrowLeft" ? i - 1 : e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : -2;
    if (n === -2) return;
    e.preventDefault();
    const k = tabs[(n + tabs.length) % tabs.length]!.key;
    set("view", k);
    document.getElementById(`alerttab-${k}`)?.focus();
  };
  const s = sum.data;
  const count: Record<string, number | undefined> = { active: s?.active, acknowledged: s?.acknowledged, maintenance: s ? Object.keys(s.maintenance).length : undefined };
  return (
    <>
      <PageHeader title="Alerts"
        description={<>Problems monitoring confirmed and how people were told. An alert closes only when its condition recovers; acknowledging says someone is on it.
          What counts as a problem is set in <Link className={plainLink} to="/monitoring/settings">Monitoring → Health checks</Link>.</>}
        actions={can("notifications.read") && (
          <Link to="/settings/notifications" className={secondaryLink}>Notification settings</Link>
        )} />
      {s && (s.critical > 0 || s.warning > 0) && (
        <p className="mb-3 rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="status">
          <b>{s.active + s.acknowledged}</b> open: <span className="text-danger">{s.critical} critical</span>, <span className="text-warn">{s.warning} warning</span>
          {s.suppressed > 0 && <span className="text-muted"> · {s.suppressed} suppressed by maintenance</span>}
          {s.destinations && s.destinations.configured === 0 && <span className="text-muted"> · no notification destination yet — <Link className="text-accent underline" to="/settings/notifications">add one</Link></span>}
          {s.destinations && s.destinations.failing > 0 && <span className="text-warn"> · {s.destinations.failing} destination{s.destinations.failing === 1 ? "" : "s"} failing</span>}
        </p>
      )}
      {s?.destinations && s.destinations.configured === 0 && !(s.critical > 0 || s.warning > 0) && can("notifications.read") && (
        <p className="mb-3 rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="note">
          Nobody is notified about alerts yet. <Link className={plainLink} to="/settings/notifications">Configure notifications</Link> to send them to email, Discord, Slack or a webhook.
        </p>
      )}
      <div role="tablist" aria-label="Alerts" className="-mx-1 mb-4 flex gap-1 overflow-x-auto border-b border-line px-1">
        {tabs.map((t, i) => (
          <button key={t.key} id={`alerttab-${t.key}`} role="tab" type="button" aria-selected={tab === t.key} aria-controls="alertpanel"
            tabIndex={tab === t.key ? 0 : -1} onKeyDown={(e) => onKey(e, i)} onClick={() => set("view", t.key)}
            className={cx("-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm", tab === t.key ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")}>
            {t.label}{count[t.key] != null && <span className="ml-1.5 tabular-nums text-muted">{count[t.key]}</span>}
          </button>
        ))}
      </div>
      <div id="alertpanel" role="tabpanel" aria-labelledby={`alerttab-${tab}`}>
        {tab === "maintenance" ? <MaintenanceTab /> : <AlertList view={tab as "active"} params={params} set={set} />}
      </div>
    </>
  );
}

function AlertList({ view, params, set }: { view: "active" | "acknowledged" | "resolved"; params: URLSearchParams; set: (k: string, v: string | undefined) => void }) {
  const hosts = useHosts();
  const groups = useGroups();
  const day = (v: string | null, end = false) => (v ? new Date(v + (end ? "T23:59:59" : "T00:00:00")).getTime() : undefined);
  const f: AlertFilter = {
    view,
    severity: (params.get("severity") as "critical") || undefined,
    host_id: params.get("host_id") || undefined,
    group_id: params.get("group_id") || undefined,
    category: (params.get("category") as AlertCategory) || undefined,
    suppressed: params.get("suppressed") ? params.get("suppressed") === "true" : undefined,
    since: day(params.get("from")),
    until: day(params.get("to"), true),
  };
  const q = useAlerts(f);
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];
  const history = view === "resolved";
  const empty = { active: "No active alerts. Everything monitoring checks is fine, or acknowledged.", acknowledged: "No acknowledged alerts.", resolved: "No resolved alerts match." }[view];
  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <Field label="Severity">
          <Select value={f.severity ?? ""} onChange={(e) => set("severity", e.target.value)}>
            <option value="">All</option><option value="critical">Critical</option><option value="warning">Warning</option>
          </Select>
        </Field>
        <Field label="Host">
          <Select value={f.host_id ?? ""} onChange={(e) => set("host_id", e.target.value)}>
            <option value="">All hosts</option>
            {(hosts.data?.items ?? []).filter((h) => !h.jump_only).map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
          </Select>
        </Field>
        <Field label="Pack">
          <Select value={f.group_id ?? ""} onChange={(e) => set("group_id", e.target.value)}>
            <option value="">All packs</option>
            {(groups.data?.items ?? []).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </Select>
        </Field>
        <Field label="Category">
          <Select value={f.category ?? ""} onChange={(e) => set("category", e.target.value)}>
            <option value="">All</option>
            {Object.entries(categoryLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </Select>
        </Field>
        <Field label="Maintenance">
          <Select value={params.get("suppressed") ?? ""} onChange={(e) => set("suppressed", e.target.value)}>
            <option value="">Any</option><option value="true">{history ? "Was suppressed" : "Suppressed"}</option><option value="false">Not suppressed</option>
          </Select>
        </Field>
        {history && (
          <>
            <Field label="From"><Input type="date" value={params.get("from") ?? ""} onChange={(e) => set("from", e.target.value)} /></Field>
            <Field label="To"><Input type="date" value={params.get("to") ?? ""} onChange={(e) => set("to", e.target.value)} /></Field>
          </>
        )}
      </div>
      {q.error && <Notice>{errorMessage(q.error)}</Notice>}
      <div className="overflow-x-auto">
        <table className="rtable w-full text-left text-sm">
          <caption className="sr-only">{view} alerts</caption>
          <thead>
            <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
              <th className="px-3 py-2 font-medium">Severity</th>
              <th className="px-3 py-2 font-medium">Host</th>
              <th className="px-3 py-2 font-medium">Problem</th>
              <th className="px-3 py-2 font-medium">{history ? "Duration" : "Age"}</th>
              <th className="px-3 py-2 font-medium">State</th>
              <th className="px-3 py-2 font-medium">Notification</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr><td colSpan={6} className="rt-empty px-3 py-6 text-center text-muted">{q.isLoading ? "Loading…" : empty}</td></tr>
            ) : rows.map((a) => <AlertRow key={a.id} a={a} history={history} />)}
          </tbody>
        </table>
      </div>
      {q.hasNextPage && (
        <div className="mt-3 flex justify-center">
          <Button onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>{q.isFetchingNextPage ? "Loading…" : "Load more"}</Button>
        </div>
      )}
    </Card>
  );
}

function AlertRow({ a, history }: { a: Alert; history: boolean }) {
  return (
    <tr className="border-b border-line last:border-0 align-top">
      <td data-label="Severity" className="px-3 py-2"><AlertSeverity severity={a.severity} /></td>
      <td data-label="Host" className="px-3 py-2"><Link className="text-accent" to={`/hosts/${a.host_id}`}>{a.host_name}</Link></td>
      <td data-primary="" className="px-3 py-2">
        <Link className="font-medium hover:underline" to={`/alerts/${a.id}`}>{a.title}</Link>
        <div className="text-xs text-muted">{categoryLabel[a.category] ?? a.category}{a.value_text ? ` · ${a.value_text}` : ""}</div>
      </td>
      <td data-label={history ? "Duration" : "Age"} className="px-3 py-2 whitespace-nowrap tabular-nums" title={`Opened ${fmtTime(a.opened_at)}`}>
        {history ? fmtDuration(a.duration_ms) : fmtSince(a.observed_at ?? a.issue_started_at).replace(" ago", "")}
      </td>
      <td data-label="State" className="px-3 py-2">
        <div className="flex flex-col items-start gap-1">
          <AlertStateBadge a={a} />
          {a.acknowledgement && <span className="text-xs text-muted">by {a.acknowledgement.by}</span>}
          <MaintenanceNote a={a} />
        </div>
      </td>
      <td data-label="Notification" className="px-3 py-2"><NotificationBadge a={a} /></td>
    </tr>
  );
}

function MaintenanceTab() {
  const { can } = useAuth();
  const [state, setState] = useState<"active" | "ended">("active");
  const [open, setOpen] = useState(false);
  const m = useMaintenance(state);
  const end = useEndMaintenance();
  const rows = m.data?.maintenance ?? [];
  return (
    <>
    <Card title={state === "active" ? "Maintenance in progress" : "Ended maintenance"}
      actions={
        <div className="flex gap-2">
          <Button size="sm" variant="ghost" onClick={() => setState(state === "active" ? "ended" : "active")}>{state === "active" ? "Show ended" : "Show active"}</Button>
          {can("maintenance.manage") && <Button size="sm" variant="primary" onClick={() => setOpen(true)}>Start maintenance</Button>}
        </div>
      }>
      <p className="mb-3 text-sm text-muted">Maintenance pauses notifications only. Alerts still open and close, marked “suppressed by maintenance”. Scheduled jobs skip hosts under maintenance unless they belong to that window.</p>
      {m.error && <Notice>{errorMessage(m.error)}</Notice>}
      {end.error && <Notice>{errorMessage(end.error)}</Notice>}
      {rows.length === 0 ? (
        <p className="text-sm text-muted">{state === "active" ? "No maintenance in progress." : "No ended maintenance."}</p>
      ) : (
        <ul className="divide-y divide-line">
          {rows.map((x) => (
            <li key={x.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
              <div className="text-sm">
                <div className="font-medium">{x.reason || "No reason given"}</div>
                {x.schedule_id && <div className="text-xs"><Link className="text-accent" to={`/jobs/schedules/${x.schedule_id}`}>Started by a recurring window</Link></div>}
                <div className="text-muted">
                  {x.targets.map((t) => (t.kind === "group" ? `pack ${t.name}` : t.name)).join(", ")} · {x.host_count} host{x.host_count === 1 ? "" : "s"}
                </div>
                <div className="text-xs text-muted">
                  Started {fmtTime(x.started_at)} by {x.created_by || "unknown"} ·{" "}
                  {x.active ? untilText(x.ends_at) : `ended ${fmtTime(x.ended_at)}${x.ended_by ? ` by ${x.ended_by}` : " (expired)"}`}
                  {x.active && x.suppressing > 0 && ` · suppressing ${x.suppressing} alert${x.suppressing === 1 ? "" : "s"}`}
                </div>
              </div>
              {x.active && can("maintenance.manage") && (
                <Button size="sm" onClick={() => end.mutate(x.id)} disabled={end.isPending}>End now</Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {open && <MaintenanceDialog onClose={() => setOpen(false)} />}
    </Card>
    {can("schedules.read") && <RecurringWindows />}
    </>
  );
}

/** Recurring maintenance windows (schedules of kind maintenance): each
 * occurrence starts ordinary maintenance for its hosts and groups. */
function RecurringWindows() {
  const { can } = useAuth();
  const list = useSchedules("maintenance");
  const rows = list.data?.items ?? [];
  return (
    <Card title="Recurring windows" className="mt-4"
      actions={can("schedules.manage") && can("maintenance.manage") && (
        <Link to="/jobs/schedules/new?kind=maintenance" className="inline-flex h-7 items-center rounded-ui bg-accent px-2.5 text-xs font-medium text-accent-fg hover:opacity-90">New recurring window</Link>
      )}>
      <p className="mb-3 text-sm text-muted">
        Maintenance that starts by itself, for example every Sunday 02:00–04:00. Each window is ordinary maintenance;
        group membership is checked live while it lasts. A scheduled job can be tied to a window to run inside it.
      </p>
      {list.error && <Notice>{errorMessage(list.error)}</Notice>}
      {rows.length === 0 ? (
        <p className="text-sm text-muted">{list.isPending ? "Loading…" : "No recurring windows."}</p>
      ) : (
        <ul className="divide-y divide-line">
          {rows.map((w) => (
            <li key={w.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
              <div className="text-sm">
                <Link className="font-medium text-accent" to={`/jobs/schedules/${w.id}`}>{w.name}</Link>{" "}
                <ScheduleStateBadge s={w} />
                {w.active_maintenance_id && <span className="ml-1 text-xs text-ok">active now</span>}
                <div className="text-muted">{w.when} · for {minutesText(w.config.duration_minutes ?? 0)} · {w.targets_text}</div>
                {w.enabled && w.next_run_local && <div className="text-xs text-muted">Next: {w.next_run_local}{w.next_run_note ? ` (${w.next_run_note})` : ""}</div>}
                {w.attention && <div className="text-xs text-danger">{w.attention}</div>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
