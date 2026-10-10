import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatAgo, formatDateTime } from "@/lib/format";

type HostRec = Schemas["HostReconciliation"];
type State = HostRec["state"];

export const restoreKey = ["system", "restore"] as const;

export function useRestoreStatus(enabled = true) {
  return useQuery({
    queryKey: restoreKey,
    queryFn: () => unwrap(api.GET("/system/restore")),
    enabled,
    refetchInterval: (q) => (q.state.data?.restore?.status === "reconciling" ? 5000 : 60_000),
  });
}

/** Plain-language names for reconciliation states (ARCHITECTURE-ALPHA §33.1). */
export const stateInfo: Record<State, { label: string; tone: "ok" | "warn" | "danger" | "neutral"; help: string }> = {
  recovered: { label: "Recovered", tone: "ok", help: "Observed again; nothing versioned to reconcile." },
  reconciled: { label: "Reconciled", tone: "ok", help: "Compared with the restored data and moved to this controller's epoch." },
  pending: { label: "Waiting", tone: "neutral", help: "Not compared yet: its Shepherd hasn't connected, or it is next in line." },
  unreachable: { label: "Unreachable", tone: "warn", help: "Couldn't be checked; DROOL tries again." },
  ahead: { label: "Ahead of DROOL", tone: "warn", help: "Changed after the backup was taken. Nothing is rolled back: accept its state, or re-enroll." },
  attention: { label: "Needs attention", tone: "warn", help: "Disagrees with the restored data in a way DROOL won't guess about." },
  incompatible: { label: "Incompatible", tone: "warn", help: "Its Shepherd can't move to the new epoch; update it." },
  blocked: { label: "Blocked for safety", tone: "danger", help: "Not reconciled until the cause is resolved." },
};

const order: State[] = ["blocked", "attention", "ahead", "incompatible", "unreachable", "pending", "reconciled", "recovered"];

function canAccept(h: HostRec) {
  return h.state === "ahead" || h.state === "attention" || (h.state === "blocked" &&
    (h.reason.startsWith("its Shepherd's identity is now presented by another machine") || h.reason.startsWith("its Shepherd reports that a controller revoked its identity")));
}

function domainText(d: Schemas["ReconciliationDomain"]) {
  const name = d.domain === "agent-config" ? "Shepherd configuration" : d.domain === "files" ? "file deployments" : d.domain;
  switch (d.verdict) {
    case "same": return `${name}: identical (revision ${d.host_revision})`;
    case "controller_ahead": return `${name}: DROOL has newer (revision ${d.controller_revision}, host ${d.host_revision})`;
    case "host_ahead": return `${name}: host has newer (revision ${d.host_revision}, DROOL ${d.controller_revision})`;
    case "conflict": return `${name}: same revision ${d.host_revision}, different content`;
    case "foreign_epoch": return `${name}: from another controller epoch`;
    case "accepted": return `${name}: host's state accepted (revision ${d.host_revision})`;
  }
}

/** Reconciliation after a restore (Settings → System). Hidden on a
 * controller that was never restored. */
export function RestoreReconciliationCard() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const st = useRestoreStatus(can("settings.read"));
  const [all, setAll] = useState(false);
  const invalidate = () => void qc.invalidateQueries({ queryKey: restoreKey });
  const accept = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/hosts/{id}/reconciliation/accept", { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const retry = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/hosts/{id}/reconciliation/retry", { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const finish = useMutation({
    mutationFn: () => unwrap(api.POST("/system/restore/finish")),
    onSuccess: (d) => qc.setQueryData(restoreKey, d),
  });
  const d = st.data;
  if (!d?.restore) return st.error ? <Alert>{errorMessage(st.error)}</Alert> : null;
  const r = d.restore;
  const open = d.hosts.filter((h) => h.state !== "reconciled" && h.state !== "recovered");
  const rows = all ? d.hosts : open;
  const u = d.unknown_outcome;
  const held = d.schedules.filter((s) => s.held);
  const paused = d.baselines.filter((b) => b.held);
  const err = accept.error ?? retry.error ?? finish.error;
  return (
    <Card title="Restore" actions={r.status === "reconciling"
      ? <Badge tone="warn">Reconciling</Badge>
      : <Badge tone="ok">Finished</Badge>}>
      <div className="space-y-4 text-sm" data-testid="restore-reconciliation">
        {err && <Alert>{errorMessage(err)}</Alert>}
        <p>
          Restored {formatAgo(r.restored_at)} from a backup made {formatDateTime(r.backup_created_at)}
          {r.backup_host ? <> on {r.backup_host}</> : null}{r.backup_version ? <> (DROOL {r.backup_version})</> : null}
          {r.restored_by ? <>, by {r.restored_by}</> : null}.
          {r.status === "finished" && r.finished_by ? <> Finished {r.finished_at ? formatAgo(r.finished_at) : ""} ({r.finished_by}).</> : null}
        </p>
        <p className="text-muted">
          The restored controller has a new controller epoch. Each host is compared with the restored data and moved to it only when
          nothing on the host would be lost or rolled back; until then it refuses this controller's commands. Nothing that was in
          progress when the backup was taken is repeated.
        </p>

        <ul className="flex flex-wrap gap-2" aria-label="Hosts by reconciliation state">
          {order.filter((s) => d.counts[s]).map((s) => (
            <li key={s} title={stateInfo[s].help}><Badge tone={stateInfo[s].tone}>{d.counts[s]} {stateInfo[s].label.toLowerCase()}</Badge></li>
          ))}
        </ul>

        {(u.jobs + u.firewall_transactions + u.notifications + u.queued_runs > 0) && (
          <div>
            <h3 className="mb-1 font-medium">Unknown outcome</h3>
            <p className="mb-1 text-muted">Unfinished when the backup was taken. What happened afterwards isn't known here, so none of it is repeated or marked successful.</p>
            <ul className="list-disc space-y-0.5 pl-5">
              {u.jobs > 0 && <li>{u.jobs} job{u.jobs === 1 ? "" : "s"} ({u.job_hosts} host run{u.job_hosts === 1 ? "" : "s"}) marked interrupted — <Link className={plainLink} to="/jobs">Jobs</Link></li>}
              {u.firewall_transactions > 0 && <li>{u.firewall_transactions} firewall transaction{u.firewall_transactions === 1 ? "" : "s"} closed as unknown — <Link className={plainLink} to="/firewall">Firewall</Link></li>}
              {u.notifications > 0 && <li>{u.notifications} notification{u.notifications === 1 ? "" : "s"} not sent (the previous controller may have sent {u.notifications === 1 ? "it" : "them"})</li>}
              {u.queued_runs > 0 && <li>{u.queued_runs} queued scheduled run{u.queued_runs === 1 ? "" : "s"} skipped</li>}
            </ul>
          </div>
        )}

        {(d.schedules.length > 0 || d.baselines.length > 0 || d.agent_rollouts_paused > 0) && (
          <div>
            <h3 className="mb-1 font-medium">Held until you resume it</h3>
            <ul className="list-disc space-y-0.5 pl-5">
              {d.schedules.map((s) => (
                <li key={s.id}>Schedule <Link className={plainLink} to={`/jobs/schedules/${s.id}`}>{s.name}</Link>{" "}
                  {s.held ? <Badge tone="warn">held</Badge> : <Badge tone="ok">resumed</Badge>}</li>
              ))}
              {d.baselines.map((b) => (
                <li key={b.id}>Automatic restore of <Link className={plainLink} to={`/desired/baselines/${b.id}`}>{b.name}</Link>{" "}
                  {b.held ? <Badge tone="warn">paused</Badge> : <Badge tone="ok">resumed</Badge>}</li>
              ))}
              {d.agent_rollouts_paused > 0 && <li>{d.agent_rollouts_paused} Shepherd update rollout{d.agent_rollouts_paused === 1 ? "" : "s"} paused — <Link className={plainLink} to="/agents?tab=updates">Shepherds → Updates</Link></li>}
            </ul>
            {(held.length > 0 || paused.length > 0) && (
              <p className="mt-1 text-muted">Schedules count from when they're enabled again; runs due while they were held are not made up.</p>
            )}
          </div>
        )}

        <div>
          <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
            <h3 className="font-medium">{all ? "All hosts" : open.length ? "Hosts not reconciled yet" : "Every host is reconciled"}</h3>
            {d.hosts.length > open.length && (
              <Button size="sm" variant="ghost" onClick={() => setAll(!all)}>{all ? "Show only hosts left" : `Show all ${d.hosts.length} hosts`}</Button>
            )}
          </div>
          {rows.length > 0 && (
            <Table<HostRec>
              rows={rows}
              rowKey={(h) => h.host_id}
              columns={[
                { header: "Host", cell: (h) => <Link className={plainLink} to={`/hosts/${h.host_id}`}>{h.host_name}</Link> },
                { header: "State", cell: (h) => <span title={stateInfo[h.state].help}><Badge tone={stateInfo[h.state].tone}>{stateInfo[h.state].label}</Badge></span> },
                {
                  header: "Details", cell: (h) => (
                    <div className="space-y-1">
                      {h.reason && <p>{h.reason}</p>}
                      {h.domains.length > 0 && (
                        <ul className="text-xs text-muted">{h.domains.map((x) => <li key={x.domain}>{domainText(x)}</li>)}</ul>
                      )}
                      {h.decided_by && <p className="text-xs text-muted">Host's state accepted by {h.decided_by}.</p>}
                      {h.next_attempt_at && <p className="text-xs text-muted">Next check {formatAgo(h.next_attempt_at)}.</p>}
                    </div>
                  ),
                },
                {
                  header: "", cell: (h) => can("settings.write") && h.state !== "reconciled" && h.state !== "recovered" ? (
                    <div className="flex flex-wrap gap-2">
                      {canAccept(h) && (
                        <Button size="sm" onClick={() => {
                          if (confirm(`Accept ${h.host_name}'s state as it is? Nothing on the host changes; it is moved to this controller's epoch with what it has, and DROOL's own configuration then shows where it differs.`)) accept.mutate(h.host_id);
                        }} disabled={accept.isPending}>Accept host's state…</Button>
                      )}
                      <Button size="sm" variant="ghost" onClick={() => retry.mutate(h.host_id)} disabled={retry.isPending}>Check again</Button>
                    </div>
                  ) : null,
                },
              ]}
            />
          )}
        </div>

        {r.status === "reconciling" && can("settings.write") && (
          <div className="flex flex-wrap items-center gap-3">
            <Button onClick={() => {
              if (confirm(open.length
                ? `${open.length} host${open.length === 1 ? " is" : "s are"} not reconciled. They keep refusing this controller's commands until they are, and are still compared when their Shepherds connect. Finish anyway?`
                : "Finish the reconciliation?")) finish.mutate();
            }} disabled={finish.isPending}>Finish reconciliation…</Button>
            <span className="text-muted">It finishes by itself once every host is reconciled.</span>
          </div>
        )}
      </div>
    </Card>
  );
}

/** A host's reconciliation on its page, while it needs something. */
export function HostReconciliationNotice({ hostId }: { hostId: string }) {
  const { can } = useAuth();
  const q = useQuery({
    queryKey: ["hosts", hostId, "reconciliation"],
    queryFn: async (): Promise<HostRec | null> => {
      const r = await api.GET("/hosts/{id}/reconciliation", { params: { path: { id: hostId } } });
      if (r.response.status === 404) return null; // never restored, or not part of it
      return unwrap(Promise.resolve(r));
    },
    enabled: can("hosts.read"),
    refetchInterval: (s) => (s.state.data && s.state.data.state !== "reconciled" && s.state.data.state !== "recovered" ? 5000 : false),
  });
  const h = q.data;
  if (!h || h.state === "reconciled" || h.state === "recovered") return null;
  const info = stateInfo[h.state];
  return (
    <Alert tone={info.tone === "danger" ? "danger" : "warn"}>
      <strong>Restore reconciliation: {info.label.toLowerCase()}.</strong> {h.reason || info.help}{" "}
      Until this host is reconciled, DROOL sends it nothing.{" "}
      {can("settings.read") && <Link className={plainLink} to="/settings/system">Settings → System → Restore</Link>}
    </Alert>
  );
}
