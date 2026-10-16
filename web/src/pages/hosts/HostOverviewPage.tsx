import type { ReactNode } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useJobs } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { JobStatusBadge } from "@/components/jobs/badges";
import { HostAttention } from "@/components/hosts/HostAttention";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { ServicesCard, StatusLines, SystemCard } from "@/components/hosts/InventoryCards";
import { ReachabilityBadge } from "@/components/hosts/health";
import { statusHelp } from "@/components/hosts/statusHelp";
import { useHostWorkspace, useLegacyHostAnchors } from "@/components/hosts/workspace/HostLayout";
import { ManagedFilesCard } from "@/components/files/ManagedFilesCard";
import { DesiredStateCard } from "@/components/desired/DesiredStateCard";
import { HostFirewallSummary } from "@/components/firewall/HostFirewall";
import { HostNetworkSummary } from "@/components/network/HostNetwork";
import { HostMonitoringSummary } from "@/components/monitoring/HostMonitoringSummary";
import { HostAlertsCard, HostMaintenanceBanner } from "@/components/alerts/HostAlerts";
import { Alert } from "@/components/ui/Alert";
import { Card } from "@/components/ui/Card";
import { LoadError, Loading } from "@/components/ui/States";
import { errorMessage, formatDateTime } from "@/lib/format";

/** "View monitoring" and friends: the way from a summary to the section
 * that has the details. */
function ViewLink({ to, children }: { to: string; children: ReactNode }) {
  return <Link className="whitespace-nowrap text-sm text-accent" to={to}>{children} <span aria-hidden="true">→</span></Link>;
}

/**
 * A host at a glance: is it okay, what needs attention, and the way into
 * each section of its workspace. The header above already says who the
 * host is and how DROOL reaches it.
 */
export function HostOverviewPage() {
  const { host: h } = useHostWorkspace();
  const qc = useQueryClient();
  const { can } = useAuth();
  const recentJobs = useJobs(h.id);
  useLegacyHostAnchors(h.id);
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");
  const managed = !h.jump_only;
  const help = statusHelp(h);
  // Approving the host key runs the first connection test; its steps show
  // here.
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/probe", { params: { path: { id: h.id } } })),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.host(h.id) });
      void qc.invalidateQueries({ queryKey: qk.hosts });
      void qc.invalidateQueries({ queryKey: qk.hostKeys(h.id) });
    },
  });

  return (
    <div className="space-y-4">
      {can("alerts.read") && managed && <HostMaintenanceBanner hostId={h.id} />}
      {needsKey && help && <Alert tone={help.tone}>{help.text}</Alert>}
      {needsKey && (
        <Card title={h.status === "hostkey_mismatch" ? "The host key changed" : "Approve the host key"}>
          <HostKeyPanel hostId={h.id} onApproved={() => test.mutate()} />
        </Card>
      )}
      {(test.data || test.error || test.isPending) && (
        <Card title="Connection test">
          {test.isPending ? <Loading what="the connection test" /> : test.error ? <Alert>{errorMessage(test.error)}</Alert> : test.data && <ProbeSteps result={test.data} />}
        </Card>
      )}
      {managed && <HostAttention h={h} hideStatus={!!help} />}

      <div className="grid gap-4 lg:grid-cols-2">
        {managed && (
          <Card title="Health" actions={can("monitoring.read") && <ViewLink to={`/hosts/${h.id}/monitoring`}>View monitoring</ViewLink>}>
            <div className="space-y-3">
              <p className="flex flex-wrap items-center gap-2 text-sm"><ReachabilityBadge host={h} />
                <span className="text-muted">{h.health ? (h.health.reachable ? "answered the last status check" : "the last status check failed") : "status not checked yet"}</span></p>
              {can("monitoring.read") && <HostMonitoringSummary hostId={h.id} />}
              <StatusLines host={h} />
            </div>
          </Card>
        )}
        {can("alerts.read") && managed && <HostAlertsCard hostId={h.id} />}
        {can("desired.read") && managed && <DesiredStateCard hostId={h.id} />}
        {can("firewall.read") && (
          <Card title="Firewall" actions={<ViewLink to={`/hosts/${h.id}/firewall`}>View firewall</ViewLink>}>
            <HostFirewallSummary hostId={h.id} />
          </Card>
        )}
        {can("network.read") && managed && (
          <Card title="Network Activity" actions={<ViewLink to={`/hosts/${h.id}/network`}>View network activity</ViewLink>}>
            <HostNetworkSummary hostId={h.id} />
          </Card>
        )}
        {managed && (
          <Card title="Recent runs" actions={<ViewLink to={`/hosts/${h.id}/activity`}>View activity</ViewLink>}>
            {recentJobs.error ? <LoadError what="runs" error={recentJobs.error} /> : !recentJobs.data ? <Loading /> : recentJobs.data.items.length === 0 ? (
              <p className="text-sm text-muted">No runs have included this host yet.</p>
            ) : (
              <ul className="space-y-1.5 text-sm">
                {recentJobs.data.items.slice(0, 5).map((j) => (
                  <li key={j.id} className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
                    <JobStatusBadge job={j} />
                    <Link className="min-w-0 text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
                    <span className="ml-auto text-xs text-muted">{formatDateTime(j.created_at)}</span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        )}
        {can("files.read") && managed && <ManagedFilesCard hostId={h.id} />}
        {managed && <SystemCard host={h} />}
        {managed && <ServicesCard host={h} />}
      </div>
    </div>
  );
}
