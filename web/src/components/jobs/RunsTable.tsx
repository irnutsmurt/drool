import { Link } from "react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk, type Job } from "@/api/queries";
import { Button } from "@/components/ui/Button";
import { LoadError } from "@/components/ui/States";
import { Table } from "@/components/ui/Table";
import { formatDateTime } from "@/lib/format";
import { summarizeCounts } from "@/pages/jobs/summary";
import { JobStatusBadge } from "./badges";

const active = (status?: string) => status === "pending" || status === "running" || status === "paused";

/** Runs, newest first — all of them or the ones that included one host —
 * a page at a time from the server ("Show more"), so a long history never
 * loads at once. */
export function RunsTable({ host, pageSize = 50, empty }: { host?: string; pageSize?: number; empty?: string }) {
  const q = useInfiniteQuery({
    queryKey: [...jk.jobs, { host: host ?? "", pageSize }],
    initialPageParam: "",
    queryFn: ({ pageParam }) => unwrap(api.GET("/jobs", { params: { query: { host: host || undefined, limit: pageSize, cursor: pageParam || undefined } } })),
    getNextPageParam: (last) => last.next_cursor || undefined,
    // Keep the list fresh while anything is running; SSE drives the run's own page.
    refetchInterval: (s) => (s.state.data?.pages.some((p) => p.items.some((j) => active(j.status))) ? 3000 : false),
  });
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];
  if (q.error) return <LoadError what="runs" error={q.error} />;
  return (
    <div className="space-y-3" data-testid="runs-table">
      <Table<Job>
        rows={rows}
        rowKey={(j) => j.id}
        empty={q.isPending ? "Loading…" : empty ?? "No runs."}
        columns={[
          {
            header: "Run",
            cell: (j) => (
              <>
                <Link className="font-medium text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
                {j.origin?.schedule_name && <div className="text-xs text-muted">{j.origin.manual ? "Run now" : "Scheduled"}: {j.origin.schedule_name}</div>}
                {!j.origin?.schedule_name && j.origin?.workflow_name && <div className="text-xs text-muted">Workflow: {j.origin.workflow_name}</div>}
                {j.origin?.reconcile_id && <div className="text-xs text-muted">Desired State correction{j.origin.baseline_name ? `: ${j.origin.baseline_name}` : ""}</div>}
              </>
            ),
          },
          { header: "Status", cell: (j) => <JobStatusBadge job={j} /> },
          { header: "Hosts", cell: (j) => <span className="text-muted">{summarizeCounts(j)}</span> },
          { header: "By", cell: (j) => j.created_by },
          { header: "Started", cell: (j) => <span className="whitespace-nowrap text-muted">{formatDateTime(j.created_at)}</span> },
        ]}
      />
      {(q.hasNextPage || rows.length > 0) && (
        <div className="flex flex-wrap items-center gap-3 text-xs text-muted">
          <span aria-live="polite">{rows.length} run{rows.length === 1 ? "" : "s"} shown{q.hasNextPage ? "; older runs on request" : ""}</span>
          {q.hasNextPage && (
            <Button size="sm" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>
              {q.isFetchingNextPage ? "Loading…" : "Show more"}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
