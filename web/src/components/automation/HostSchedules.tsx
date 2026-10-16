import { Link } from "react-router";
import { useGroups, type Host } from "@/api/queries";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Table } from "@/components/ui/Table";
import { errorMessage } from "@/lib/format";
import { fmtTime } from "@/components/monitoring/format";
import { useSchedules, type Schedule } from "./queries";
import { ScheduleStateBadge } from "./ui";

/** How a schedule reaches a host: named directly, or through a Pack or a
 * tag — which are worked out again each time it runs. */
export interface HostTargeting {
  schedule: Schedule;
  direct: boolean;
  packs: string[];
  tags: string[];
  excluded: boolean;
}

/** The schedules that target a host, directly or (today) through one of
 * its Packs or tags; a schedule that names it under "except" is listed as
 * excluding it. */
export function hostTargeting(host: Pick<Host, "id" | "group_ids" | "tags">, schedules: Schedule[], packNames: Record<string, string>): HostTargeting[] {
  const out: HostTargeting[] = [];
  for (const s of schedules) {
    const t = s.config.targets;
    const direct = (t.hosts ?? []).includes(host.id);
    const packs = (t.groups ?? []).filter((g) => host.group_ids.includes(g)).map((g) => packNames[g] ?? g);
    const tags = (t.tags ?? []).filter((tag) => host.tags.includes(tag));
    const excluded = (t.exclude_hosts ?? []).includes(host.id);
    if (direct || packs.length || tags.length) out.push({ schedule: s, direct, packs, tags, excluded });
  }
  return out;
}

/** A host's schedules: what runs on it at set times (and maintenance
 * windows that cover it), saying how each one reaches it. */
export function HostSchedules({ host }: { host: Host }) {
  const q = useSchedules();
  const groups = useGroups();
  const names = Object.fromEntries((groups.data?.items ?? []).map((g) => [g.id, g.name]));
  const rows = q.data ? hostTargeting(host, q.data.items, names) : [];
  return (
    <div className="space-y-3" data-testid="host-schedules">
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      <Table<HostTargeting>
        rows={rows}
        rowKey={(r) => r.schedule.id}
        empty={q.isPending ? "Loading…" : "No schedule targets this host, directly or through its Packs or tags."}
        columns={[
          {
            header: "Schedule",
            cell: (r) => (
              <>
                <Link className="font-medium text-accent" to={`/jobs/schedules/${r.schedule.id}`}>{r.schedule.name}</Link>
                <div className="text-xs text-muted">{r.schedule.kind === "maintenance" ? "Maintenance window" : r.schedule.workflow_name ? `Workflow: ${r.schedule.workflow_name}` : "Steps"}</div>
              </>
            ),
          },
          { header: "State", cell: (r) => <ScheduleStateBadge s={r.schedule} /> },
          {
            header: "How it reaches this host",
            cell: (r) => r.excluded ? (
              <span className="text-muted">Excluded by name, although {r.direct ? "also listed" : r.packs.length ? `Pack ${r.packs.join(", ")} matches` : `tag ${r.tags.join(", ")} matches`}</span>
            ) : (
              <span className="flex flex-col gap-0.5">
                {r.direct && <span><Badge tone="accent">directly</Badge> names this host</span>}
                {r.packs.length > 0 && <span>through Pack {r.packs.join(", ")} <span className="text-xs text-muted">(membership is checked again when it runs)</span></span>}
                {r.tags.length > 0 && <span>through tag {r.tags.join(", ")} <span className="text-xs text-muted">(checked again when it runs)</span></span>}
              </span>
            ),
          },
          { header: "When", cell: (r) => <span className="text-muted">{r.schedule.when}</span> },
          { header: "Next run", cell: (r) => r.schedule.enabled && r.schedule.next_run_at && !r.excluded ? fmtTime(r.schedule.next_run_at) : <span className="text-muted">—</span> },
        ]}
      />
      <p className="text-xs text-muted">
        Schedules that reach a host through a Pack or a tag work out their hosts each time they run: if the host leaves the Pack or loses the tag,
        it isn&apos;t included any more. Recurring maintenance windows are listed too; they are edited under Alerts → Maintenance.
      </p>
    </div>
  );
}
