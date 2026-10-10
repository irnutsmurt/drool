import { Link, useParams } from "react-router";
import { useHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { HostNetworkPanel } from "@/components/network/HostNetwork";
import { secondaryLink } from "@/components/ui/Button";
import { PageHeader } from "@/components/ui/PageHeader";

/** One host's network activity: what it is talking to. */
export function HostNetworkPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const host = useHost(id);
  const name = host.data?.name ?? "Host";
  return (
    <>
      <PageHeader crumbs={[{ label: "Hosts", to: "/hosts" }, { label: name, to: `/hosts/${id}` }]}
        title={`${name}: network activity`}
        description="What this host listens on and talks to, and how the firewall policy classifies each connection. Monitoring only: nothing here blocks traffic."
        actions={
          <>
            <Link to={`/network?host=${id}`} className={secondaryLink}>Open in Network Activity</Link>
            {can("firewall.read") && <Link to={`/hosts/${id}/firewall`} className={secondaryLink}>Host firewall</Link>}
          </>
        } />
      <HostNetworkPanel hostId={id} />
    </>
  );
}
