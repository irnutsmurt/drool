import { useEffect, useMemo, useState } from "react";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { useDeleteOverride, usePreview, usePutOverride, type HealthCheck, type HealthField, type HealthFields, type HealthPreview, type OverrideKey } from "./queries";
import { fieldValue } from "./ui";
import { healthLayers, type EditScope } from "./provenance";
import { Provenance, type ProvenanceLayer } from "@/components/ui/Provenance";

type Draft = Record<string, string | undefined>; // undefined: inherited

function initial(c: HealthCheck): Draft {
  const d: Draft = {};
  for (const f of c.fields) {
    if (f.own !== null && f.own !== undefined) d[f.name] = f.type === "bool" ? (f.own ? "on" : "off") : String(f.own);
  }
  return d;
}

function toFields(d: Draft, c: HealthCheck): HealthFields {
  const out: Record<string, number | boolean> = {};
  for (const f of c.fields) {
    const v = d[f.name];
    if (v === undefined) continue;
    out[f.name] = f.type === "bool" ? v === "on" : Number(v);
  }
  return out as HealthFields;
}

const scopeWord = { global: "every host", group: "this Pack", host: "this host" } as const;

/** The fields set most: the rest (when a condition clears, …) are under
 * More options, open when one of them is set here. */
const primary = ["enabled", "warning", "critical", "duration_min", "missed_intervals", "expected"];

/** Customize one health check at one scope, in a dialog. */
export function HealthEditor({ k, check, title, onClose }: { k: OverrideKey; check: HealthCheck; title: string; onClose: () => void }) {
  return (
    <Dialog title={title} onClose={onClose} wide>
      <HealthEditorForm k={k} check={check} onDone={onClose} onCancel={onClose} />
    </Dialog>
  );
}

/** Customize one health check at one scope: in a dialog, or as a step of
 * one (a Pack's or a device's checks, an alert). Every field is either
 * inherited (empty, showing the value it inherits and from where) or set
 * here by typing a value; "Reset to inherited" undoes an override without
 * copying any value. */
export function HealthEditorForm({ k, check, onDone, onCancel, cancelLabel }: {
  k: OverrideKey; check: HealthCheck; onDone: () => void; onCancel: () => void; cancelLabel?: string;
}) {
  const { can } = useAuth();
  const [draft, setDraft] = useState<Draft>(() => initial(check));
  const put = usePutOverride();
  const del = useDeleteOverride();
  const preview = usePreview();
  const fields = useMemo(() => toFields(draft, check), [draft, check]);
  const key = JSON.stringify(fields);
  const broad = k.scope !== "host";
  const changed = key !== JSON.stringify(toFields(initial(check), check));
  const invalid = check.fields.some((f) => {
    const v = draft[f.name];
    return v !== undefined && f.type !== "bool" && (v === "" || isNaN(Number(v)) || Number(v) < f.min || Number(v) > f.max ||
      ((f.type === "minutes" || f.type === "count") && !Number.isInteger(Number(v))));
  });
  // Broad changes show their impact before saving.
  useEffect(() => {
    if (!changed || invalid) return;
    const t = setTimeout(() => preview.mutate({ ...k, fields }), 350);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, changed, invalid]);
  const edit = k.scope === "host" ? can("monitoring.manage") : can("monitoring.settings");
  const save = () => put.mutate({ ...k, fields }, { onSuccess: onDone });
  const scope = k.subject ? "subject" : k.scope;
  const main = check.fields.filter((f) => primary.includes(f.name));
  const more = check.fields.filter((f) => !primary.includes(f.name));
  const row = (f: HealthField) => <FieldRow key={f.name} f={f} scope={scope} value={draft[f.name]} disabled={!edit} onChange={(v) => setDraft({ ...draft, [f.name]: v })} />;
  return (
    <div data-testid="health-editor">
      <p className="mb-3 text-sm text-muted">{check.description}</p>
      <p className="mb-4 text-xs text-muted">
        For {k.subject ? "this device or service" : scopeWord[k.scope]}. Type a value to set it here; leave a field empty to inherit it. Who gets notified is set under
        Settings → Notifications.
      </p>
      <div className="space-y-3">{main.map(row)}</div>
      {more.length > 0 && (
        <details className="mt-3" open={more.some((f) => draft[f.name] !== undefined)}>
          <summary className="cursor-pointer text-sm">More options: {more.map((f) => f.label.toLowerCase()).join(", ")}</summary>
          <div className="mt-3 space-y-3">{more.map(row)}</div>
        </details>
      )}
      {changed && !invalid && (preview.data || preview.isPending) && <Impact p={preview.data} pending={preview.isPending} broad={broad} />}
      {preview.error && <div className="mt-3"><Alert>{errorMessage(preview.error)}</Alert></div>}
      {(put.error || del.error) && <div className="mt-3"><Alert>{errorMessage(put.error ?? del.error)}</Alert></div>}
      <div className="mt-5 flex flex-wrap items-center gap-2">
        {edit && <Button variant="primary" onClick={save} disabled={!changed || invalid || put.isPending}>{put.isPending ? "Saving…" : "Save"}</Button>}
        {edit && check.override_id && (
          <Button onClick={() => del.mutate(check.override_id!, { onSuccess: onDone })} disabled={del.isPending}>Reset all to inherited</Button>
        )}
        <Button variant="ghost" onClick={onCancel}>{cancelLabel ?? (edit ? "Cancel" : "Close")}</Button>
        {!edit && <span className="text-xs text-muted">You can view these settings; changing them needs more permissions.</span>}
      </div>
    </div>
  );
}

function FieldRow({ f, scope, value, disabled, onChange }: { f: HealthField; scope: EditScope; value: string | undefined; disabled: boolean; onChange: (v: string | undefined) => void }) {
  const custom = value !== undefined;
  const id = `hf-${f.name}`;
  const bad = custom && f.type !== "bool" && (value === "" || isNaN(Number(value)) || Number(value) < f.min || Number(value) > f.max ||
    ((f.type === "minutes" || f.type === "count") && !Number.isInteger(Number(value))));
  const inherited = fieldValue(f.inherited.value, f, f.inherited.source);
  const own = !custom || bad ? undefined : f.type === "bool" ? (value === "on" ? 1 : 0) : Number(value);
  const layers: ProvenanceLayer[] = healthLayers(f, scope, own).map((l) => ({
    key: l.key,
    scope: l.scope,
    value: l.state === "conflict" ? "set different values" : l.value === undefined ? undefined : fieldValue(l.value, f, l.src),
    detail: l.conflict ? `Equal priority (${l.conflict.priority}): DROOL doesn't pick one, so the value above applies until a priority or an override settles it.` : undefined,
    state: l.state,
  }));
  return (
    <div className="grid gap-3 rounded-ui border border-line p-3 sm:grid-cols-[minmax(0,15rem)_1fr]">
      <div>
        <div className="mb-1 text-sm font-medium" id={`${id}-label`}>{f.label}</div>
        <Provenance compact label={`Where ${f.label} comes from`} layers={layers} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {f.type === "bool" ? (
          <Select aria-labelledby={`${id}-label`} className="max-w-xs" value={value ?? "inherit"} disabled={disabled}
            onChange={(e) => onChange(e.target.value === "inherit" ? undefined : e.target.value)}>
            <option value="inherit">Inherited ({inherited})</option>
            <option value="on">On</option>
            <option value="off">Off</option>
          </Select>
        ) : (
          <>
            <Input type="number" aria-labelledby={`${id}-label`} aria-invalid={bad || undefined} aria-describedby={bad ? `${id}-err` : `${id}-state`}
              className="max-w-[8rem]" min={f.min} max={f.max} step={f.type === "minutes" || f.type === "count" ? 1 : "any"}
              value={custom ? value : ""} placeholder={f.inherited.value === null ? "—" : String(f.inherited.value)} disabled={disabled}
              onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)} />
            <span className="text-sm text-muted">{f.unit}</span>
            <span id={`${id}-state`} className="text-xs text-muted">{custom ? "set here" : `inherited: ${inherited}`}</span>
          </>
        )}
        {custom && !disabled && <Button size="sm" variant="ghost" onClick={() => onChange(undefined)}>Reset to inherited</Button>}
        {bad && <span id={`${id}-err`} className="text-xs text-danger" role="alert">Must be {f.min}–{f.max}{f.unit === "%" ? "%" : f.unit ? ` ${f.unit}` : ""}{f.type === "minutes" || f.type === "count" ? ", a whole number" : ""}.</span>}
        {f.type === "celsius" && custom && <span className="text-xs text-warn">Replaces the hardware's own limit for this field.</span>}
      </div>
    </div>
  );
}

function Impact({ p, pending, broad }: { p?: HealthPreview; pending: boolean; broad: boolean }) {
  if (!p) return <p className="mt-4 text-sm text-muted">Working out the impact…</p>;
  return (
    <div className={`mt-4 rounded-ui border p-3 text-sm ${p.disables || p.would_close > 0 ? "border-warn" : "border-line"}`} aria-live="polite">
      <div className="font-medium">Impact{pending ? " (updating…)" : ""}</div>
      <p className="mt-1">{p.summary}{broad && p.in_scope > 0 ? ` (of ${p.in_scope} in scope)` : ""}.</p>
      {p.disables && <p className="mt-1 text-warn">This turns a check off: matching conditions won't be reported for these hosts.</p>}
      {p.hosts.length > 0 && (
        <ul className="mt-2 max-h-40 space-y-0.5 overflow-y-auto text-xs">
          {p.hosts.slice(0, 20).map((h) => (
            <li key={h.host_id}><b>{h.name}</b>: {h.effect}{h.closes?.length ? ` — ${h.closes.join("; ")}` : ""}</li>
          ))}
          {p.hosts.length > 20 && <li className="text-muted">…and {p.affected - 20} more</li>}
        </ul>
      )}
    </div>
  );
}
