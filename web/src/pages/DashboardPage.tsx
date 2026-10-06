import { Link } from "react-router";
import { useHosts, useJobs } from "@/api/queries";
import { JobStatusBadge } from "@/components/jobs/badges";
import { useAuth } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";

export function DashboardPage() {
  const { me, can } = useAuth();
  const hosts = useHosts();
  const jobs = useJobs();
  const recent = (jobs.data?.items ?? []).slice(0, 5);
  const items = hosts.data?.items ?? [];
  const ready = items.filter((h) => h.status === "ready").length;
  const attention = items.filter((h) => h.status !== "ready" && !h.jump_only).length;

  return (
    <>
      <PageHeader title="Dashboard" description={`Signed in as ${me?.user.username}.`} />
      <div className="grid gap-4 md:grid-cols-3">
        <Card title="Hosts">
          {items.length === 0 ? (
            <p className="text-sm text-muted">
              No hosts yet.{" "}
              {can("hosts.write") && <Link className="text-accent" to="/hosts/new">Add your first host</Link>}
            </p>
          ) : (
            <p className="text-sm">
              <span className="text-2xl font-semibold">{items.length}</span> total · {ready} ready
            </p>
          )}
        </Card>
        <Card title="Needs attention">
          {attention === 0 ? (
            <p className="text-sm text-muted">Nothing needs attention.</p>
          ) : (
            <p className="text-sm">
              <Link className="text-accent" to="/hosts">{attention} host{attention === 1 ? "" : "s"}</Link> not ready
            </p>
          )}
        </Card>
        <Card title="Recent jobs">
          {recent.length === 0 ? (
            <p className="text-sm text-muted">No jobs yet.</p>
          ) : (
            <ul className="space-y-1.5 text-sm">
              {recent.map((j) => (
                <li key={j.id} className="flex items-center gap-2">
                  <JobStatusBadge job={j} />
                  <Link className="truncate text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </>
  );
}
