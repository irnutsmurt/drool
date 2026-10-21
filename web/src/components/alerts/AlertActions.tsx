import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert as Notice } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Inspector } from "@/components/ui/Inspector";
import { Field } from "@/components/ui/Field";
import { cx } from "@/components/ui/cx";
import { plainLink, touchLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { MaintenanceForm } from "./MaintenanceDialog";
import { ServiceActionsDialog, useServiceFix } from "@/components/monitoring/ServiceActions";
import { AlertSeverity, AlertStateBadge, MaintenanceNote, NotificationBadge, alertMonitoringLink, categoryLabel } from "./ui";
import { alertKeys, useAcknowledge, type Alert } from "./queries";

type IgnoreOption = Schemas["AlertIgnoreOptions"]["options"][number];

/** Can the signed-in user do anything from an alert's Actions…? */
function useCanAct(a: Alert) {
  const { can } = useAuth();
  return { ack: a.state === "active" && can("alerts.acknowledge"), snooze: can("maintenance.manage"), ignore: can("monitoring.manage") };
}

/** Actions… on an open alert's row (dogfooding correction batch): the
 * common ways to deal with it without opening its page. Acknowledge, Snooze
 * (maintenance for its host) and Ignore (its health settings, so the
 * condition stops being a problem), in the alert's inspector. Closed alerts
 * have none. The list shows the inspector, so it stays open when the alert
 * leaves the list. */
export function AlertActionsButton({ a, onOpen }: { a: Alert; onOpen: (a: Alert) => void }) {
  const c = useCanAct(a);
  if (a.state === "resolved" || !(c.ack || c.snooze || c.ignore)) return null;
  return <Button size="sm" onClick={() => onOpen(a)} aria-label={`Actions for ${a.title} on ${a.host_name}`}>Actions…</Button>;
}

function Choice({ title, help, onClick, disabled, note }: { title: string; help: ReactNode; onClick: () => void; disabled?: boolean; note?: string }) {
  return (
    <li>
      <button type="button" onClick={onClick} disabled={disabled}
        className="flex w-full flex-col items-start gap-0.5 rounded-ui border border-line bg-surface px-3 py-2 text-left hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50">
        <span className="text-sm font-medium">{title}</span>
        <span className="text-xs text-muted">{help}</span>
        {note && <span className="text-xs text-muted">{note}</span>}
      </button>
    </li>
  );
}

/** An open alert, inspected beside the list (Pattern A): what is wrong,
 * where, since when and who was told, with the ways to deal with it —
 * Acknowledge, Snooze (maintenance for its host), Ignore (its health
 * setting) — each a step inside the same panel with Back, never a dialog on
 * top; and the way to its host, its monitoring and its full page (timeline,
 * deliveries). The list keeps it open while acting takes the alert out of
 * view. */
export function AlertInspector({ a, onClose }: { a: Alert; onClose: () => void }) {
  const c = useCanAct(a);
  const [step, setStep] = useState<"menu" | "ack" | "snooze" | "ignore" | "service">("menu");
  const back = () => setStep("menu");
  const open = a.state !== "resolved";
  // A service alert offers what fixes it (start, restart or stop the unit),
  // reviewed and followed in this panel.
  const fix = useServiceFix(a.host_id, a.category === "services" ? a.subject : undefined, a.check, open);
  const title = step === "ack" ? `Acknowledge: ${a.title}` : step === "snooze" ? `Snooze: ${a.title}` : step === "ignore" ? `Ignore: ${a.title}`
    : step === "service" && fix ? `${fix.label.replace(/…$/, "")} on ${a.host_name}` : a.title;
  return (
    <Inspector title={title} onClose={onClose} onBack={step === "menu" ? undefined : back} testId="alert-inspector"
      subtitle={<span className="inline-flex flex-wrap items-center gap-2"><AlertSeverity severity={a.severity} /><AlertStateBadge a={a} /><span>{a.host_name}</span></span>}>
      {/* Done is done: the list shows the result (the alert moves or is marked). */}
      {step === "ack" ? <AcknowledgeForm a={a} onBack={back} onClose={onClose} onDone={onClose} />
        : step === "snooze" ? <MaintenanceForm hostIds={[a.host_id]} defaultReason={`Snoozed: ${a.title}`} onClose={back} onDone={onClose} cancelLabel="Back" />
          : step === "ignore" ? <IgnoreForm a={a} onBack={back} onClose={onClose} />
            : step === "service" && fix ? <ServiceActionsDialog hostId={a.host_id} hostName={a.host_name} unit={fix.unit} initial={fix.action} inline onClose={back} />
            : (
              <div className="space-y-5 text-sm">
                <dl className="grid grid-cols-[minmax(6.5rem,8rem)_1fr] gap-y-1.5" data-testid="alert-summary">
                  <dt className="text-muted">Host</dt><dd><Link className={cx(plainLink, touchLink)} to={`/hosts/${a.host_id}`}>{a.host_name}</Link></dd>
                  <dt className="text-muted">What</dt><dd>{categoryLabel[a.category] ?? a.category}{a.subject && <span className="font-mono text-xs"> · {a.subject}</span>}</dd>
                  {a.value_text && (<><dt className="text-muted">Now</dt><dd>{a.value_text}</dd></>)}
                  <dt className="text-muted">Since</dt><dd>{fmtTime(a.observed_at ?? a.issue_started_at)} <span className="text-muted">({fmtSince(a.observed_at ?? a.issue_started_at)})</span></dd>
                  {a.acknowledgement && (<><dt className="text-muted">Acknowledged</dt><dd>by {a.acknowledgement.by}{a.acknowledgement.note && <span className="block text-muted">“{a.acknowledgement.note}”</span>}</dd></>)}
                  <dt className="text-muted">Notification</dt><dd className="flex flex-wrap items-center gap-2"><NotificationBadge a={a} /><MaintenanceNote a={a} /></dd>
                </dl>
                {a.detail && <p className="rounded-ui bg-surface-2 px-3 py-2">{a.detail}</p>}
                {open && (
                  <section data-testid="alert-actions" aria-label="Deal with it">
                    <h3 className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted">Deal with it</h3>
                    <ul className="space-y-2" aria-label="Actions">
                      {fix && (
                        <Choice title={fix.label} onClick={() => setStep("service")}
                          help={<>Fix it on {a.host_name}: you confirm first, then DROOL checks the unit again and says what it finds. The alert resolves when monitoring sees it recover.</>} />
                      )}
                      {a.state === "active" && (
                        <Choice title="Acknowledge…" disabled={!c.ack} onClick={() => setStep("ack")}
                          help="You've seen it: no more reminders. It stays open until the condition recovers." />
                      )}
                      <Choice title="Snooze…" disabled={!c.snooze} onClick={() => setStep("snooze")}
                        help={<>Maintenance for {a.host_name} until a time you choose: no notifications meanwhile, health is still recorded, and what is still wrong when it ends is notified then.</>} />
                      <Choice title="Ignore…" disabled={!c.ignore} onClick={() => setStep("ignore")}
                        help="If it's meant to be this way: change the health setting behind it, so it isn't a problem any more. The alert closes as cleared, not recovered." />
                    </ul>
                    {!(c.ack && c.snooze && c.ignore) && <p className="mt-2 text-xs text-muted">Greyed out: your role can&apos;t do that.</p>}
                  </section>
                )}
                <section aria-label="Look further">
                  <h3 className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted">Look further</h3>
                  <ul className="space-y-1.5">
                    <li><Link className={cx(plainLink, touchLink)} to={`/hosts/${a.host_id}`}>View host {a.host_name}</Link></li>
                    <li><Link className={cx(plainLink, touchLink)} to={alertMonitoringLink(a).to}>{alertMonitoringLink(a).label}</Link></li>
                    <li><Link className={cx(plainLink, touchLink)} to={`/alerts/${a.id}`}>Open the alert</Link> <span className="text-xs text-muted">— its timeline, deliveries and other occurrences</span></li>
                  </ul>
                </section>
              </div>
            )}
    </Inspector>
  );
}

/** Acknowledge, with the optional note (the same as on the alert's page). */
export function AcknowledgeDialog({ a, onBack, onClose }: { a: Alert; onBack?: () => void; onClose: () => void }) {
  return (
    <Dialog title={`Acknowledge: ${a.title}`} onClose={onClose}>
      <AcknowledgeForm a={a} onBack={onBack} onClose={onClose} onDone={onClose} />
    </Dialog>
  );
}

/** The acknowledgement form, for a dialog or an inspector step. */
export function AcknowledgeForm({ a, onBack, onClose, onDone }: { a: Alert; onBack?: () => void; onClose: () => void; onDone: () => void }) {
  const ack = useAcknowledge(a.id);
  const [note, setNote] = useState("");
  return (
      <form className="space-y-3" data-testid="alert-acknowledge" onSubmit={(e) => { e.preventDefault(); ack.mutate(note, { onSuccess: onDone }); }}>
        <p className="text-sm text-muted">Tells others someone is on it and stops reminders. It doesn&apos;t close the alert: that happens when {a.host_name}&apos;s condition recovers.</p>
        <Field label="Note (optional)">
          <textarea className="min-h-16 w-full rounded-ui border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent focus:ring-2 focus:ring-accent/25"
            maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. Replacing the failed disk this evening" data-autofocus />
        </Field>
        {ack.error && <Notice>{errorMessage(ack.error)}</Notice>}
        <div className="flex justify-end gap-2">
          {onBack ? <Button type="button" onClick={onBack}>Back</Button> : <Button type="button" onClick={onClose}>Cancel</Button>}
          <Button type="submit" variant="primary" disabled={ack.isPending}>{ack.isPending ? "Acknowledging…" : "Acknowledge"}</Button>
        </div>
      </form>
  );
}

/** Ignore…: the health settings that would make this condition stop being
 * a problem (the server lists them, narrowest first), what each changes and
 * where it's undone. Applying one is an ordinary health setting change with
 * its own permissions and audit; nothing closes an alert by hand. */
export function IgnoreDialog({ a, onBack, onClose }: { a: Alert; onBack?: () => void; onClose: () => void }) {
  return (
    <Dialog title={`Ignore: ${a.title}`} onClose={onClose}>
      <IgnoreForm a={a} onBack={onBack} onClose={onClose} />
    </Dialog>
  );
}

/** The Ignore… choices, for a dialog or an inspector step. */
export function IgnoreForm({ a, onBack, onClose }: { a: Alert; onBack?: () => void; onClose: () => void }) {
  const qc = useQueryClient();
  // Not under alertKeys.all: applying an option refreshes the alerts, and
  // the options it was chosen from stay as they were.
  const q = useQuery({
    queryKey: ["alert-ignore-options", a.id],
    queryFn: () => unwrap(api.GET("/alerts/{id}/ignore-options", { params: { path: { id: a.id } } })),
    gcTime: 0,
  });
  const opts = q.data?.options ?? [];
  const [picked, setPicked] = useState<string>();
  const sel: IgnoreOption | undefined = opts.find((o) => o.id === (picked ?? opts.find((x) => x.allowed)?.id));
  const apply = useMutation({
    mutationFn: (o: IgnoreOption) => unwrap(api.PUT("/monitoring/health/overrides", { body: o.change })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: alertKeys.all });
      void qc.invalidateQueries({ queryKey: ["monitoring"] });
      void qc.invalidateQueries({ queryKey: ["health"] });
    },
  });
  const needs = (n?: string) => (n === "monitoring.settings" ? "an administrator's (monitoring.settings)" : "monitoring.manage");
  return (
      <div className="space-y-4 text-sm" data-testid="alert-ignore">
        {q.error && <Notice>{errorMessage(q.error)}</Notice>}
        {apply.isSuccess ? (
          <>
            <Notice tone="ok">Done: {apply.variables.label}. {q.data?.closes}</Notice>
            <p className="text-xs text-muted">{apply.variables.reset}</p>
            <div className="flex justify-end"><Button variant="primary" onClick={onClose} data-autofocus>Close</Button></div>
          </>
        ) : !q.data ? <p className="text-muted">Working out what would change…</p> : opts.length === 0 ? (
          <>
            <p>{q.data.note || "Nothing to change for this alert."}</p>
            <div className="flex justify-end gap-2">{onBack && <Button onClick={onBack}>Back</Button>}<Button onClick={onClose}>Close</Button></div>
          </>
        ) : (
          <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (sel?.allowed) apply.mutate(sel); }}>
            <p className="text-muted">If this is how {a.host_name} is meant to be, change the setting behind it, for this host only:</p>
            <fieldset className="space-y-2">
              <legend className="sr-only">What to change</legend>
              {opts.map((o) => (
                <label key={o.id} className={`flex items-start gap-2 rounded-ui border p-2 ${sel?.id === o.id ? "border-accent bg-accent/5" : "border-line"} ${o.allowed ? "" : "opacity-60"}`}>
                  <input type="radio" name="ignore" className="mt-1 accent-[var(--ui-accent)]" checked={sel?.id === o.id} disabled={!o.allowed}
                    onChange={() => setPicked(o.id)} />
                  <span>
                    <span className="font-medium">{o.label}</span>
                    <span className="block text-xs text-muted">{o.effect}</span>
                    {!o.allowed && <span className="block text-xs text-muted">Needs {needs(o.needs)}.</span>}
                  </span>
                </label>
              ))}
            </fieldset>
            {sel && <p className="text-xs text-muted" data-testid="ignore-reset">{q.data.closes} {sel.reset}</p>}
            {apply.error && <Notice>{errorMessage(apply.error)}</Notice>}
            <div className="flex justify-end gap-2">
              {onBack ? <Button type="button" onClick={onBack}>Back</Button> : <Button type="button" onClick={onClose}>Cancel</Button>}
              <Button type="submit" variant="primary" disabled={!sel?.allowed || apply.isPending}>{apply.isPending ? "Saving…" : "Change the setting"}</Button>
            </div>
          </form>
        )}
      </div>
  );
}
