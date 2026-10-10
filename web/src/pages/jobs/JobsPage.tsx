import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk, useHosts, type Job } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { JobStatusBadge } from "@/components/jobs/badges";
import { primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { LoadError } from "@/components/ui/States";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { formatDateTime } from "@/lib/format";
import { summarizeCounts } from "./summary";

const active = (status?: string) => status === "pending" || status === "running" || status === "paused";

/** Runs: every job DROOL has run, newest first, optionally for one host. */
export function JobsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const host = params.get("host") ?? "";
  const hosts = useHosts();
  const jobs = useQuery({
    queryKey: host ? [...jk.jobs, { host, all: true }] : jk.jobs,
    queryFn: () => unwrap(api.GET("/jobs", { params: { query: { host: host || undefined, limit: 100 } } })),
    refetchInterval: (q) => (q.state.data?.items.some((j) => active(j.status)) ? 3000 : false),
  });
  const hostName = hosts.data?.items.find((h) => h.id === host)?.name;
  const newButton = can("jobs.execute") && (
    <Link to={host ? `/jobs/new?hosts=${host}` : "/jobs/new"} className={primaryLink}>New run</Link>
  );
  const header = (
    <PageHeader title="Runs" actions={newButton}
      description={host
        ? <>Runs that included <Link className={plainLink} to={`/hosts/${host}`}>{hostName ?? "this host"}</Link> · <button type="button" className="underline underline-offset-2 hover:text-fg" onClick={() => setParams({})}>show all runs</button></>
        : "Every job DROOL runs — commands, updates, file deployments, scheduled runs and desired-state corrections — with its output and outcome."} />
  );
  if (jobs.data && jobs.data.items.length === 0 && !host) {
    return (
      <>
        {header}
        <EmptyState title="No runs yet"
          body="A run executes a command, script, update, package or service change on one or more hosts, with canaries, batches and a failure threshold when you want them. Scheduled runs and desired-state corrections appear here too."
          action={newButton} />
      </>
    );
  }
  return (
    <>
      {header}
      {jobs.error && <LoadError what="runs" error={jobs.error} />}
      <Card>
        <Table<Job>
          rows={jobs.data?.items ?? []}
          rowKey={(j) => j.id}
          empty={jobs.isPending ? "Loading…" : host ? "No runs have included this host yet." : "No runs."}
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
      </Card>
    </>
  );
}
