import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert as Notice } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { MaintenanceDialog } from "./MaintenanceDialog";
import { alertKeys, useAcknowledge, type Alert } from "./queries";

type IgnoreOption = Schemas["AlertIgnoreOptions"]["options"][number];

/** Can the signed-in user do anything from an alert's Actions…? */
function useCanAct(a: Alert) {
  const { can } = useAuth();
  return { ack: a.state === "active" && can("alerts.acknowledge"), snooze: can("maintenance.manage"), ignore: can("monitoring.manage") };
}

/** Actions… on an open alert's row (dogfooding correction batch): the
 * common ways to deal with it without opening it. Acknowledge, Snooze
 * (maintenance for its host) and Ignore (its health settings, so the
 * condition stops being a problem). The alert's page has the same and its
 * timeline. Closed alerts have none. The list shows the dialog
 * (AlertActionsDialog), so it stays open when the alert leaves the list. */
export function AlertActionsButton({ a, onOpen }: { a: Alert; onOpen: (a: Alert) => void }) {
  const c = useCanAct(a);
  if (a.state === "resolved" || !(c.ack || c.snooze || c.ignore)) return null;
  return <Button size="sm" onClick={() => onOpen(a)} aria-label={`Actions for ${a.title} on ${a.host_name}`}>Actions…</Button>;
}

function Choice({ title, help, onClick, disabled, note }: { title: string; help: ReactNode; onClick: () => void; disabled?: boolean; note?: string }) {
  return (
    <li>
      <button type="button" onClick={onClick} disabled={disabled} aria-label={title}
        className="flex w-full flex-col items-start gap-0.5 rounded-ui border border-line bg-surface px-3 py-2 text-left hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50">
        <span className="text-sm font-medium">{title}</span>
        <span className="text-xs text-muted">{help}</span>
        {note && <span className="text-xs text-muted">{note}</span>}
      </button>
    </li>
  );
}

export function AlertActionsDialog({ a, onClose }: { a: Alert; onClose: () => void }) {
  const c = useCanAct(a);
  const [step, setStep] = useState<"menu" | "ack" | "snooze" | "ignore">("menu");
  if (step === "snooze") return <MaintenanceDialog hostIds={[a.host_id]} defaultReason={`Snoozed: ${a.title}`} onClose={onClose} />;
  if (step === "ack") return <AcknowledgeDialog a={a} onBack={() => setStep("menu")} onClose={onClose} />;
  if (step === "ignore") return <IgnoreDialog a={a} onBack={() => setStep("menu")} onClose={onClose} />;
  return (
    <Dialog title={`${a.host_name}: ${a.title}`} onClose={onClose}>
      <div className="space-y-4" data-testid="alert-actions">
        <ul className="space-y-2" aria-label="Actions">
          {a.state === "active" && (
            <Choice title="Acknowledge…" disabled={!c.ack} onClick={() => setStep("ack")}
              help="You've seen it: no more reminders. It stays open until the condition recovers." />
          )}
          <Choice title="Snooze…" disabled={!c.snooze} onClick={() => setStep("snooze")}
            help={<>Maintenance for {a.host_name} until a time you choose: no notifications meanwhile, health is still recorded, and what is still wrong when it ends is notified then.</>} />
          <Choice title="Ignore…" disabled={!c.ignore} onClick={() => setStep("ignore")}
            help="If it's meant to be this way: change the health setting behind it, so it isn't a problem any more. The alert closes as cleared, not recovered." />
        </ul>
        {!(c.ack && c.snooze && c.ignore) && (
          <p className="text-xs text-muted">Greyed out: your role can&apos;t do that.</p>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Link className={plainLink} to={`/alerts/${a.id}`}>Open the alert</Link>
          <Button onClick={onClose}>Close</Button>
        </div>
      </div>
    </Dialog>
  );
}

/** Acknowledge, with the optional note (the same as on the alert's page). */
export function AcknowledgeDialog({ a, onBack, onClose }: { a: Alert; onBack?: () => void; onClose: () => void }) {
  const ack = useAcknowledge(a.id);
  const [note, setNote] = useState("");
  return (
    <Dialog title={`Acknowledge: ${a.title}`} onClose={onClose}>
      <form className="space-y-3" data-testid="alert-acknowledge" onSubmit={(e) => { e.preventDefault(); ack.mutate(note, { onSuccess: onClose }); }}>
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
    </Dialog>
  );
}

/** Ignore…: the health settings that would make this condition stop being
 * a problem (the server lists them, narrowest first), what each changes and
 * where it's undone. Applying one is an ordinary health setting change with
 * its own permissions and audit; nothing closes an alert by hand. */
export function IgnoreDialog({ a, onBack, onClose }: { a: Alert; onBack?: () => void; onClose: () => void }) {
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
    <Dialog title={`Ignore: ${a.title}`} onClose={onClose}>
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
    </Dialog>
  );
}
