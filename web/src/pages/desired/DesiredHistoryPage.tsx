import { Link, useSearchParams } from "react-router";
import { Alert } from "@/components/ui/Alert";
import { Card } from "@/components/ui/Card";
import { Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { fmtTime } from "@/components/monitoring/format";
import { DesiredStatusBadge } from "@/components/desired/ui";
import { useDesiredHistory, type DesiredHistoryEntry } from "@/components/desired/queries";
import { errorMessage } from "@/lib/format";
import { DesiredTabs } from "./DesiredTabs";
import { historyTitle } from "./HostDesiredPage";

const kinds = [
  ["", "Everything"],
  ["drift_detected", "Drift detected"],
  ["restored", "Restored"],
  ["restore_failed", "Restore failed"],
  ["accepted", "Current state accepted"],
  ["resolved_by_configuration", "Resolved by configuration"],
  ["conflict_detected", "Conflicts"],
  ["unknown", "Unknown"],
] as const;

const resolutions: Record<string, string> = {
  restored: "a corrective job changed the host (checked afterwards)",
  configuration: "the desired state changed; the host didn't",
  observed: "the host changed by other means",
};

/** Meaningful transitions across the fleet; unchanged conditions are never recorded. */
export function DesiredHistoryPage() {
  const [params, setParams] = useSearchParams();
  const kind = params.get("kind") ?? "";
  const baseline = params.get("baseline") ?? "";
  const hist = useDesiredHistory({ kind, baseline });
  return (
    <>
      <PageHeader title="Desired State" description="What changed, and how it ended: restored by DROOL, resolved by configuration, or changed on the host." />
      <DesiredTabs />
      <Card>
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-sm">Show
            <Select className="w-auto" value={kind} onChange={(e) => setParams({ ...(e.target.value ? { kind: e.target.value } : {}), ...(baseline ? { baseline } : {}) })}>
              {kinds.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
            </Select>
          </label>
          {baseline && <span className="text-sm text-muted">One baseline only · <Link className={plainLink} to="/desired/history">show all</Link></span>}
        </div>
        {hist.error && <Alert>{errorMessage(hist.error)}</Alert>}
        <Table<DesiredHistoryEntry>
          rows={hist.data?.items ?? []}
          rowKey={(e) => String(e.id)}
          empty={hist.isPending ? "Loading…" : "Nothing recorded yet."}
          columns={[
            { header: "When", cell: (e) => <span className="whitespace-nowrap text-muted">{fmtTime(e.ts)}</span> },
            { header: "Host", cell: (e) => <Link className={plainLink} to={`/hosts/${e.host_id}/desired`}>{e.host_name}</Link> },
            { header: "Resource", cell: (e) => <span className="font-mono text-xs">{e.resource ?? "(whole host)"}</span> },
            {
              header: "What happened",
              cell: (e) => (
                <div>
                  <span className="inline-flex flex-wrap items-center gap-1 font-medium">{historyTitle(e.kind, e.resolution)}{e.status && <DesiredStatusBadge status={e.status} />}</span>
                  <div className="text-xs text-muted">{e.detail}{e.resolution && resolutions[e.resolution] ? ` — ${resolutions[e.resolution]}` : ""}</div>
                </div>
              ),
            },
            {
              header: "Source",
              cell: (e) => e.baseline
                ? <span className="text-sm"><Link className={plainLink} to={`/desired/baselines/${e.baseline.id}`}>{e.baseline.name}</Link> <span className="text-muted">rev. {e.baseline.revision}</span></span>
                : <span className="text-sm text-muted">{e.source_label || "—"}</span>,
            },
            { header: "By", cell: (e) => <span className="text-sm">{e.actor || <span className="text-muted">DROOL</span>}{e.job_id && <> · <Link className={plainLink} to={`/jobs/${e.job_id}`}>job</Link></>}</span> },
          ]}
        />
      </Card>
    </>
  );
}
