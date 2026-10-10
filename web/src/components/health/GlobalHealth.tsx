import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { Input } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { useDeleteOverride, useHealthGlobal, useHealthGroup, useHealthGroups, useHealthOverrides, usePreview, useSetGroupPriority, type HealthGroup, type HealthOverride } from "./queries";
import { HealthChecksTable } from "./ui";
import { healthScopes } from "./provenance";
import { ScopeLegend } from "@/components/ui/Provenance";

/** Settings → Monitoring: Recommended is the simple path; customizing is
 * one click away and per check. */
export function HealthChecksCard() {
  const g = useHealthGlobal();
  const [open, setOpen] = useState(false);
  const v = g.data;
  return (
    <Card title="Health checks" actions={<Button size="sm" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? "Hide checks" : "Customize"}</Button>}>
      {g.error && <Alert>{errorMessage(g.error)}</Alert>}
      <p className="text-sm">
        <b>{v?.summary?.using_recommended ? "Using the Recommended profile" : "Recommended profile with global changes"}</b>
        <span className="text-muted">
          {" "}— useful health checks with no configuration. Temperatures use each sensor's own hardware limits; high GPU utilization is off
          (a busy GPU is usually working, not failing).
        </span>
      </p>
      <p className="mt-1 text-xs text-muted">
        Packs, hosts and individual devices can override single settings; everything else is inherited. This decides what monitoring
        considers unhealthy — who gets notified is configured in <Link className="text-accent" to="/settings/notifications">Notifications</Link>.
      </p>
      <div className="mt-2"><ScopeLegend scopes={healthScopes} /></div>
      {open && v && (
        <div className="mt-3">
          <HealthChecksTable checks={v.checks} k={{ scope: "global" }} title={(c) => `${c.label} for every host`} />
        </div>
      )}
    </Card>
  );
}

function fieldsText(o: HealthOverride) {
  return Object.entries(o.fields)
    .map(([k, v]) => (k === "enabled" ? (v ? "on" : "off") : `${k.replace("_min", "").replace("_", " ")} ${v}`))
    .join(", ");
}

/** Advanced: every group, host and device override in one place, and the
 * group priorities that decide between groups. */
export function HealthOverridesCard() {
  const { can } = useAuth();
  const ov = useHealthOverrides();
  const groups = useHealthGroups();
  const glob = useHealthGlobal();
  const del = useDeleteOverride();
  const label = (check: string) => glob.data?.checks.find((c) => c.check === check)?.label ?? check;
  const rows = (ov.data?.items ?? []).filter((o) => o.scope !== "global");
  const canEdit = (o: HealthOverride) => (o.scope === "host" ? can("monitoring.manage") : can("monitoring.settings"));
  return (
    <Card title="Health overrides (advanced)">
      <details>
        <summary className="cursor-pointer text-sm">
          {rows.length === 0 ? "No pack, host or device overrides." : `${rows.length} pack, host and device overrides`} — show all and pack priorities
        </summary>
        <div className="mt-3 space-y-4">
          {(ov.error || del.error) && <Alert>{errorMessage(ov.error ?? del.error)}</Alert>}
          {rows.length > 0 && (
            <div className="overflow-x-auto">
              <table className="rtable w-full text-left text-sm">
                <caption className="sr-only">Health overrides</caption>
                <thead><tr className="border-b border-line text-xs text-muted">
                  <th className="px-2 py-1 font-medium">Where</th><th className="px-2 py-1 font-medium">Check</th>
                  <th className="px-2 py-1 font-medium">Overrides</th><th className="px-2 py-1 font-medium">Changed</th><th className="px-2 py-1"><span className="sr-only">Actions</span></th>
                </tr></thead>
                <tbody>
                  {rows.map((o) => (
                    <tr key={o.id} className="border-b border-line last:border-0">
                      <td data-primary="" className="px-2 py-1.5">
                        {o.scope === "group" ? <>Pack <Link className="text-accent" to="/groups">{o.group_name}</Link></> :
                          <Link className="text-accent" to={`/hosts/${o.host_id}/monitoring?tab=health`}>{o.host_name}</Link>}
                        {o.subject && <span className="text-muted"> · {o.subject_label || o.subject}</span>}
                      </td>
                      <td data-label="Check" className="px-2 py-1.5">{label(o.check)}</td>
                      <td data-label="Overrides" className="px-2 py-1.5 font-mono text-xs">{fieldsText(o)}</td>
                      <td data-label="Changed" className="px-2 py-1.5 text-xs text-muted">{o.updated_by || "system"} · {new Date(o.updated_at).toLocaleString()}</td>
                      <td data-label="" className="px-2 py-1.5 text-right">
                        {canEdit(o) && <Button size="sm" variant="ghost" disabled={del.isPending} onClick={() => del.mutate(o.id)} aria-label={`Reset ${label(o.check)} for ${o.group_name || o.host_name} to inherited`}>Reset</Button>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div>
            <h3 className="mb-1 text-sm font-medium">Pack priorities</h3>
            <p className="mb-2 text-xs text-muted">
              Only matters when a host is in several groups that set the same value differently: the higher priority wins. Equal
              priorities with different values are shown as a conflict and use the global value until settled.
            </p>
            <ul className="divide-y divide-line text-sm">
              {(groups.data?.groups ?? []).map((g) => <PriorityRow key={g.group_id} g={g} />)}
              {(groups.data?.groups ?? []).length === 0 && <li className="py-1 text-muted">No packs.</li>}
            </ul>
          </div>
        </div>
      </details>
    </Card>
  );
}

function PriorityRow({ g }: { g: HealthGroup }) {
  const { can } = useAuth();
  const [v, setV] = useState(String(g.priority));
  const set = useSetGroupPriority();
  const preview = usePreview();
  useEffect(() => setV(String(g.priority)), [g.priority]);
  const n = Number(v);
  const changed = v !== "" && n !== g.priority && Number.isInteger(n);
  useEffect(() => {
    if (!changed || g.overrides === 0) return;
    const t = setTimeout(() => preview.mutate({ scope: "group", group_id: g.group_id, check: "cpu", fields: {}, priority: n }), 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [v]);
  return (
    <li className="flex flex-wrap items-center gap-2 py-1.5">
      <span className="w-40 font-medium">{g.name}</span>
      <span className="text-xs text-muted">{g.hosts} hosts · {g.overrides} overridden fields</span>
      <label className="ml-auto flex items-center gap-2 text-xs">
        Priority
        <Input type="number" className="w-20" min={-1000} max={1000} value={v} disabled={!can("monitoring.settings")} onChange={(e) => setV(e.target.value)} />
      </label>
      {changed && can("monitoring.settings") && (
        <Button size="sm" onClick={() => set.mutate({ id: g.group_id, priority: n })} disabled={set.isPending}>Save</Button>
      )}
      {changed && preview.data && <span className="basis-full text-right text-xs text-muted">{preview.data.summary}</span>}
      {set.error && <span className="basis-full"><Alert>{errorMessage(set.error)}</Alert></span>}
    </li>
  );
}

/** A group's monitoring overrides (Groups page): hidden until somebody
 * chooses to customize the group. */
export function GroupHealthButton({ groupId, name }: { groupId: string; name: string }) {
  const groups = useHealthGroups();
  const [open, setOpen] = useState(false);
  const info = groups.data?.groups.find((g) => g.group_id === groupId);
  return (
    <>
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)} aria-label={`Monitoring overrides for ${name}`}>
        {info && info.overrides > 0 ? `Monitoring overrides (${info.overrides})` : "Customize monitoring"}
      </Button>
      {open && <GroupHealthDialog groupId={groupId} name={name} onClose={() => setOpen(false)} />}
    </>
  );
}

function GroupHealthDialog({ groupId, name, onClose }: { groupId: string; name: string; onClose: () => void }) {
  const q = useHealthGroup(groupId);
  const groups = useHealthGroups();
  const info = groups.data?.groups.find((g) => g.group_id === groupId);
  return (
    <Dialog title={`Monitoring overrides: ${name}`} onClose={onClose} wide>
      <p className="mb-3 text-sm text-muted">
        Settings here apply to every host in the pack {name} unless a host or device overrides them. Values not set here are inherited from the
        global settings and the Recommended profile. Changes show their impact before you save.
      </p>
      {info && (
        <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
          <Badge>{info.hosts} hosts</Badge>
          <Badge>Priority {info.priority}</Badge>
          <span className="text-xs text-muted">The priority only matters when a host's packs disagree (Monitoring → Health checks and settings → Health overrides).</span>
        </div>
      )}
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data ? (
        <HealthChecksTable checks={q.data.checks} k={{ scope: "group", group_id: groupId }} title={(c) => `${c.label} for pack ${name}`} />
      ) : <p className="text-sm text-muted">Loading…</p>}
      <div className="mt-4"><Button onClick={onClose}>Close</Button></div>
    </Dialog>
  );
}


