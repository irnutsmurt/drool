import { Link, useParams } from "react-router";
import { HostNetworkPanel } from "@/components/network/HostNetwork";
import { secondaryLink } from "@/components/ui/Button";
import { SectionHeader } from "@/components/ui/SectionHeader";

/** One host's network activity: what it is talking to. */
export function HostNetworkPage() {
  const { id = "" } = useParams();
  return (
    <>
      <SectionHeader title="Network"
        description="What this host listens on and talks to, and how the firewall policy classifies each connection. Monitoring only: nothing here blocks traffic."
        actions={<Link to={`/network?host=${id}`} className={secondaryLink}>Open in Network Activity</Link>} />
      <HostNetworkPanel hostId={id} />
    </>
  );
}
