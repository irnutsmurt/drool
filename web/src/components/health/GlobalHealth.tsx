import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage } from "@/lib/format";
import { useDeleteOverride, useHealthGlobal, useHealthGroup, useHealthGroups, useHealthOverrides, type HealthCheck, type HealthOverride } from "./queries";
import { HealthChecksTable } from "./ui";
import { HealthEditorForm } from "./HealthEditor";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { healthScopes } from "./provenance";
import { ScopeLegend } from "@/components/ui/Provenance";

/** Settings → Monitoring: Recommended is the simple path; customizing is
 * one click away and per check. */
/** The checks for every host, with their values and where they come
 * from; Customize… on each. */
export function HealthChecksCard() {
  const g = useHealthGlobal();
  const v = g.data;
  return (
    <Card title="Health checks for every host">
      {g.error ? <Alert>{errorMessage(g.error)}</Alert> : !v ? <p className="text-sm text-muted">Loading…</p> : (
        <>
          <p className="text-sm">
            <b>{v.summary?.using_recommended ? "The Recommended profile, unchanged" : "The Recommended profile, with changes for every host"}</b>
            <span className="text-muted">. Temperatures use each sensor&apos;s own limits; high GPU utilization is off (a busy GPU is usually working).</span>
          </p>
          <p className="mt-1 text-xs text-muted">
            A Pack, a host or one device can set its own values; everything else is inherited. Who gets notified is set in{" "}
            <Link className="text-accent" to="/settings/notifications">Notifications</Link>.
          </p>
          <div className="mt-2"><ScopeLegend scopes={healthScopes} /></div>
          <div className="mt-3">
            <HealthChecksTable checks={v.checks} k={{ scope: "global" }} title={(c) => `${c.label} for every host`} />
          </div>
        </>
      )}
    </Card>
  );
}

const fieldWord: Record<string, [string, string]> = {
  warning: ["warning", "%"], critical: ["critical", "%"], clear: ["clears below", "%"], duration_min: ["for", " min"],
  clear_duration_min: ["clears after", " min"], missed_intervals: ["after", " missed reports"],
};

/** "warning 90%, for 10 min": an override's values in words. Temperature
 * limits are degrees, not percent. */
function fieldsText(o: HealthOverride) {
  return Object.entries(o.fields)
    .map(([k, v]) => {
      if (k === "enabled") return v ? "on" : "off";
      if (k === "expected") return `expected ${v}`;
      const [w, unit] = fieldWord[k] ?? [k.replace(/_/g, " "), ""];
      return `${w} ${v}${o.check === "temperature" && unit === "%" ? " °C" : unit}`;
    })
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
  const where = (o: HealthOverride) => (o.scope === "group" ? `the Pack ${o.group_name}` : `${o.host_name}${o.subject ? ` (${o.subject_label || o.subject})` : ""}`);
  const reset = async (o: HealthOverride) => {
    const ok = await confirmAction({
      title: `Reset ${label(o.check)} for ${where(o)}?`, confirmLabel: "Reset to inherited",
      body: <p>{o.scope === "group" ? "The Pack's hosts" : "It"} then use{o.scope === "group" ? "" : "s"} the inherited values again ({fieldsText(o)} is removed). Hosts and devices that set their own keep them.</p>,
    });
    if (ok) del.mutate(o.id);
  };
  return (
    <Card title="Set for Packs, hosts and devices">
      <details>
        <summary className="cursor-pointer text-sm">
          {ov.isLoading ? "Loading…" : ov.error ? "Couldn't load them" : rows.length === 0 ? "None: every host uses the settings above."
            : `${rows.length} setting${rows.length === 1 ? "" : "s"} for Packs, hosts and devices`} — show them, and the Packs' monitoring priorities
        </summary>
        <div className="mt-3 space-y-4">
          {(ov.error || del.error) && <Alert>{errorMessage(ov.error ?? del.error)}</Alert>}
          {rows.length > 0 && (
            <div className="overflow-x-auto">
              <table className="rtable w-full text-left text-sm">
                <caption className="sr-only">Health overrides</caption>
                <thead><tr className="border-b border-line text-xs text-muted">
                  <th className="px-2 py-1 font-medium">Where</th><th className="px-2 py-1 font-medium">Check</th>
                  <th className="px-2 py-1 font-medium">Set to</th><th className="px-2 py-1 font-medium">Changed</th><th className="px-2 py-1"><span className="sr-only">Actions</span></th>
                </tr></thead>
                <tbody>
                  {rows.map((o) => (
                    <tr key={o.id} className="border-b border-line last:border-0">
                      <td data-primary="" className="px-2 py-1.5">
                        {o.scope === "group" ? <>Pack <Link className="text-accent" to={`/groups/${o.group_id}`}>{o.group_name}</Link></> :
                          <Link className="text-accent" to={`/hosts/${o.host_id}/monitoring?tab=health`}>{o.host_name}</Link>}
                        {o.subject && <span className="text-muted"> · {o.subject_label || o.subject}</span>}
                      </td>
                      <td data-label="Check" className="px-2 py-1.5">{label(o.check)}</td>
                      <td data-label="Set to" className="px-2 py-1.5">{fieldsText(o)}</td>
                      <td data-label="Changed" className="px-2 py-1.5 text-xs text-muted">{o.updated_by || "system"} · {new Date(o.updated_at).toLocaleString()}</td>
                      <td data-label="" className="px-2 py-1.5 text-right">
                        {canEdit(o) && <Button size="sm" variant="ghost" disabled={del.isPending} onClick={() => void reset(o)} aria-label={`Reset ${label(o.check)} for ${o.group_name || o.host_name} to inherited`}>Reset…</Button>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div>
            <h3 className="mb-1 text-sm font-medium">Monitoring priority of each Pack</h3>
            <p className="mb-2 text-xs text-muted">
              Only matters when a host is in several Packs that set the same value differently: the higher priority wins. Equal
              priorities with different values are a conflict and use the global value until settled. Each is changed on its Pack&apos;s page.
            </p>
            <ul className="divide-y divide-line text-sm">
              {(groups.data?.groups ?? []).map((g) => (
                <li key={g.group_id} className="flex flex-wrap items-center gap-2 py-1.5">
                  <Link className="w-40 font-medium text-accent" to={`/groups/${g.group_id}`}>{g.name}</Link>
                  <span className="text-xs text-muted">{g.hosts} hosts · {g.overrides} set</span>
                  <span className="ml-auto text-xs">priority {g.priority}</span>
                </li>
              ))}
              {groups.isLoading ? <li className="py-1 text-muted">Loading…</li> : groups.error ? <li className="py-1"><Alert>{errorMessage(groups.error)}</Alert></li>
                : (groups.data?.groups ?? []).length === 0 && <li className="py-1 text-muted">No Packs yet.</li>}
            </ul>
          </div>
        </div>
      </details>
    </Card>
  );
}

/** A Pack's health checks; customizing one is the dialog's next step. */
export function GroupHealthDialog({ groupId, name, onClose }: { groupId: string; name: string; onClose: () => void }) {
  const q = useHealthGroup(groupId);
  const groups = useHealthGroups();
  const info = groups.data?.groups.find((g) => g.group_id === groupId);
  const [editing, setEditing] = useState<HealthCheck>();
  const k = { scope: "group" as const, group_id: groupId };
  return (
    <Dialog title={editing ? `${editing.label} for the Pack ${name}` : `Health checks: ${name}`} onClose={onClose} wide
      onBack={editing ? () => setEditing(undefined) : undefined} backLabel={`Back to ${name}'s health checks`} step={editing ? `edit:${editing.check}` : ""}>
      {editing ? <HealthEditorForm k={{ ...k, check: editing.check }} check={editing} onDone={() => setEditing(undefined)} onCancel={() => setEditing(undefined)} /> : (
        <>
          <p className="mb-3 text-sm text-muted">
            For every host in the Pack {name}, unless a host or one of its devices sets its own. What isn&apos;t set here comes from the global
            settings and the Recommended profile.
          </p>
          {info && (
            <p className="mb-3 text-xs text-muted">
              {info.hosts} hosts · monitoring priority {info.priority}: when two of a host&apos;s Packs set different values, the higher priority wins.
            </p>
          )}
          {q.error && <Alert>{errorMessage(q.error)}</Alert>}
          {q.data ? (
            <HealthChecksTable checks={q.data.checks} k={k} title={(c) => `${c.label} for the Pack ${name}`} onEdit={setEditing} />
          ) : !q.error && <p className="text-sm text-muted">Loading…</p>}
          <div className="mt-4"><Button onClick={onClose}>Close</Button></div>
        </>
      )}
    </Dialog>
  );
}


