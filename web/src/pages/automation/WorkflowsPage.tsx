import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { useWorkflows, type Workflow } from "@/components/automation/queries";
import { fmtTime } from "@/components/monitoring/format";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { errorMessage } from "@/lib/format";

/** Workflows: reusable, revisioned lists of steps. */
export function WorkflowsPage() {
  const { can } = useAuth();
  const [archived, setArchived] = useState(false);
  const list = useWorkflows(archived);
  const rows = list.data?.items ?? [];
  const newButton = can("workflows.manage") && (
    <Link to="/jobs/workflows/new" className="inline-flex h-9 items-center rounded-ui bg-accent px-3.5 text-sm font-medium text-accent-fg hover:opacity-90">
      New workflow
    </Link>
  );
  return (
    <>
      <PageHeader title="Workflows" actions={newButton}
        description="Reusable, revisioned lists of steps — for example: update, reboot if required, then check that services run. Run one now or put it on a schedule." />
      {list.error && <Alert>{errorMessage(list.error)}</Alert>}
      {!archived && list.data && rows.length === 0 ? (
        <EmptyState title="No workflows yet"
          body="A workflow is a list of steps you can run or schedule again and again, for example: update, reboot if required, then check that services are running. Start from a template."
          action={newButton} />
      ) : (
        <Card title={archived ? "Archived workflows" : "Workflows"}
          actions={<Button size="sm" variant="ghost" onClick={() => setArchived(!archived)}>{archived ? "Show current" : "Show archived"}</Button>}>
          <Table<Workflow>
            rows={rows}
            rowKey={(w) => w.id}
            empty={list.isPending ? "Loading…" : "No workflows."}
            columns={[
              {
                header: "Workflow",
                cell: (w) => (
                  <div className="min-w-[10rem]">
                    <Link className="font-medium text-accent" to={`/jobs/workflows/${w.id}`}>{w.name}</Link>
                    {w.description && <div className="text-xs text-muted">{w.description}</div>}
                  </div>
                ),
              },
              { header: "Steps", cell: (w) => <ol className="list-decimal pl-4 text-muted">{w.summary.map((x, i) => <li key={i}>{x}</li>)}</ol> },
              { header: "Revision", cell: (w) => w.latest_revision },
              {
                header: "Schedules",
                cell: (w) => (
                  <span className="flex flex-col items-start gap-1">
                    {w.schedules || "—"}
                    {w.outdated_schedules > 0 && <Badge tone="accent">{w.outdated_schedules} on an older revision</Badge>}
                  </span>
                ),
              },
              { header: "Changed", cell: (w) => <span className="whitespace-nowrap text-muted">{fmtTime(w.updated_at)}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
