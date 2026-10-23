import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage } from "@/lib/format";
import { useHostHealth, usePreview, useSetGroupPriority, type HealthCheck, type HealthField, type HealthSubject } from "./queries";
import { fieldValue, HealthChecksTable, IdentityBadge } from "./ui";
import { HealthEditorForm } from "./HealthEditor";
import { healthScope, healthScopes } from "./provenance";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { ScopeLegend } from "@/components/ui/Provenance";

const kindWord: Record<string, string> = {
  filesystem: "Filesystems", disk: "Disks", gpu: "GPUs", sensor: "Temperature sensors", interface: "Network interfaces", service: "Services", probe: "Probe targets",
};

/** A host's health checks: where they come from (Packs, with their
 * disagreements said plainly and settled in one step), host-level
 * customization, and the subjects (filesystems, disks, GPUs, sensors,
 * interfaces, services) that can be customized one by one. */
export function HostHealthPanel({ hostId }: { hostId: string }) {
  const q = useHostHealth(hostId);
  const [subject, setSubject] = useState<HealthSubject>();
  const v = q.data;
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!v) return <p className="text-sm text-muted">Loading…</p>;
  const kinds = [...new Set((v.subjects ?? []).map((s) => s.kind))];
  const groups = v.groups ?? [];
  return (
    <div className="space-y-4">
      <Card title="Health checks for this host">
        <p className="text-sm">
          <b>{v.summary?.text}</b>
          {v.summary?.using_recommended && <span className="text-muted"> — the same as every other host unless you customize something.</span>}
        </p>
        {groups.length > 0 && (
          <p className="mt-1 text-xs text-muted">
            From its Packs:{" "}
            {groups.map((g, i) => (
              <span key={g.group_id}>{i > 0 && ", "}<Link className="text-accent" to={`/groups/${g.group_id}`}>{g.name}</Link> (monitoring priority {g.priority}{g.overrides ? `, ${g.overrides} set` : ""})</span>
            ))}. When two Packs set a value differently, the higher priority wins.
          </p>
        )}
        <div className="mt-2"><ScopeLegend scopes={healthScopes} /></div>
        <PackConflicts checks={v.checks} />
        <div className="mt-3">
          <HealthChecksTable checks={v.checks} k={{ scope: "host", host_id: hostId }} title={(c) => `${c.label} on this host`} />
        </div>
        <p className="mt-2 text-xs text-muted">
          Who gets notified is set in <Link className="text-accent" to="/settings/notifications">Settings → Notifications</Link>.
        </p>
      </Card>
      <Card title="Individual devices and services">
        <p className="mb-3 text-sm text-muted">
          Customize one filesystem, disk, GPU, sensor, interface or service. Settings follow the device's own identity (a filesystem UUID,
          a disk's WWN, a GPU UUID), not its current name.
        </p>
        {kinds.length === 0 && <p className="text-sm text-muted">Shown once the host has reported.</p>}
        {kinds.map((k) => (
          <div key={k} className="mb-3">
            <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">{kindWord[k] ?? k}</h3>
            <ul className="divide-y divide-line">
              {(v.subjects ?? []).filter((s) => s.kind === k).map((s) => (
                <li key={s.key} className="flex flex-wrap items-center gap-2 py-1.5 text-sm">
                  <span className="font-medium">{s.label}</span>
                  <IdentityBadge s={s} />
                  {s.customized && <Badge tone="accent">Customized</Badge>}
                  <Button size="sm" className="ml-auto" onClick={() => setSubject(s)} aria-label={`${s.customized ? "Edit" : "Customize"} health check for ${s.label}`}>
                    {s.customized ? "Edit…" : "Customize…"}
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </Card>
      {subject && <SubjectHealthDialog hostId={hostId} subject={subject} onClose={() => setSubject(undefined)} />}
    </div>
  );
}

type PackConflict = { check: HealthCheck; field: HealthField; priority: number; groups: { id: string; name: string; value?: number }[] };

/** The fields where a host's Packs of equal monitoring priority set
 * different values (the server's verdict, per field). */
export function packConflicts(checks: HealthCheck[]): PackConflict[] {
  const out: PackConflict[] = [];
  for (const c of checks) {
    for (const f of c.fields) {
      const x = f.inherited.conflict;
      if (x) out.push({ check: c, field: f, priority: x.priority, groups: x.groups });
    }
  }
  return out;
}

/** Packs that disagree about this host, said plainly: which value, which
 * Packs with what, what applies meanwhile; "Let … win" settles it (the
 * Pack's monitoring priority goes just above the other's, after a preview),
 * or the value can be set on the host. */
function PackConflicts({ checks }: { checks: HealthCheck[] }) {
  const { can } = useAuth();
  const set = useSetGroupPriority();
  const preview = usePreview();
  const all = packConflicts(checks);
  if (all.length === 0) return null;
  const win = async (g: { id: string; name: string }, c: PackConflict) => {
    const n = c.priority + 1; // the others are all at c.priority
    const others = c.groups.filter((x) => x.id !== g.id).map((x) => x.name).join(" and ");
    const pv = await preview.mutateAsync({ scope: "group", group_id: g.id, check: c.check.check, fields: {}, priority: n }).catch(() => undefined);
    const ok = await confirmAction({
      title: `Let ${g.name} win over ${others}?`, confirmLabel: `Set ${g.name}'s monitoring priority to ${n}`,
      body: (
        <div className="space-y-2">
          <p>{g.name}&apos;s monitoring priority becomes {n}, above {others} ({c.priority}): where they set a value differently, {g.name}&apos;s applies, on every host in both.</p>
          <p className="text-sm text-muted">{pv ? `${pv.summary}.` : "The impact couldn't be worked out in advance."}</p>
        </div>
      ),
    });
    if (ok) set.mutate({ id: g.id, priority: n });
  };
  return (
    <div className="mt-3" data-testid="pack-conflicts">
      <Alert tone="warn">
        <p className="font-medium">Packs disagree about {all.length === 1 ? "one value" : `${all.length} values`} on this host</p>
        <ul className="mt-1 space-y-2">
          {all.map((c) => (
            <li key={`${c.check.check}:${c.field.name}`}>
              {c.check.label}, {c.field.label.toLowerCase()}:{" "}
              {c.groups.map((g, i) => (
                <span key={g.id}>{i > 0 && " and "}<Link className="underline" to={`/groups/${g.id}`}>{g.name}</Link>{g.value != null && ` (${fieldValue(g.value, c.field)})`}</span>
              ))}
              , both monitoring priority {c.priority}. Meanwhile {fieldValue(c.field.inherited.value, c.field, c.field.inherited.source)} from {healthScope(c.field.inherited.source)} applies.
              {can("monitoring.settings") && (
                <span className="mt-1 flex flex-wrap gap-2">
                  {c.groups.map((g) => <Button key={g.id} size="sm" disabled={set.isPending || preview.isPending} onClick={() => void win(g, c)}>Let {g.name} win…</Button>)}
                </span>
              )}
            </li>
          ))}
        </ul>
        <p className="mt-2 text-xs">Or set the value on this host itself: Customize… on the check below.</p>
        {set.error && <p className="mt-1 text-danger">{errorMessage(set.error)}</p>}
      </Alert>
    </div>
  );
}

/** The health checks of one subject (opened from the health settings tab
 * or from a filesystem, disk, sensor, GPU or service row). Customizing a
 * check is the dialog's next step, not a dialog on top. */
export function SubjectHealthDialog({ hostId, subject, onClose }: { hostId: string; subject: HealthSubject; onClose: () => void }) {
  const [editing, setEditing] = useState<HealthCheck>();
  return (
    <Dialog title={editing ? `${editing.label}: ${subject.label}` : `Health checks: ${subject.label}`} onClose={onClose} wide
      onBack={editing ? () => setEditing(undefined) : undefined} backLabel={`Back to ${subject.label}`} step={editing ? `edit:${editing.check}` : ""}>
      <SubjectHealthPanel hostId={hostId} subject={subject} editing={editing} onEdit={setEditing} />
      {!editing && <div className="mt-4"><Button onClick={onClose}>Close</Button></div>}
    </Dialog>
  );
}

/** One subject's checks, or (editing) the editor of one of them: the
 * content of a dialog's step. */
export function SubjectHealthPanel({ hostId, subject, editing, onEdit }: {
  hostId: string; subject: HealthSubject; editing?: HealthCheck; onEdit: (c?: HealthCheck) => void;
}) {
  const q = useHostHealth(hostId, subject.key);
  const v = q.data;
  const k = { scope: "host" as const, host_id: hostId, subject: subject.key };
  if (editing) return <HealthEditorForm k={{ ...k, check: editing.check }} check={editing} onDone={() => onEdit(undefined)} onCancel={() => onEdit(undefined)} />;
  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
        <IdentityBadge s={subject} />
        <span className="text-muted">{subject.present ? subject.identity : "The host doesn't report it now. Its settings stay and apply again if it returns with the same identity."}</span>
      </div>
      {subject.strength === "weak" && (
        <div className="mb-3"><Alert tone="warn">DROOL only knows this by its position or kernel name, which can refer to another device after a reboot or a hardware change.</Alert></div>
      )}
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {!v ? (!q.error && <p className="text-sm text-muted">Loading…</p>) : (
        <HealthChecksTable checks={v.checks} k={k} title={(c) => `${c.label}: ${subject.label}`} onEdit={onEdit} />
      )}
    </>
  );
}

/** "Customize health check" for one row of a monitoring page; ref is
 * what that page calls the thing (a mount, a sensor id, gpu:<index>…). */
export function SubjectHealthButton({ hostId, kind, subjectRef: r, label }: { hostId: string; kind: string; subjectRef: string; label: string }) {
  const { can } = useAuth();
  const q = useHostHealth(hostId, undefined, can("monitoring.read"));
  const [open, setOpen] = useState(false);
  const s = (q.data?.subjects ?? []).find((x) => x.kind === kind && x.ref === r);
  if (!s) return null;
  return (
    <>
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)} aria-label={`${s.customized ? "Customized" : "Customize"} health check for ${label}`}>
        {s.customized ? <><span aria-hidden="true">⚙ </span>Customized</> : "Customize health check"}
      </Button>
      {open && <SubjectHealthDialog hostId={hostId} subject={s} onClose={() => setOpen(false)} />}
    </>
  );
}
