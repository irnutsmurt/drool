import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk, useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { RunsTable } from "@/components/jobs/RunsTable";
import { primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { plainLink } from "@/components/ui/links";

/** Runs: every job DROOL has run, newest first, optionally for one host. */
export function JobsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const host = params.get("host") ?? "";
  const hosts = useHosts();
  // Only to tell "no runs at all yet" (the introduction) from an empty page.
  const any = useQuery({
    queryKey: [...jk.jobs, "any"],
    queryFn: () => unwrap(api.GET("/jobs", { params: { query: { limit: 1 } } })),
    enabled: !host,
  });
  const hostName = hosts.data?.items.find((h) => h.id === host)?.name;
  const newButton = can("jobs.execute") && (
    <Link to={host ? `/jobs/new?hosts=${host}` : "/jobs/new"} className={primaryLink}>New run</Link>
  );
  const header = (
    <PageHeader title="Runs" actions={newButton}
      description={host
        ? <>Runs that included <Link className={plainLink} to={`/hosts/${host}/activity`}>{hostName ?? "this host"}</Link> · <button type="button" className="underline underline-offset-2 hover:text-fg" onClick={() => setParams({})}>show all runs</button></>
        : <>Every job DROOL runs — commands, updates, file deployments, scheduled runs and desired-state corrections — with its output and outcome.
          {" "}<Link className={plainLink} to="/jobs/schedules">Schedules</Link> start runs at set times; <Link className={plainLink} to="/jobs/workflows">workflows</Link> are reusable lists of steps.</>} />
  );
  if (any.data && any.data.items.length === 0 && !host) {
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
      <Card>
        <RunsTable host={host || undefined} empty={host ? "No runs have included this host yet." : "No runs."} />
      </Card>
    </>
  );
}
