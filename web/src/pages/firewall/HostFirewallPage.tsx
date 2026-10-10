import { Link, useParams } from "react-router";
import { useHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { HostFirewall } from "@/components/firewall/HostFirewall";
import { secondaryLink } from "@/components/ui/Button";
import { PageHeader } from "@/components/ui/PageHeader";
import { ReadOnlyNote } from "@/components/ui/ReadOnlyNote";
import { LoadError, Loading } from "@/components/ui/States";

/** One host's firewall: mode, environment, enforcement and its history. */
export function HostFirewallPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const host = useHost(id);
  const h = host.data;
  return (
    <>
      <PageHeader crumbs={[{ label: "Hosts", to: "/hosts" }, { label: h?.name ?? "Host", to: `/hosts/${id}` }]}
        title={`${h?.name ?? "Host"}: firewall`}
        description="What may reach this host and what it may reach, applied with automatic rollback protection: if a fresh management connection can't verify a change, the host reverts it by itself."
        actions={
          <>
            {can("network.read") && <Link to={`/hosts/${id}/network`} className={secondaryLink}>Observed traffic</Link>}
            <Link to="/firewall?tab=designer" className={secondaryLink}>Firewall policy</Link>
          </>
        } />
      {!can("firewall.write") && <ReadOnlyNote>you can see this host&apos;s firewall and simulate policies; changing its mode or applying policy needs the firewall.write permission (administrators).</ReadOnlyNote>}
      {host.error ? <LoadError what="the host" error={host.error} /> : !h ? <Loading /> : <HostFirewall host={h} />}
    </>
  );
}
