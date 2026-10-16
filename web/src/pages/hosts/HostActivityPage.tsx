import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { RunsTable } from "@/components/jobs/RunsTable";
import { useHostWorkspace } from "@/components/hosts/workspace/HostLayout";
import { primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { SectionHeader } from "@/components/ui/SectionHeader";

/** What has happened on this host: the runs that included it. */
export function HostActivityPage() {
  const { host: h } = useHostWorkspace();
  const { can } = useAuth();
  return (
    <div className="space-y-4">
      <SectionHeader title="Activity" description="Runs that included this host, newest first: commands, updates, deployments, scheduled runs and corrections."
        actions={can("jobs.execute") && !h.jump_only && <Link to={`/jobs/new?hosts=${h.id}`} className={primaryLink}>New run</Link>} />
      <Card>
        <RunsTable host={h.id} pageSize={25} empty="No runs have included this host yet." />
      </Card>
    </div>
  );
}
