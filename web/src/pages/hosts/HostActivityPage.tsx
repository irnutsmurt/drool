import { Link, useSearchParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { AuditTable } from "@/components/audit/AuditTable";
import { HostSchedules } from "@/components/automation/HostSchedules";
import { RunsTable } from "@/components/jobs/RunsTable";
import { useHostWorkspace } from "@/components/hosts/workspace/HostLayout";
import { primaryLink, secondaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { SectionHeader } from "@/components/ui/SectionHeader";
import { cx } from "@/components/ui/cx";

const views = [
  { key: "runs", label: "Runs", permission: "jobs.read" },
  { key: "schedules", label: "Schedules", permission: "schedules.read" },
  { key: "audit", label: "Audit Log", permission: "audit.read" },
] as const;

/** What has happened, and will happen, on this host: the runs that
 * included it, the schedules that may target it, and its audit events. */
export function HostActivityPage() {
  const { host: h } = useHostWorkspace();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const shown = views.filter((v) => can(v.permission));
  const view = shown.find((v) => v.key === params.get("view"))?.key ?? shown[0]?.key ?? "runs";
  return (
    <div className="space-y-4">
      <SectionHeader title="Activity"
        description={view === "runs" ? "Runs that included this host, newest first: commands, updates, deployments, scheduled runs and corrections."
          : view === "schedules" ? "Schedules that run on this host, and how each one reaches it."
            : "Audit events about this host: changes to it and actions taken on it, by whom and when."}
        actions={
          view === "runs" ? can("jobs.execute") && !h.jump_only && <Link to={`/jobs/new?hosts=${h.id}`} className={primaryLink}>New run</Link>
            : view === "schedules" ? can("schedules.manage") && !h.jump_only && <Link to="/jobs/schedules/new" className={secondaryLink}>New schedule</Link>
              : <Link to={`/audit?target=${h.id}`} className={secondaryLink}>Open in the Audit Log</Link>
        } />
      <div role="group" aria-label="Activity views" className="flex flex-wrap gap-1" data-testid="activity-views">
        {shown.map((v) => (
          <button key={v.key} type="button" aria-pressed={view === v.key}
            onClick={() => setParams((p) => {
              p.set("view", v.key);
              return p;
            }, { replace: true })}
            className={cx("rounded-ui border px-3 py-1 text-sm", view === v.key ? "border-accent bg-accent/15 font-medium" : "border-line hover:bg-surface-2")}>
            {v.label}
          </button>
        ))}
      </div>
      <Card>
        {view === "runs" && <RunsTable host={h.id} pageSize={25} empty="No runs have included this host yet." />}
        {view === "schedules" && <HostSchedules host={h} />}
        {view === "audit" && <AuditTable target={h.id} pageSize={25} empty="No audit events name this host as their target yet." />}
      </Card>
    </div>
  );
}
