import { useState } from "react";
import { useAuth } from "@/auth/AuthContext";
import { Alert as Notice } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { errorMessage } from "@/lib/format";
import { HealthEditorForm } from "@/components/health/HealthEditor";
import { useHealthGlobal, useHealthGroup, useHostHealth, type HealthCheck, type HealthGroup, type HealthSubject, type OverrideKey } from "@/components/health/queries";
import { effectText, SourceBadge, sources } from "@/components/health/ui";
import { healthScope } from "@/components/health/provenance";
import type { Alert } from "./queries";

type Key = Omit<OverrideKey, "check">;

const kindPlural: Record<string, string> = {
  filesystem: "filesystem", disk: "disk", gpu: "GPU", sensor: "temperature sensor", interface: "network interface", service: "service", probe: "probe target",
};

/** The health check behind an alert, as it applies to the alert's subject
 * (a filesystem, a service…) or its host: what fired and where its values
 * come from. Undefined while loading, or when the person can't read
 * monitoring settings or the alert has no adjustable check. */
export function useAlertRule(a: Alert, enabled = true) {
  const { can } = useAuth();
  const read = enabled && can("monitoring.read");
  const host = useHostHealth(a.host_id, undefined, read);
  const subject = a.subject ? (host.data?.subjects ?? []).find((s) => s.issue_subject === a.subject && s.checks.includes(a.check)) : undefined;
  const sub = useHostHealth(a.host_id, subject?.key, read && !!subject);
  const view = subject ? sub.data : host.data;
  const check = view?.checks.find((c) => c.check === a.check && !c.info);
  return { check, subject, groups: host.data?.groups ?? [], loading: read && (host.isLoading || (!!subject && sub.isLoading)), error: host.error ?? sub.error };
}

/** "Now: warning 80% · critical 95% (Global)": the rule in one line. */
export function ruleText(c: HealthCheck) {
  const from = sources(c).map(healthScope);
  return `${effectText(c)} (${from.length ? from.join(", ") : "Recommended"})`;
}

/** "Adjust when this alerts…": the check behind an alert, changed at the
 * scope the person chooses — the device or service, its host, one of the
 * host's Packs, or every host — in place. The rule that fired and where it
 * comes from come first; each scope shows what it sets now; the editor and
 * its impact preview are the ordinary ones. */
export function AlertRulePanel({ a, onDone, onCancel, cancelLabel }: { a: Alert; onDone: () => void; onCancel: () => void; cancelLabel?: string }) {
  const { can } = useAuth();
  const rule = useAlertRule(a);
  const [scope, setScope] = useState<string>();
  if (rule.error) return <Notice>{errorMessage(rule.error)}</Notice>;
  if (rule.loading) return <p className="text-sm text-muted">Loading…</p>;
  if (!rule.check) return <p className="text-sm text-muted">This alert has no threshold to adjust.</p>;
  const hostKey: Key = { scope: "host", host_id: a.host_id };
  const opts: { id: string; label: string; k: Key; allowed: boolean; group?: HealthGroup; subject?: HealthSubject }[] = [
    ...(rule.subject ? [{ id: "subject", label: `Only ${rule.subject.label} on ${a.host_name}`, k: { ...hostKey, subject: rule.subject.key }, allowed: can("monitoring.manage"), subject: rule.subject }] : []),
    { id: "host", label: rule.subject ? `Every ${kindPlural[rule.subject.kind] ?? rule.subject.kind} on ${a.host_name}` : `${a.host_name}`, k: hostKey, allowed: can("monitoring.manage") },
    ...rule.groups.map((g) => ({ id: `group:${g.group_id}`, label: `The Pack ${g.name} (${g.hosts} host${g.hosts === 1 ? "" : "s"})`, k: { scope: "group" as const, group_id: g.group_id }, allowed: can("monitoring.settings"), group: g })),
    { id: "global", label: "Every host", k: { scope: "global" }, allowed: can("monitoring.settings") },
  ];
  const chosen = opts.find((o) => o.id === scope) ?? opts.find((o) => o.allowed) ?? opts[0]!;
  return (
    <div className="space-y-4 text-sm" data-testid="alert-rule">
      <div>
        <h3 className="mb-1 text-xs font-semibold uppercase tracking-wider text-muted">What fired</h3>
        <p>
          <b>{rule.check.label}</b>{rule.subject && <> for <span className="font-mono text-xs">{rule.subject.label}</span></>}: {effectText(rule.check)}
        </p>
        <p className="mt-1 flex flex-wrap items-center gap-1 text-xs text-muted">
          From: {sources(rule.check).length === 0 ? <Badge>Recommended</Badge> : sources(rule.check).map((s) => <SourceBadge key={s.label} src={s} />)}
          {rule.check.conflicts > 0 && <Badge tone="warn">⚠ Packs disagree</Badge>}
        </p>
      </div>
      <fieldset>
        <legend className="mb-1 text-xs font-semibold uppercase tracking-wider text-muted">Change it for</legend>
        <div className="space-y-1.5">
          {opts.map((o) => (
            <label key={o.id} className={`flex items-start gap-2 rounded-ui border px-3 py-2 ${chosen.id === o.id ? "border-accent bg-accent/5" : "border-line"} ${o.allowed ? "" : "opacity-60"}`}>
              <input type="radio" name="alert-rule-scope" className="mt-1 accent-[var(--ui-accent)]" checked={chosen.id === o.id} disabled={!o.allowed} onChange={() => setScope(o.id)} />
              <span className="min-w-0">
                <span className="font-medium">{o.label}</span>
                <ScopeNow k={o.k} check={a.check} />
                {!o.allowed && <span className="block text-xs text-muted">Your role can&apos;t change this.</span>}
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <ScopeEditor key={chosen.id} k={chosen.k} check={a.check} onDone={onDone} onCancel={onCancel} cancelLabel={cancelLabel} />
    </div>
  );
}

/** One scope's own view of the check (its values, inherited or set there). */
function useScopeCheck(k: Key, check: string) {
  const global = useHealthGlobal(k.scope === "global");
  const group = useHealthGroup(k.group_id ?? "", k.scope === "group");
  const host = useHostHealth(k.host_id ?? "", k.subject, k.scope === "host");
  const q = k.scope === "global" ? global : k.scope === "group" ? group : host;
  return { check: q.data?.checks.find((c) => c.check === check), loading: q.isLoading, error: q.error };
}

function ScopeNow({ k, check }: { k: Key; check: string }) {
  const s = useScopeCheck(k, check);
  if (!s.check) return null;
  return <span className="block text-xs text-muted">Now: {effectText(s.check)}{s.check.customized ? " · set here" : ""}</span>;
}

function ScopeEditor({ k, check, onDone, onCancel, cancelLabel }: { k: Key; check: string; onDone: () => void; onCancel: () => void; cancelLabel?: string }) {
  const s = useScopeCheck(k, check);
  if (s.error) return <Notice>{errorMessage(s.error)}</Notice>;
  if (!s.check) return <p className="text-muted">{s.loading ? "Loading…" : "This check can't be set here."}</p>;
  return <HealthEditorForm k={{ ...k, check }} check={s.check} onDone={onDone} onCancel={onCancel} cancelLabel={cancelLabel} />;
}
