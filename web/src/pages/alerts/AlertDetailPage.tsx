import { Fragment, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert as Notice } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { secondaryLink } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { errorMessage } from "@/lib/format";
import { fmtTime } from "@/components/monitoring/format";
import { useAcknowledge, useAlert, type AlertDetail, type Delivery } from "@/components/alerts/queries";
import { AlertSeverity, AlertStateBadge, categoryLabel, checkLabel, clearedBy, fmtDuration, NotificationBadge, resolutionWord, untilText } from "@/components/alerts/ui";

const deliveryWord: Record<string, string> = { opening: "Opening", escalation: "Escalation", reminder: "Reminder", recovery: "Recovery", cleared: "Cleared (not a recovery)" };

export function AlertDetailPage() {
  const { id = "" } = useParams();
  const q = useAlert(id);
  const d = q.data;
  if (q.error) return <Notice>{errorMessage(q.error)}</Notice>;
  if (!d) return <p className="text-sm text-muted">Loading…</p>;
  const a = d.alert;
  return (
    <>
      <PageHeader
        crumbs={[{ label: "Alerts", to: "/alerts" }]}
        title={a.title}
        description={<><Link className="font-medium text-fg underline" to={`/hosts/${a.host_id}`}>{a.host_name}</Link> · {checkLabel[a.check] ?? a.check}{a.subject ? ` · ${a.subject}` : ""}</>}
        actions={
          <>
            <Link to={`/hosts/${a.host_id}`} className={secondaryLink}>View host</Link>
            <Link to={`/hosts/${a.host_id}/monitoring`} className={secondaryLink}>View monitoring</Link>
          </>
        }
      />
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <AlertSeverity severity={a.severity} />
        {a.peak_severity !== a.severity && <Badge>peaked at {a.peak_severity}</Badge>}
        <AlertStateBadge a={a} />
        <NotificationBadge a={a} />
        {a.suppressed && <Badge>⏸ Suppressed by maintenance{a.suppressed_by?.reason ? `: ${a.suppressed_by.reason}` : ""} ({untilText(a.suppressed_by?.ends_at)})</Badge>}
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-2">
          <Card title="Timeline">
            <ol className="space-y-2 text-sm">
              {d.timeline.map((t, i) => (
                <li key={i} className="flex gap-3">
                  <time className="w-40 shrink-0 tabular-nums text-muted" dateTime={new Date(t.ts).toISOString()}>{fmtTime(t.ts)}</time>
                  <span>{t.text}</span>
                </li>
              ))}
            </ol>
            {a.state !== "resolved" && <p className="mt-3 text-xs text-muted">The alert resolves on its own when monitoring sees the condition recover.</p>}
          </Card>
          <Deliveries d={d} />
        </div>
        <div className="space-y-4">
          <Facts d={d} />
          <Acknowledge d={d} />
          {d.occurrences.length > 0 && (
            <Card title="Other occurrences">
              <ul className="space-y-1 text-sm">
                {d.occurrences.map((o) => (
                  <li key={o.id}>
                    <Link className="text-accent" to={`/alerts/${o.id}`}>{fmtTime(o.opened_at)}</Link>{" "}
                    <span className="text-muted">{o.state}{o.resolved_at ? `, lasted ${fmtDuration(o.resolved_at - o.opened_at)}` : ""}</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      </div>
    </>
  );
}

function Facts({ d }: { d: AlertDetail }) {
  const a = d.alert;
  const rows: [string, ReactNode][] = [
    ["Category", categoryLabel[a.category] ?? a.category],
    ["Current value", a.value_text || "—"],
    ["First observed", a.observed_at ? fmtTime(a.observed_at) : "Not known (raised when detected)"],
    ["Alert opened", fmtTime(a.opened_at)],
    [a.state === "resolved" ? "Lasted" : "Open for", fmtDuration(a.duration_ms)],
  ];
  if (a.resolved_at) rows.push([clearedBy(a) ? resolutionWord[a.resolution!]! : "Recovered", fmtTime(a.resolved_at)]);
  if (a.resolved_at && clearedBy(a) && a.resolution_note) rows.push(["Why", a.resolution_note]);
  return (
    <Card title="Details">
      {a.detail && <p className="mb-3 text-sm">{a.detail}</p>}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        {rows.map(([k, v]) => <Fragment key={k}><dt className="text-muted">{k}</dt><dd>{v}</dd></Fragment>)}
      </dl>
      {d.issue && (
        <p className="mt-3 text-xs text-muted">
          Source: health issue {d.issue.state === "active" ? "still active" : `resolved ${fmtTime(d.issue.resolved_at)}`} ·{" "}
          <Link className="text-accent" to={`/hosts/${a.host_id}/monitoring`}>monitoring</Link>
        </p>
      )}
    </Card>
  );
}

function Acknowledge({ d }: { d: AlertDetail }) {
  const { can } = useAuth();
  const a = d.alert;
  const ack = useAcknowledge(a.id);
  const [note, setNote] = useState("");
  if (a.acknowledgement) {
    return (
      <Card title="Acknowledgement">
        <p className="text-sm">Acknowledged by <b>{a.acknowledgement.by}</b> {fmtTime(a.acknowledgement.at)}</p>
        {a.acknowledgement.note && <blockquote className="mt-2 border-l-2 border-line pl-3 text-sm">{a.acknowledgement.note}</blockquote>}
      </Card>
    );
  }
  if (a.state !== "active" || !can("alerts.acknowledge")) return null;
  return (
    <Card title="Acknowledge">
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); ack.mutate(note); }}>
        <p className="text-xs text-muted">Tells others someone is on it and stops reminders. It doesn't close the alert: that happens when the condition recovers.</p>
        <Field label="Note (optional)">
          <textarea className="min-h-16 w-full rounded-ui border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent focus:ring-2 focus:ring-accent/25"
            maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. Replacing the failed disk this evening" />
        </Field>
        {ack.error && <Notice>{errorMessage(ack.error)}</Notice>}
        <Button type="submit" variant="primary" disabled={ack.isPending}>{ack.isPending ? "Acknowledging…" : "Acknowledge"}</Button>
      </form>
    </Card>
  );
}

function deliveryStatus(x: Delivery) {
  switch (x.status) {
    case "delivered":
      return <Badge tone="ok">✓ Delivered</Badge>;
    case "failed":
      return <Badge tone="danger">✕ Failed</Badge>;
    case "cancelled":
      return <Badge>– Not sent</Badge>;
    case "sending":
      return <Badge>⧗ Sending</Badge>;
  }
  return x.attempts > 0 ? <Badge tone="warn">⚠ Retrying</Badge> : <Badge>⧗ Queued</Badge>;
}

function Deliveries({ d }: { d: AlertDetail }) {
  if (d.deliveries.length === 0) {
    return <Card title="Notifications"><p className="text-sm text-muted">{d.alert.ever_suppressed ? "Suppressed by maintenance: nothing was sent." : "No destination matched this alert."}</p></Card>;
  }
  return (
    <Card title="Notifications">
      <ul className="divide-y divide-line">
        {d.deliveries.map((x) => (
          <li key={x.id} className="py-2 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              {deliveryStatus(x)}
              <span className="font-medium">{x.destination}</span>
              <span className="text-muted">{deliveryWord[x.kind] ?? x.kind} · {x.attempts} attempt{x.attempts === 1 ? "" : "s"}</span>
              {x.ambiguous && <Badge tone="warn">may have arrived twice</Badge>}
              {x.next_attempt_at && <span className="text-xs text-muted">next try {fmtTime(x.next_attempt_at)}</span>}
            </div>
            {x.last_error && <div className="mt-1 text-xs text-muted">{x.last_error}</div>}
            {x.attempt_log && x.attempt_log.length > 1 && (
              <details className="mt-1 text-xs">
                <summary className="cursor-pointer text-muted">Attempts</summary>
                <ol className="mt-1 space-y-0.5">
                  {x.attempt_log.map((t) => (
                    <li key={t.n}>{t.n}. {fmtTime(t.started_at)} — {t.outcome}{t.status_code ? ` (HTTP/SMTP ${t.status_code})` : ""}{t.error ? `: ${t.error}` : ""}</li>
                  ))}
                </ol>
              </details>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}
