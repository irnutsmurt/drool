import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import type { Group } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { plainLink } from "@/components/ui/links";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { errorMessage } from "@/lib/format";
import { GroupHealthDialog } from "@/components/health/GlobalHealth";
import { useHealthGroup, useHealthGroups, usePreview, useSetGroupPriority } from "@/components/health/queries";
import { PriorityDialog } from "@/components/desired/GroupDesiredState";
import { useDesiredGroups } from "@/components/desired/queries";
import { useFwGroups } from "@/components/firewall/queries";
import { useApprovals } from "@/components/monitoring/queries";

type Deps = Schemas["PackDependencies"];
type Conflicts = Schemas["PackConflicts"];

export function usePackDependencies(id: string, enabled = true) {
  return useQuery({ queryKey: ["groups", id, "dependencies"], enabled,
    queryFn: () => unwrap(api.GET("/groups/{id}/dependencies", { params: { path: { id } } })) });
}

export function usePackConflicts(id: string, enabled = true) {
  return useQuery({ queryKey: ["groups", id, "conflicts"], enabled,
    queryFn: () => unwrap(api.GET("/groups/{id}/conflicts", { params: { path: { id } } })) });
}

/** What uses a Pack as a target, by kind, each shown only to roles that may
 * read its area (the list's names would tell them otherwise). */
const usedBy: { kind: string; label: string; one: string; perm: string }[] = [
  { kind: "schedule", label: "Schedules", one: "the schedule", perm: "schedules.read" },
  { kind: "maintenance", label: "Maintenance", one: "the maintenance", perm: "alerts.read" },
  { kind: "notification_route", label: "Notification routes", one: "the notification route", perm: "notifications.read" },
  { kind: "enrollment_token", label: "Shepherd enrollment tokens", one: "the Shepherd enrollment token", perm: "agents.read" },
  { kind: "network_rule", label: "Network Activity classifications", one: "the classification", perm: "network.read" },
];

const refs = (d: Deps | undefined, kind: string) => (d?.blocking ?? []).filter((b) => b.kind === kind);

function Row({ label, children, action }: { label: string; children: ReactNode; action?: ReactNode }) {
  return (
    <div className="grid gap-1 py-2.5 sm:grid-cols-[11rem_1fr_auto] sm:items-start sm:gap-3">
      <dt className="text-sm font-medium">{label}</dt>
      <dd className="min-w-0 text-sm">{children}</dd>
      {action && <dd className="sm:text-right">{action}</dd>}
    </div>
  );
}

const linkList = (items: { id: string; name: string; path: string }[]) =>
  items.map((r, i) => <span key={`${r.id}:${r.name}`}>{i > 0 && ", "}<Link className={plainLink} to={r.path}>{r.name}</Link></span>);

/** What a Pack sets for its hosts and what targets it, each where it's
 * changed (§3: one owner; this card owns the Pack's health checks, its
 * monitored services below, and its two priorities). */
export function PackSettingsCard({ pack }: { pack: Group }) {
  const { can } = useAuth();
  const deps = usePackDependencies(pack.id);
  const mon = can("monitoring.read");
  const hgs = useHealthGroups(mon);
  const hg = useHealthGroup(pack.id, mon);
  const ds = useDesiredGroups(can("desired.read"));
  const fw = useFwGroups(can("firewall.read"));
  const approvals = useApprovals(mon);
  const [dialog, setDialog] = useState<"" | "health" | "mon-priority" | "ds-priority">("");
  const info = hgs.data?.groups.find((g) => g.group_id === pack.id);
  const set = (hg.data?.checks ?? []).filter((c) => c.customized);
  const dg = can("desired.read") ? ds.data?.items.find((x) => x.group_id === pack.id) : undefined;
  const baselines = refs(deps.data, "desired_assignment");
  const policies = refs(deps.data, "firewall_policy");
  const timeout = fw.data?.items.find((g) => g.id === pack.id)?.rollback_timeout_s;
  const helpers = (approvals.data?.approvals ?? []).filter((a) => a.scope === "group" && a.group_id === pack.id).map((a) => a.package);
  const uses = usedBy.filter((u) => can(u.perm)).map((u) => ({ ...u, items: refs(deps.data, u.kind) })).filter((u) => u.items.length > 0);
  return (
    <Card title="What this Pack sets for its hosts" className="mb-4">
      {deps.error && <Alert>{errorMessage(deps.error)}</Alert>}
      <dl className="divide-y divide-line" data-testid="pack-settings">
        {mon && (
          <Row label="Health checks"
            action={<Button size="sm" onClick={() => setDialog("health")} aria-label={`Health checks of ${pack.name}`}>Health checks…</Button>}>
            {hg.isLoading ? <span className="text-muted">Loading…</span> : hg.error ? <span className="text-danger">Couldn&apos;t load them</span>
              : set.length ? <>Set here: {set.map((c) => c.label).join(", ")}</> : <span className="text-muted">None: its hosts use every host&apos;s health checks.</span>}
          </Row>
        )}
        {mon && (
          <Row label="Monitoring priority"
            action={can("monitoring.settings") && info && <Button size="sm" onClick={() => setDialog("mon-priority")} aria-label={`Change the monitoring priority of ${pack.name}`}>Change…</Button>}>
            {info ? <>{info.priority} <span className="text-muted">— decides when another of a host&apos;s Packs sets a health check differently: the higher wins.</span></>
              : <span className="text-muted">{hgs.isLoading ? "Loading…" : "—"}</span>}
          </Row>
        )}
        {can("desired.read") && (
          <Row label="Desired State"
            action={can("desired.manage") && dg && <Button size="sm" onClick={() => setDialog("ds-priority")} aria-label={`Change the Desired State priority of ${pack.name}`}>Change priority…</Button>}>
            {deps.isLoading ? <span className="text-muted">Loading…</span> : baselines.length ? <>Baselines: {linkList(baselines)}</> : <span className="text-muted">No baseline assigned to this Pack.</span>}
            {dg && <span className="block text-xs text-muted">Desired State priority {dg.priority}: decides when another of a host&apos;s Packs declares a resource differently.</span>}
          </Row>
        )}
        {can("firewall.read") && (
          <Row label="Firewall">
            {deps.isLoading ? <span className="text-muted">Loading…</span> : policies.length
              ? <>Rules naming it: {linkList(policies)}</> : <span className="text-muted">No firewall rule names this Pack.</span>}
            <span className="block text-xs text-muted">
              Rollback timeout: {fw.isLoading ? "…" : timeout ? `${timeout} s for this Pack` : "the default"} (<Link className={plainLink} to="/firewall?tab=settings">Firewall settings</Link>).
            </span>
          </Row>
        )}
        {mon && helpers.length > 0 && (
          <Row label="Monitoring helpers">
            {helpers.join(", ")}: installed on its hosts where they add detail, without asking (<Link className={plainLink} to="/monitoring/settings?advanced=1">helper approvals</Link>).
          </Row>
        )}
        {uses.length > 0 && (
          <Row label="Targeted by">
            <ul className="space-y-1">
              {uses.map((u) => <li key={u.kind}><span className="text-muted">{u.label}:</span> {linkList(u.items)}</li>)}
            </ul>
          </Row>
        )}
      </dl>
      {dialog === "health" && <GroupHealthDialog groupId={pack.id} name={pack.name} onClose={() => setDialog("")} />}
      {dialog === "mon-priority" && info && <MonitoringPriorityDialog groupId={pack.id} name={pack.name} current={info.priority} onClose={() => setDialog("")} />}
      {dialog === "ds-priority" && dg && <PriorityDialog groupId={pack.id} name={pack.name} current={dg.priority} onClose={() => setDialog("")} />}
    </Card>
  );
}

/** A Pack's monitoring priority, with what changes before saving. */
export function MonitoringPriorityDialog({ groupId, name, current, onClose }: { groupId: string; name: string; current: number; onClose: () => void }) {
  const [v, setV] = useState(String(current));
  const set = useSetGroupPriority();
  const preview = usePreview();
  const n = Number(v);
  const ok = v.trim() !== "" && Number.isInteger(n) && n >= -1000 && n <= 1000;
  const changed = ok && n !== current;
  useEffect(() => {
    if (!changed) return;
    const t = setTimeout(() => preview.mutate({ scope: "group", group_id: groupId, check: "cpu", fields: {}, priority: n }), 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [v]);
  return (
    <Dialog title={`Monitoring priority: ${name}`} onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); set.mutate({ id: groupId, priority: n }, { onSuccess: onClose }); }}>
        <p className="text-sm text-muted">
          When another of a host&apos;s Packs sets a health check differently, the higher priority wins. Equal priorities that disagree are a
          conflict: DROOL picks neither and uses every host&apos;s setting. A host&apos;s own setting always wins.
        </p>
        <Field label="Priority" hint="-1000 to 1000. Default 0.">
          <Input type="number" min={-1000} max={1000} value={v} onChange={(e) => setV(e.target.value)} data-autofocus />
        </Field>
        {changed && preview.data && <p className="text-sm" aria-live="polite">{preview.data.summary}.</p>}
        {(preview.error || set.error) && <Alert>{errorMessage(preview.error ?? set.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!changed || set.isPending}>{set.isPending ? "Saving…" : "Save"}</Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Where this Pack's settings don't decide what its hosts get, because of
 * another Pack (the server's verdicts): said plainly, with what applies
 * instead and the way to settle it. */
export function PackConflictsCard({ pack }: { pack: Group }) {
  const { can } = useAuth();
  const q = usePackConflicts(pack.id, pack.host_ids.length > 0);
  const set = useSetGroupPriority();
  const preview = usePreview();
  if (pack.host_ids.length === 0) return null;
  const c: Conflicts | undefined = q.data;
  const win = async (others: Conflicts["items"][number]["others"]) => {
    const n = Math.max(...others.map((o) => o.priority)) + 1;
    const names = others.map((o) => o.name).join(" and ");
    const pv = await preview.mutateAsync({ scope: "group", group_id: pack.id, check: "cpu", fields: {}, priority: n }).catch(() => undefined);
    const ok = await confirmAction({
      title: `Let ${pack.name} win over ${names}?`, confirmLabel: `Set ${pack.name}'s monitoring priority to ${n}`,
      body: (
        <div className="space-y-2">
          <p>{pack.name}&apos;s monitoring priority becomes {n}, above {names}: where they set a health check differently, {pack.name}&apos;s applies, on every host in both.</p>
          <p className="text-sm text-muted">{pv ? `${pv.summary}.` : "The impact couldn't be worked out in advance."}</p>
        </div>
      ),
    });
    if (ok) set.mutate({ id: pack.id, priority: n }, { onSuccess: () => void q.refetch() });
  };
  const missing = ["monitoring", "desired"].filter((a) => !(c?.checked ?? []).includes(a as never));
  return (
    <Card title="Where another Pack decides" className="mb-4">
      {q.error ? <Alert>{errorMessage(q.error)}</Alert> : q.isLoading ? <p className="text-sm text-muted">Loading…</p> : c && c.items.length === 0 ? (
        <p className="text-sm text-muted" data-testid="pack-conflicts-none">
          None: this Pack&apos;s settings apply on every host in it, unless the host sets its own.
          {missing.length > 0 && ` (${missing.map((m) => (m === "desired" ? "Desired State" : "Monitoring")).join(" and ")} not checked: your role can't see it.)`}
        </p>
      ) : (
        <ul className="divide-y divide-line text-sm" data-testid="pack-conflicts-list">
          {(c?.items ?? []).map((x) => (
            <li key={`${x.host_id}:${x.area}:${x.what}`} className="space-y-1 py-2">
              <p>
                <Link className={plainLink} to={x.area === "desired" ? `/hosts/${x.host_id}/desired` : `/hosts/${x.host_id}/monitoring?tab=health`}>{x.host_name}</Link>
                {" · "}<b>{x.what}</b>
                {x.area === "desired" && <span className="text-muted"> (Desired State)</span>}
              </p>
              <p className="text-muted">
                {x.kind === "conflict" ? (
                  <>This Pack{x.value ? ` (${x.value})` : ""} and {x.others.map((o) => `${o.name}${o.value ? ` (${o.value})` : ""}`).join(" and ")} disagree at
                    equal priority ({x.others[0]?.priority ?? 0}): {x.applies} applies.</>
                ) : (
                  <>This Pack sets {x.value}; {x.others.map((o) => `${o.name} (priority ${o.priority})`).join(" and ")} outrank{x.others.length === 1 ? "s" : ""} it: {x.applies} applies.</>
                )}
              </p>
              {x.area === "monitoring" && can("monitoring.settings") && (
                <Button size="sm" disabled={set.isPending || preview.isPending} onClick={() => void win(x.others)}>Let {pack.name} win…</Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {set.error && <Alert>{errorMessage(set.error)}</Alert>}
    </Card>
  );
}

/** What hosts get by joining a Pack, in a few lines (Add hosts): the same
 * sources as the Pack's settings card. Conflicts with a host's other Packs
 * show on the Pack's page once they're in. */
export function PackJoinSummary({ pack }: { pack: Group }) {
  const { can } = useAuth();
  const mon = can("monitoring.read");
  const deps = usePackDependencies(pack.id);
  const hg = useHealthGroup(pack.id, mon);
  const services = useQuery({
    queryKey: ["monitoring", "expected-services", "group", pack.id], enabled: mon,
    queryFn: () => unwrap(api.GET("/monitoring/health/overrides", { params: { query: { scope: "group", group_id: pack.id, check: "service_expected" } } })),
  });
  if (deps.isLoading || (mon && (hg.isLoading || services.isLoading))) return <p className="text-sm text-muted">Working out what they get…</p>;
  const lines: ReactNode[] = [];
  const set = (hg.data?.checks ?? []).filter((c) => c.customized).map((c) => c.label);
  if (mon) lines.push(set.length ? <>Health checks set by the Pack: {set.join(", ")}</> : <>No health checks of its own: every host&apos;s apply</>);
  const watched = (services.data?.items ?? []).filter((o) => o.fields.expected).map((o) => o.subject_label || o.subject || "");
  if (mon && watched.length) lines.push(<>Monitored services: {watched.join(", ")}</>);
  const baselines = refs(deps.data, "desired_assignment").map((b) => b.name);
  if (can("desired.read") && baselines.length) lines.push(<>Desired State: {baselines.join(", ")} (a baseline with automatic restore on changes a host to match)</>);
  const rules = refs(deps.data, "firewall_policy").length;
  if (can("firewall.read") && rules) lines.push(<>{rules} firewall rule{rules === 1 ? "" : "s"} naming the Pack (applied when someone reviews and applies the policy)</>);
  const targets = usedBy.filter((u) => can(u.perm)).flatMap((u) => refs(deps.data, u.kind).map((r) => `${u.one} ${r.name}`));
  if (targets.length) lines.push(<>Included in: {targets.join(", ")}</>);
  return (
    <div className="rounded-ui border border-line p-3 text-sm" data-testid="pack-join-summary">
      <p className="font-medium">What they get from {pack.name}</p>
      <ul className="mt-1 list-disc space-y-0.5 pl-5">{lines.map((l, i) => <li key={i}>{l}</li>)}</ul>
      <p className="mt-1 text-xs text-muted">A host&apos;s own settings still win. Where one of its other Packs disagrees, this Pack&apos;s page says so.</p>
    </div>
  );
}
