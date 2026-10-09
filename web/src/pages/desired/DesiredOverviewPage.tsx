import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { fmtSince } from "@/components/monitoring/format";
import { RestoreDialog } from "@/components/desired/RestoreDialog";
import { CountTile, DesiredStatusBadge } from "@/components/desired/ui";
import { useDesiredHosts, useDesiredSummary, useInvalidateDesired, type DesiredHostRow } from "@/components/desired/queries";
import { errorMessage } from "@/lib/format";
import { DesiredTabs } from "./DesiredTabs";

const filters = [
  ["attention", "Needs attention"],
  ["", "All managed hosts"],
  ["drift", "Drift"],
  ["conflict", "Conflicts"],
  ["unknown", "Unknown"],
  ["failed", "Restore failed"],
  ["compliant", "Compliant"],
] as const;

/** The fleet's desired state: what differs, what conflicts, what DROOL
 * can't see, and what automatic restore paused. Compliance recedes. */
export function DesiredOverviewPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "attention";
  const [q, setQ] = useState("");
  const sum = useDesiredSummary();
  const hosts = useDesiredHosts(status === "all" ? "" : status, q);
  const invalidate = useInvalidateDesired();
  const [restore, setRestore] = useState(false);
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/desired/refresh", { body: {} })),
    onSuccess: () => setTimeout(invalidate, 3000),
  });
  const s = sum.data;
  const attention = s ? s.hosts.drift + s.hosts.conflict + s.hosts.failed : 0;
  return (
    <>
      <PageHeader
        title="Desired state"
        description="What your machines should look like, whether they still do, and how to put them back."
        actions={
          <>
            {can("desired.restore") && (
              <Button onClick={() => refresh.mutate()} disabled={refresh.isPending}>{refresh.isPending ? "Checking…" : "Check all now"}</Button>
            )}
            {can("desired.restore") && s && s.resources.drift + s.resources.failed > 0 && (
              <Button variant="primary" onClick={() => setRestore(true)}>Restore managed state…</Button>
            )}
          </>
        }
      />
      <DesiredTabs />
      {sum.error && <Alert>{errorMessage(sum.error)}</Alert>}
      {s && s.baselines === 0 ? (
        <EmptyState title="No baselines yet"
          body={<>A baseline says what machines should look like: services that should run, packages that should (or shouldn't) be installed, files that should match. DROOL then tells you whether they still do.</>}
          action={can("desired.manage") ? <Link to="/desired/baselines/new"><Button variant="primary">Create baseline</Button></Link> : undefined} />
      ) : s && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <CountTile label={`host${s.hosts.drift === 1 ? "" : "s"} with drift (${s.resources.drift} resource${s.resources.drift === 1 ? "" : "s"})`} value={s.hosts.drift} tone="warn" to="/desired?status=drift" />
            <CountTile label={`conflict${s.resources.conflict === 1 ? "" : "s"} to resolve`} value={s.resources.conflict} tone="danger" to="/desired?status=conflict" />
            <CountTile label={`failed restore${s.resources.failed === 1 ? "" : "s"}`} value={s.resources.failed} tone="danger" to="/desired?status=failed" />
            <CountTile label="can't be verified right now" value={s.resources.unknown} tone="neutral" to="/desired?status=unknown" />
          </div>
          <p className="text-sm text-muted">
            {s.hosts.compliant} of {s.hosts.total} managed host{s.hosts.total === 1 ? "" : "s"} compliant
            {s.unmanaged > 0 && <> · {s.unmanaged} host{s.unmanaged === 1 ? "" : "s"} with nothing managed</>}
            {s.reconciling > 0 && <> · {s.reconciling} correction{s.reconciling === 1 ? "" : "s"} in progress</>}
            {" · "}{s.baselines} baseline{s.baselines === 1 ? "" : "s"}{s.auto_baselines > 0 && <>, {s.auto_baselines} restoring automatically</>}
          </p>
          {s.auto_paused.length > 0 && (
            <Alert tone="warn">
              <strong>Automatic restore paused — needs attention:</strong>
              <ul className="mt-1 list-disc pl-5">
                {s.auto_paused.map((p) => (
                  <li key={p.id}><Link className="underline" to={`/desired/baselines/${p.id}`}>{p.name}</Link>: {p.attention}</li>
                ))}
              </ul>
            </Alert>
          )}
          {(s.firewall || s.agents) && (
            <div className="flex flex-wrap gap-3 text-sm" aria-label="Other desired state">
              {s.firewall && (
                <div className="rounded-ui border border-line bg-surface px-3 py-2">
                  <span className="font-medium">Firewall policy</span> — managed by <Link className={plainLink} to="/firewall">Firewall</Link>:{" "}
                  {s.firewall.drift > 0 ? <span className="text-warn">{s.firewall.drift} host{s.firewall.drift === 1 ? "" : "s"} drifted</span>
                    : s.firewall.enforced === 0 ? <span className="text-muted">not enforced on any host</span>
                      : <span className="text-muted">enforced on {s.firewall.enforced} host{s.firewall.enforced === 1 ? "" : "s"}, no drift</span>}
                </div>
              )}
              {s.agents && s.agents.state_conflicts > 0 && (
                <div className="rounded-ui border border-line bg-surface px-3 py-2">
                  <span className="font-medium">Agent state</span>: <span className="text-warn">{s.agents.state_conflicts} host{s.agents.state_conflicts === 1 ? "" : "s"} ahead of or foreign to this controller</span> —{" "}
                  <Link className={plainLink} to="/agents">Agents</Link>
                </div>
              )}
            </div>
          )}
          <Card>
            <div className="mb-3 flex flex-wrap items-center gap-2">
              <div role="group" aria-label="Show" className="flex flex-wrap gap-1">
                {filters.map(([k, label]) => {
                  const on = status === (k || "all");
                  return (
                    <button key={k} type="button" aria-pressed={on}
                      onClick={() => setParams(k === "attention" ? {} : { status: k || "all" })}
                      className={cx("rounded-full border px-3 py-1 text-xs", on ? "border-accent bg-accent/10 font-medium text-fg" : "border-line text-muted hover:text-fg")}>
                      {label}
                    </button>
                  );
                })}
              </div>
              <Input className="ml-auto max-w-[14rem]" placeholder="Filter by host name" aria-label="Filter by host name" value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <HostTable rows={(hosts.data?.items ?? [])} loading={hosts.isPending} attention={status === "attention" && attention === 0} />
          </Card>
        </div>
      )}
      {restore && <RestoreDialog selection={{}} title="Restore managed state on every host" onClose={() => setRestore(false)} />}
    </>
  );
}

function HostTable({ rows, loading, attention }: { rows: DesiredHostRow[]; loading: boolean; attention: boolean }) {
  return (
    <Table<DesiredHostRow>
      rows={rows}
      rowKey={(h) => h.host_id}
      empty={loading ? "Loading…" : attention ? "Nothing needs attention: every managed host is compliant (or being checked)." : "No hosts match."}
      columns={[
        { header: "Host", cell: (h) => <Link className={plainLink} to={`/hosts/${h.host_id}/desired`}>{h.name}</Link> },
        { header: "Status", cell: (h) => <span className="inline-flex flex-wrap items-center gap-1"><DesiredStatusBadge status={h.status} />{h.in_maintenance && <span className="text-xs text-muted">in maintenance</span>}</span> },
        {
          header: "What differs",
          cell: (h) => h.problems.length > 0
            ? <span className="text-sm">{h.problems.join(", ")}{h.counts.drift + h.counts.conflict + h.counts.failed > h.problems.length && " …"}</span>
            : h.unknown ? <span className="text-xs text-muted">{h.unknown}</span> : <span className="text-muted">—</span>,
        },
        { header: "Resources", cell: (h) => <span className="text-muted">{h.counts.compliant}/{h.counts.total} compliant</span> },
        { header: "Baselines", cell: (h) => <span className="text-sm text-muted">{h.baselines.map((b) => b.name).join(", ") || "—"}</span> },
        { header: "Checked", cell: (h) => <span className="text-muted">{h.checked_at ? fmtSince(h.checked_at) : "—"}</span> },
      ]}
    />
  );
}
