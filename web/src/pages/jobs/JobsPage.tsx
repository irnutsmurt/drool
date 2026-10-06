import { Link } from "react-router";
import { useJobs, type Job } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { JobStatusBadge } from "@/components/jobs/badges";
import { Alert } from "@/components/ui/Alert";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatDateTime } from "@/lib/format";
import { summarizeCounts } from "./summary";

export function JobsPage() {
  const { can } = useAuth();
  const jobs = useJobs();
  const newButton = can("jobs.execute") && (
    <Link to="/jobs/new" className="inline-flex h-9 items-center rounded-ui bg-accent px-3.5 text-sm font-medium text-accent-fg hover:opacity-90">
      Run command
    </Link>
  );
  if (jobs.data && jobs.data.items.length === 0) {
    return (
      <>
        <PageHeader title="Jobs" />
        <EmptyState title="No jobs yet" body="Run a command or script on one or more hosts." action={newButton} />
      </>
    );
  }
  return (
    <>
      <PageHeader title="Jobs" actions={newButton} />
      {jobs.error && <Alert>{errorMessage(jobs.error)}</Alert>}
      <Card>
        <Table<Job>
          rows={jobs.data?.items ?? []}
          rowKey={(j) => j.id}
          empty={jobs.isPending ? "Loading…" : "No jobs."}
          columns={[
            { header: "Job", cell: (j) => <Link className="font-medium text-accent" to={`/jobs/${j.id}`}>{j.name}</Link> },
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
