import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { RestoreDialog } from "@/components/desired/RestoreDialog";
import { AcceptDialog, ReviewDialog } from "@/components/desired/ReviewDialog";
import { DesiredStatusBadge, DiffTable, kindLabel, statusLabel } from "@/components/desired/ui";
import { desiredLayers, desiredScopes } from "@/components/desired/provenance";
import { Provenance, ScopeLegend } from "@/components/ui/Provenance";
import { describeKey, useDesiredHost, useDesiredResource, useInvalidateDesired, type DesiredResourceView } from "@/components/desired/queries";
import { errorMessage } from "@/lib/format";

/** A host's effective desired state: every resource, where its value comes
 * from, what differs, and the actions on it. */
export function HostDesiredPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const host = useDesiredHost(id);
  const invalidate = useInvalidateDesired();
  const [open, setOpen] = useState<string>();
  const [restore, setRestore] = useState<string[] | null>(null);
  const [review, setReview] = useState<string>();
  const [accept, setAccept] = useState<DesiredResourceView[] | null>(null);
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/desired/hosts/{id}/refresh", { params: { path: { id } } })),
    onSuccess: () => { setTimeout(invalidate, 1500); setTimeout(invalidate, 5000); },
  });
  if (host.error) return <Alert>{errorMessage(host.error)}</Alert>;
  const h = host.data;
  if (!h) return <p className="text-muted">Loading…</p>;
  const restorable = h.resources.filter((r) => r.restorable);
  return (
    <>
      <PageHeader
        crumbs={[{ label: "Hosts", to: "/hosts" }, { label: h.name, to: `/hosts/${id}` }]}
        title={`${h.name}: desired state`}
        description={
          <span className="inline-flex flex-wrap items-center gap-2">
            <DesiredStatusBadge status={h.status} />
            {h.counts.total > 0 && <span>{h.counts.compliant} of {h.counts.total} resources compliant</span>}
          </span>
        }
        actions={
          <>
            {can("desired.restore") && h.counts.total > 0 && (
              <Button onClick={() => refresh.mutate()} disabled={refresh.isPending}>{refresh.isPending ? "Checking…" : "Check now"}</Button>
            )}
            {can("desired.restore") && restorable.length > 0 && (
              <Button variant="primary" onClick={() => setRestore(restorable.map((r) => r.key))}>Restore managed state…</Button>
            )}
          </>
        }
      />
      {h.maintenance && <div className="mb-4"><Alert tone="warn">This host is in maintenance ({h.maintenance}). DROOL keeps checking it and shows drift; automatic restore waits until maintenance ends. Restoring by hand is still possible.</Alert></div>}
      {h.path.blocked && <div className="mb-4"><Alert>DROOL can't change this host: {h.path.reason}</Alert></div>}
      {h.path.agent_state === "epoch_mismatch" && <div className="mb-4"><Alert>The agent belongs to another controller epoch; desired state isn't restored here until it is reconciled on the Agents page.</Alert></div>}
      <div className="grid gap-4">
        <Card title="Where this host's desired state comes from">
          {h.baselines.length === 0 ? (
            <p className="text-sm text-muted">No baseline applies to this host. {can("desired.manage") && <Link className={plainLink} to="/desired/baselines/new">Create a baseline</Link>}</p>
          ) : (
            <ul className="space-y-1 text-sm">
              {h.baselines.map((b) => (
                <li key={b.id + b.via}><Link className={plainLink} to={`/desired/baselines/${b.id}`}>{b.name}</Link> <span className="text-muted">rev. {b.revision}, via {b.via.replace(/^group /, "pack ")}</span></li>
              ))}
            </ul>
          )}
          {h.groups.length > 0 && (
            <p className="mt-2 text-xs text-muted">
              Packs (higher priority wins when they disagree): {h.groups.map((g) => `${g.name} (${g.priority})`).join(", ")}.{" "}
              <Link className={plainLink} to="/groups">Change priorities</Link>
            </p>
          )}
          <div className="mt-2"><ScopeLegend scopes={desiredScopes} /></div>
          <p className="mt-2 text-xs text-muted">
            Packages and files are
            checked every {h.check_every_s >= 120 ? `${Math.round(h.check_every_s / 60)} min` : `${h.check_every_s} s`}
            {h.packages_at || h.files_at ? ` (last ${fmtSince(Math.max(h.packages_at ?? 0, h.files_at ?? 0))})` : ""}; services come from monitoring
            {h.services_at ? ` (last report ${fmtSince(h.services_at)})` : ""}.
            {h.packages_note && <> Package check: {h.packages_note}.</>}
            {h.files_note && <> File check: {h.files_note}.</>}
          </p>
          {h.firewall && (
            <p className="mt-2 text-sm" aria-label="Firewall policy">
              <span className="font-medium">Firewall policy</span> — managed by <Link className={plainLink} to="/firewall">Firewall</Link>:{" "}
              {h.firewall.drift ? <span className="text-warn">drift: {h.firewall.drift}</span>
                : h.firewall.enforced ? <span className="text-muted">enforced, no drift</span>
                  : <span className="text-muted">{h.firewall.mode === "off" ? "not managed" : `${h.firewall.mode} mode, not enforced`}</span>}
            </p>
          )}
        </Card>

        <Card title={h.resources.length ? `Managed resources (${h.resources.length})` : "Managed resources"}>
          {h.resources.length === 0 ? <p className="text-sm text-muted">Nothing is managed on this host.</p> : (
            <ul className="divide-y divide-line" aria-label="Managed resources">
              {h.resources.map((r) => (
                <li key={r.key}>
                  <button type="button" aria-expanded={open === r.key} onClick={() => setOpen(open === r.key ? undefined : r.key)}
                    className="grid w-full grid-cols-1 gap-1 py-2 text-left hover:bg-surface-2 sm:grid-cols-[6rem_minmax(10rem,1.3fr)_minmax(9rem,1fr)_minmax(9rem,1fr)_9rem] sm:items-center sm:gap-3">
                    <span className="text-xs uppercase tracking-wide text-muted">{kindLabel(r.kind)}</span>
                    <span className="break-all font-mono text-xs" aria-label={describeKey(r.key)}>{r.name}</span>
                    <span className="text-sm"><span className="text-muted">wants </span>{r.want}</span>
                    <span className="text-sm"><span className="text-muted">is </span>{r.current || "—"}</span>
                    <span className="flex flex-wrap items-center gap-1"><DesiredStatusBadge status={r.status} />{r.auto.enabled && <Badge tone="accent">auto</Badge>}</span>
                  </button>
                  {open === r.key && (
                    <ResourceDetail hostId={id} r={r} onRestore={() => setRestore([r.key])} onReview={() => setReview(r.key)}
                      onAccept={() => setAccept([r])} />
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>

        {h.excluded.length > 0 && (
          <Card title="Not managed on this host">
            <ul className="space-y-1 text-sm">
              {h.excluded.map((x) => (
                <li key={x.key} className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-xs">{describeKey(x.key)}</span>
                  <span className="text-muted">{x.source.override?.note || x.source.label}</span>
                  {can("desired.manage") && <ResetButton hostId={id} k={x.key} />}
                </li>
              ))}
            </ul>
          </Card>
        )}
      </div>
      {restore && <RestoreDialog selection={{ host_ids: [id], keys: restore }} title={`Restore managed state on ${h.name}`} onClose={() => setRestore(null)} />}
      {review && <ReviewDialog hostId={id} k={review} onClose={() => setReview(undefined)} />}
      {accept && <AcceptDialog hostId={id} hostName={h.name} resources={accept} onClose={() => setAccept(null)} />}
    </>
  );
}

function ResetButton({ hostId, k }: { hostId: string; k: string }) {
  const invalidate = useInvalidateDesired();
  const reset = useMutation({
    mutationFn: () => unwrap(api.DELETE("/desired/hosts/{id}/overrides", { params: { path: { id: hostId }, query: { key: k } } })),
    onSuccess: () => invalidate(),
  });
  return (
    <>
      <Button size="sm" type="button" disabled={reset.isPending}
        onClick={() => { if (confirm(`Reset ${describeKey(k)} to its inherited desired state? The host may then show drift.`)) reset.mutate(); }}>
        Reset to inherited
      </Button>
      {reset.error && <span className="text-xs text-danger">{errorMessage(reset.error)}</span>}
    </>
  );
}

/** A resource's details: what differs, why, where the value comes from,
 * history, and the actions. */
function ResourceDetail({ hostId, r, onRestore, onReview, onAccept }: {
  hostId: string;
  r: DesiredResourceView;
  onRestore: () => void;
  onReview: () => void;
  onAccept: () => void;
}) {
  const { can } = useAuth();
  const detail = useDesiredResource(hostId, r.key);
  const hist = detail.data?.history ?? [];
  const src = r.source;
  const baseline = src.baselines?.[0];
  return (
    <div className="mb-3 space-y-4 rounded-ui border border-line bg-surface-2/40 p-3">
      <div className="flex flex-wrap gap-2">
        <Button size="sm" type="button" onClick={onReview}>Review difference</Button>
        {can("desired.restore") && (
          <Button size="sm" type="button" variant="primary" disabled={!r.restorable} onClick={onRestore}
            title={r.restorable ? undefined : r.blocker || "Nothing to restore"}>
            Restore managed state
          </Button>
        )}
        {can("desired.manage") && (
          <Button size="sm" type="button" disabled={!r.acceptable} onClick={onAccept}
            title={r.acceptable ? undefined : "Only something DROOL can verify, and that differs, can be accepted"}>
            Accept current state
          </Button>
        )}
        {can("desired.manage") && baseline && (
          <Link to={`/desired/baselines/${baseline.id}/edit?focus=${encodeURIComponent(r.key)}`}>
            <Button size="sm" type="button">Change baseline</Button>
          </Link>
        )}
        {can("desired.manage") && src.level === "host" && <ResetButton hostId={hostId} k={r.key} />}
      </div>
      {r.status === "conflict" && (
        <Alert tone="warn">
          <strong>{src.label}.</strong> DROOL doesn't pick one: this resource isn't checked against either value, restored or restored
          automatically until the conflict is resolved — give one pack a higher priority, change a baseline, or accept the current
          state for this host.
        </Alert>
      )}
      {r.status !== "compliant" && r.status !== "conflict" && r.note && (
        <p className={cx("text-sm", r.status === "failed" ? "text-danger" : "text-muted")}>{r.note}</p>
      )}
      {r.status === "reconciling" && r.job_id && <p className="text-sm">A correction is in progress: <Link className={plainLink} to={`/jobs/${r.job_id}`}>job</Link>.</p>}
      {r.diffs.length > 0 && <DiffTable diffs={r.diffs} />}
      <div className="grid gap-3 text-sm sm:grid-cols-2">
        <div>
          <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">Where the desired value comes from</h4>
          <Provenance label="Where the desired value comes from" layers={desiredLayers(r).map((l) => ({
            key: l.key,
            scope: l.scope,
            value: l.value,
            state: l.state,
            detail: (
              <>
                {l.baselines.map((b, i) => <span key={b.id}>{i > 0 && ", "}<Link className={plainLink} to={`/desired/baselines/${b.id}`}>{b.name}</Link> rev. {b.revision}</span>)}
                {l.level === "host" && l.state === "effective" && src.override && (
                  <>Set {src.override.origin === "accept" ? "by Accept current state" : "for this host"}{src.override.updated_by ? ` by ${src.override.updated_by}` : ""}{src.override.note ? `: “${src.override.note}”` : ""}</>
                )}
              </>
            ),
          }))}
            note={r.status === "conflict" ? "Equal priorities that disagree: DROOL doesn't pick one." : src.label} />
        </div>
        <div>
          <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">Automatic restore</h4>
          {r.auto.enabled ? (
            <p>
              On ({r.auto.baseline?.name}).{r.auto.paused && <span className="text-warn"> Paused — needs attention: {r.auto.paused}.</span>}
              {!r.auto.paused && r.auto.attempts > 0 && <span className="text-muted"> {r.auto.attempts} failed attempt{r.auto.attempts === 1 ? "" : "s"}{r.auto.next_at ? `; next not before ${fmtTime(r.auto.next_at)}` : ""}.</span>}
            </p>
          ) : <p className="text-muted">Off: drift is shown, and restored when someone chooses to.</p>}
        </div>
      </div>
      <div>
        <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">History</h4>
        {hist.length === 0 ? <p className="text-sm text-muted">No changes recorded yet.</p> : (
          <ol className="space-y-1 text-sm" aria-label="History">
            {hist.slice(0, 12).map((e) => (
              <li key={e.id} className="flex flex-wrap gap-x-2">
                <span className="text-muted">{fmtTime(e.ts)}</span>
                <span className="font-medium">{historyTitle(e.kind, e.resolution)}</span>
                <span className="text-muted">{e.detail}</span>
                {e.actor && <span className="text-muted">— {e.actor}</span>}
                {e.job_id && <Link className={plainLink} to={`/jobs/${e.job_id}`}>job</Link>}
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );
}

const historyTitles: Record<string, string> = {
  drift_detected: "Drift detected",
  drift_changed: "Drift changed",
  compliant: "Compliant again",
  resolved_by_configuration: "Resolved by configuration",
  accepted: "Current state accepted",
  no_longer_managed: "No longer managed",
  conflict_detected: "Conflict",
  conflict_resolved: "Conflict resolved",
  unknown: "Unknown",
  observed: "Observed again",
  restore_requested: "Restore requested",
  restored: "Restored",
  restore_failed: "Restore failed",
  restore_unverified: "Restore not verified",
};

export function historyTitle(kind: string, resolution?: string): string {
  const t = historyTitles[kind] ?? statusLabel(kind);
  if (kind === "compliant" && resolution === "observed") return "Compliant again (changed on the host)";
  return t;
}
