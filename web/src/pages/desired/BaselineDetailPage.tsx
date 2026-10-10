import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { rolloutText } from "@/components/automation/Rollout";
import { RestoreDialog } from "@/components/desired/RestoreDialog";
import { DesiredStatusBadge, kindLabel } from "@/components/desired/ui";
import { describeKey, resourceKey, splitKey, useBaseline, useBaselineRevision, useDesiredHistory, useDesiredHosts, useInvalidateDesired, wantText, type DesiredResource } from "@/components/desired/queries";
import { errorMessage } from "@/lib/format";
import { appliesText } from "./BaselinesPage";
import { historyTitle } from "./HostDesiredPage";

/** One baseline: what it declares, where, how its hosts comply, its
 * revisions, and automatic restore. */
export function BaselineDetailPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const navigate = useNavigate();
  const b = useBaseline(id);
  const hosts = useDesiredHosts("", "", id);
  const hist = useDesiredHistory({ baseline: id });
  const invalidate = useInvalidateDesired();
  const [restore, setRestore] = useState(false);
  const [rev, setRev] = useState<number>();
  const archive = useMutation({
    mutationFn: () => unwrap(api.DELETE("/desired/baselines/{id}", { params: { path: { id } } })),
    onSuccess: () => { invalidate(); navigate("/desired/baselines"); },
  });
  const auto = useMutation({
    mutationFn: (enabled: boolean) => unwrap(api.PUT("/desired/baselines/{id}/auto", { params: { path: { id } }, body: { enabled, strategy: b.data?.auto.strategy } })),
    onSuccess: () => invalidate(),
  });
  if (b.error) return <Alert>{errorMessage(b.error)}</Alert>;
  const v = b.data;
  if (!v) return <p className="text-muted">Loading…</p>;
  const c = v.counts;
  return (
    <>
      <PageHeader
        crumbs={[{ label: "Desired State", to: "/desired" }, { label: "Baselines", to: "/desired/baselines" }]}
        title={v.name}
        description={<>{v.description && <>{v.description} · </>}Revision {v.revision} · applies to {appliesText(v)}{v.archived && " · archived"}</>}
        actions={
          <>
            {can("desired.restore") && c.drift + c.failed > 0 && !v.archived && <Button variant="primary" onClick={() => setRestore(true)}>Restore managed state…</Button>}
            {can("desired.manage") && !v.archived && <Link to={`/desired/baselines/${id}/edit`}><Button>Edit</Button></Link>}
            {can("desired.manage") && !v.archived && (
              <Button variant="ghost" onClick={() => { if (confirm(`Archive “${v.name}”? It stops applying to hosts; its revisions and history are kept.`)) archive.mutate(); }}>Archive</Button>
            )}
          </>
        }
      />
      <p className="-mt-3 mb-4 text-sm"><Link className={plainLink} to="/desired/baselines">← All baselines</Link></p>
      {archive.error && <Alert>{errorMessage(archive.error)}</Alert>}
      <div className="grid gap-4">
        {v.auto.attention && (
          <Alert tone="warn">
            <strong>Automatic restore paused — needs attention.</strong> {v.auto.attention}
            {can("desired.auto") && <> <Button size="sm" className="ml-2" onClick={() => auto.mutate(true)}>Resume automatic restore (as me)</Button></>}
          </Alert>
        )}
        <div className="grid gap-4 md:grid-cols-[2fr_1fr]">
          <Card title={`Declares (${v.resources.length})`}>
            <Table<DesiredResource>
              rows={v.resources}
              rowKey={(r) => resourceKey(r)}
              empty="Nothing yet."
              columns={[
                { header: "Kind", cell: (r) => <span className="text-xs uppercase tracking-wide text-muted">{kindLabel(r.kind)}</span> },
                { header: "Resource", cell: (r) => <span className="break-all font-mono text-xs">{splitKey(resourceKey(r)).name}</span> },
                { header: "Should be", cell: (r) => <span className="text-sm">{wantText(r)}</span> },
              ]}
            />
          </Card>
          <Card title="Compliance">
            <ul className="space-y-1 text-sm">
              <li>{c.hosts} host{c.hosts === 1 ? "" : "s"}, {c.resources} resource check{c.resources === 1 ? "" : "s"}</li>
              <li className={c.drift ? "text-warn" : "text-muted"}>{c.drift} drifted</li>
              <li className={c.conflict ? "text-danger" : "text-muted"}>{c.conflict} in conflict</li>
              <li className={c.failed ? "text-danger" : "text-muted"}>{c.failed} failed restore{c.failed === 1 ? "" : "s"}</li>
              <li className="text-muted">{c.unknown} unknown · {c.reconciling} restoring · {c.compliant} compliant</li>
            </ul>
            <h3 className="mb-1 mt-4 text-sm font-medium">Automatic restore</h3>
            {v.auto.enabled ? (
              <div className="space-y-1 text-sm">
                <p><Badge tone="accent">On</Badge> owned by {v.auto.owner?.name ?? "a deleted account"}</p>
                {v.auto.strategy && <p className="text-xs text-muted">Rollout: {rolloutText(v.auto.strategy)}.</p>}
                {can("desired.auto") && <Button size="sm" onClick={() => auto.mutate(false)} disabled={auto.isPending}>Turn off</Button>}
              </div>
            ) : (
              <div className="space-y-1 text-sm">
                <p className="text-muted">Off: drift is shown, and restored when someone chooses to.</p>
                {can("desired.auto") && !v.archived && <Link className={plainLink} to={`/desired/baselines/${id}/edit`}>Set up in Edit → Advanced</Link>}
              </div>
            )}
            {auto.error && <Alert>{errorMessage(auto.error)}</Alert>}
          </Card>
        </div>

        <Card title="Hosts">
          <Table
            rows={hosts.data?.items ?? []}
            rowKey={(h) => h.host_id}
            empty={hosts.isPending ? "Loading…" : "This baseline applies to no host yet."}
            columns={[
              { header: "Host", cell: (h) => <Link className={plainLink} to={`/hosts/${h.host_id}/desired`}>{h.name}</Link> },
              { header: "Status", cell: (h) => <DesiredStatusBadge status={h.status} /> },
              { header: "What differs", cell: (h) => <span className="text-sm text-muted">{h.problems.join(", ") || h.unknown || "—"}</span> },
              { header: "Checked", cell: (h) => <span className="text-muted">{h.checked_at ? fmtSince(h.checked_at) : "—"}</span> },
            ]}
          />
        </Card>

        <div className="grid gap-4 md:grid-cols-2">
          <Card title="Revisions">
            <ol className="space-y-1 text-sm">
              {(v.revisions ?? []).map((r) => (
                <li key={r.revision} className="flex flex-wrap items-center gap-2">
                  <button type="button" className={plainLink} onClick={() => setRev(r.revision)}>Revision {r.revision}</button>
                  {r.revision === v.revision && <Badge tone="accent">current</Badge>}
                  <span className="text-muted">{fmtTime(r.created_at)}{r.created_by ? ` by ${r.created_by}` : ""} · {r.resources} resource{r.resources === 1 ? "" : "s"}</span>
                </li>
              ))}
            </ol>
          </Card>
          <Card title="Recent changes">
            {(hist.data?.items ?? []).length === 0 ? <p className="text-sm text-muted">Nothing recorded yet.</p> : (
              <ol className="space-y-1 text-sm">
                {(hist.data?.items ?? []).slice(0, 10).map((e) => (
                  <li key={e.id}>
                    <span className="text-muted">{fmtTime(e.ts)}</span> · <Link className={plainLink} to={`/hosts/${e.host_id}/desired`}>{e.host_name}</Link>
                    {e.resource && <> · <span className="font-mono text-xs">{e.resource}</span></>} · <span className="font-medium">{historyTitle(e.kind, e.resolution)}</span>
                  </li>
                ))}
              </ol>
            )}
            <p className="mt-2 text-xs"><Link className={plainLink} to={`/desired/history?baseline=${id}`}>All history</Link></p>
          </Card>
        </div>
      </div>
      {restore && <RestoreDialog selection={{ baseline_id: id }} title={`Restore managed state: ${v.name}`} onClose={() => setRestore(false)} />}
      {rev && <RevisionDialog id={id} rev={rev} onClose={() => setRev(undefined)} />}
    </>
  );
}

function RevisionDialog({ id, rev, onClose }: { id: string; rev: number; onClose: () => void }) {
  const r = useBaselineRevision(id, rev);
  return (
    <Dialog title={`Revision ${rev}`} onClose={onClose} wide>
      {r.error && <Alert>{errorMessage(r.error)}</Alert>}
      {r.data && (
        <div className="space-y-3">
          <p className="text-sm text-muted">Saved {fmtTime(r.data.revision.created_at)}{r.data.revision.created_by ? ` by ${r.data.revision.created_by}` : ""}. Exactly as it was; revisions never change.</p>
          <p className="text-sm"><strong>{r.data.baseline.name}</strong> — applied to {appliesText(r.data.baseline)}</p>
          <ul className="space-y-1 text-sm">
            {r.data.baseline.resources.map((x) => (
              <li key={resourceKey(x)}><span className="font-mono text-xs">{describeKey(resourceKey(x))}</span>: {wantText(x)}</li>
            ))}
          </ul>
          <div className="flex justify-end"><Button type="button" onClick={onClose}>Close</Button></div>
        </div>
      )}
    </Dialog>
  );
}
