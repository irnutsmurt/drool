import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { modeInfo } from "@/components/firewall/parts";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatAgo } from "@/lib/format";
import { CollectionBadge, DirectionLabel, enforcementText, noLookup, PolicyBadge } from "./labels";
import { GeoAttribution } from "./Enrichment";
import { FlowTable } from "./FlowTable";
import { nk, useHostNetwork, useNetFlows, type NetFilter, type NetHost } from "./queries";

const views: { id: string; label: string; f: NetFilter }[] = [
  { id: "all", label: "All", f: {} },
  { id: "in", label: "Inbound", f: { direction: "in" } },
  { id: "out", label: "Outbound", f: { direction: "out" } },
  { id: "internal", label: "Internal", f: { scope: "internal" } },
  { id: "external", label: "External", f: { scope: "external" } },
  { id: "new", label: "New external (24 h)", f: { new: "host", since: "24h", direction: "out" } },
  { id: "unknown", label: "Unknown traffic", f: { class: "unknown" } },
  { id: "unclassified", label: "Not in policy", f: { policy: "unclassified" } },
];

/** A host's network activity, on its own page: how it is observed, what
 * listens, what connects in, what it connects out to — and what the
 * firewall policy says. */
export function HostNetworkPanel({ hostId }: { hostId: string }) {
  const q = useHostNetwork(hostId);
  const hn = q.data;
  return (
    <div className="grid gap-4">
      <Card title="How this host is observed" actions={hn && <CollectionActions hostId={hostId} hn={hn} />}>
        <div className="space-y-3">
          {q.error && <Alert>{errorMessage(q.error)}</Alert>}
          {!hn && !q.error && <p className="text-sm text-muted">Loading…</p>}
          {hn && <CollectionDetail hn={hn} />}
          {hn && <Listening hn={hn} />}
        </div>
      </Card>
      <Card title="Connections">
        <HostConnections hostId={hostId} hn={hn} />
      </Card>
    </div>
  );
}

/** How the host is observed (a badge), with Take snapshot where only
 * snapshots are possible (SSH hosts) and the role may take one. The
 * outcome of a snapshot shows below it (CollectionDetail). */
export function CollectionActions({ hostId, hn }: { hostId: string; hn: NetHost }) {
  const { can } = useAuth();
  const snap = useSnapshot(hostId);
  return (
    <span className="flex flex-wrap items-center gap-2">
      <CollectionBadge c={hn.collection} />
      {hn.collection.mode !== "continuous" && can("network.snapshot") && (
        <Button size="sm" onClick={() => snap.mutate()} disabled={snap.isPending}>{snap.isPending ? "Taking snapshot…" : "Take snapshot"}</Button>
      )}
    </span>
  );
}

const useSnapshot = (hostId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationKey: ["network", "snapshot", hostId],
    mutationFn: () => unwrap(api.POST("/hosts/{id}/network/snapshot", { params: { path: { id: hostId } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
};

/** The collection's details: when it last reported (and whether that is
 * stale: the server says), lost events, a snapshot's outcome. */
export function CollectionDetail({ hn }: { hn: NetHost }) {
  const snaps = useMutationState({ filters: { mutationKey: ["network", "snapshot", hn.host.id] }, select: (m) => m.state });
  const snap = snaps[snaps.length - 1];
  return (
    <>
      {snap?.status === "error" && <Alert>{errorMessage(snap.error)}</Alert>}
      {snap?.status === "success" && <Alert tone="ok">Snapshot taken: {(snap.data as { flows: number }).flows} connection group(s) open right now.</Alert>}
      {hn.collection.stale && (
        <Alert tone="warn">
          <span aria-hidden="true">⚠ </span>No report from the Shepherd since {formatAgo(hn.collection.reported_at)}: these connections may be out of date.
        </Alert>
      )}
      <p className="text-xs text-muted" data-testid="collection-detail">
        {hn.collection.detail}
        {hn.collection.reported_at && <> · last report {formatAgo(hn.collection.reported_at)}</>}
        {hn.collection.snapshot_at && <> · last snapshot {formatAgo(hn.collection.snapshot_at)}</>}
        {hn.collection.events_lost > 0 && <> · <span className="text-warn">⚠ some new-connection events were lost (counts may be low)</span></>}
        {hn.collection.forwarded > 0 && <> · {hn.collection.forwarded} routed connection(s) through this host not shown (containers, routing)</>}
        {noLookup(hn.enrichment_state, "country") && noLookup(hn.enrichment_state, "asn")
          ? <> · country and network owner: not looked up ({noLookup(hn.enrichment_state, "country")}, {noLookup(hn.enrichment_state, "asn")})</>
          : (noLookup(hn.enrichment_state, "country") || noLookup(hn.enrichment_state, "asn")) && <> · {noLookup(hn.enrichment_state, "country") ?? noLookup(hn.enrichment_state, "asn")}</>}
      </p>
    </>
  );
}

/** A host's observed connections: whether the policy they are judged
 * against is in force there, the common views, and the list (each row
 * opens its inspector). Shared by the host's Network page and its
 * Firewall page's Observed connections. */
export function HostConnections({ hostId, hn, firewallLink = true }: { hostId: string; hn?: NetHost; firewallLink?: boolean }) {
  const [view, setView] = useState("all");
  const filter = { host: [hostId], ...views.find((v) => v.id === view)!.f };
  return (
    <>
      {hn && <FirewallLine hostId={hostId} fw={hn.firewall} link={firewallLink} />}
      <div className="mb-2 flex flex-wrap items-center gap-1.5" role="group" aria-label="Show connections">
        {views.map((v) => (
          <button key={v.id} type="button" aria-pressed={view === v.id} onClick={() => setView(v.id)}
            className={cx("rounded-full border px-2.5 py-0.5 text-xs", view === v.id ? "border-accent bg-accent text-accent-fg" : "border-line bg-surface hover:bg-surface-2")}>
            {v.label}
          </button>
        ))}
        {hn && <span className="ml-auto text-xs text-muted">{hn.counts.flows} total · {hn.counts.unclassified} not in policy · {hn.counts.new_external_24h} new external (24 h)</span>}
      </div>
      <FlowTable filter={filter} showHost={false} pageSize={25}
        empty={hn?.collection.mode === "snapshot" && !hn.collection.snapshot_at ? "No snapshot taken yet. SSH hosts are observed only when you take a snapshot."
          : hn?.collection.mode === "none" || (hn && !hn.collection.enabled && hn.collection.agent && !hn.collection.snapshot_at)
            ? "No connections observed: collection is off for this host (Network Activity → Collection and enrichment)." : "No connections observed."} />
    </>
  );
}

/** Whether the policy the connections are judged against is in force on
 * this host: where it isn't, their policy status is what it would do with
 * them, not what happened to them. */
function FirewallLine({ hostId, fw, link }: { hostId: string; fw: NetHost["firewall"]; link: boolean }) {
  const { can } = useAuth();
  const text = fw.enforcement !== "enforced"
    ? `${enforcementText[fw.enforcement] ?? fw.enforcement}. A connection's policy status is what DROOL's policy would do with it, not what happened to it.`
    : fw.mode !== "enforce"
      ? `DROOL's last applied policy is still in force on this host (mode ${modeInfo[fw.mode]?.label ?? fw.mode}): changes aren't applied.`
      : fw.rules_changed
        ? "DROOL's policy is in force on this host, with changes not applied yet: ↻ marks connections the host still treats differently."
        : "DROOL's policy is in force on this host.";
  return (
    <p className="mb-2 text-xs text-muted" data-testid="network-firewall">
      Firewall: {text}{fw.drift && fw.enforcement !== "drift" && " DROOL's rules are missing from the host (changed outside DROOL)."}
      {link && can("firewall.read") && <> <Link className="text-accent underline" to={`/hosts/${hostId}/firewall`}>This host&apos;s firewall</Link></>}
    </p>
  );
}

/** The host overview's network line: how the host is observed, what it
 * listens on, and connections that need a look. */
export function HostNetworkSummary({ hostId }: { hostId: string }) {
  const q = useHostNetwork(hostId);
  const hn = q.data;
  const notable = useNetFlows({ host: [hostId], policy: "unclassified", limit: 3 }, !!hn && hn.counts.unclassified > 0);
  if (q.error) return <p className="text-sm text-danger">Couldn't load network activity: {errorMessage(q.error)}</p>;
  if (!hn) return <p className="text-sm text-muted">Loading…</p>;
  const never = hn.collection.mode === "snapshot" && !hn.collection.snapshot_at && hn.counts.flows === 0;
  return (
    <div className="space-y-2 text-sm">
      <p className="flex flex-wrap items-center gap-2">
        <CollectionBadge c={hn.collection} />
        {never ? <span className="text-muted">No snapshot taken yet: SSH hosts are observed only when you take one.</span> : (
          <span>
            {hn.counts.flows} connection{hn.counts.flows === 1 ? "" : "s"} observed
            {hn.counts.unclassified > 0 && <> · <span className="text-warn">{hn.counts.unclassified} not in policy</span></>}
            {hn.counts.denied > 0 && <> · <span className="text-danger">{hn.counts.denied} denied or would be</span></>}
            {hn.counts.new_external_24h > 0 && <> · {hn.counts.new_external_24h} new external (24 h)</>}
          </span>
        )}
      </p>
      <p className="text-xs text-muted">Listening on {hn.listening.length} socket{hn.listening.length === 1 ? "" : "s"}{hn.listening.length > 0 && `: ${hn.listening.slice(0, 4).map((l) => `${l.port}/${l.protocol}`).join(", ")}${hn.listening.length > 4 ? " …" : ""}`}</p>
      {(notable.data?.items ?? []).length > 0 && (
        <ul className="space-y-1 text-xs" aria-label="Connections no policy describes">
          {notable.data!.items.map((f) => (
            <li key={f.id} className="flex flex-wrap items-baseline gap-x-2">
              <DirectionLabel dir={f.direction} />
              <span className="font-mono">{f.process || "unknown process"}</span>
              <span>{f.direction === "in" ? "from" : "to"} {f.peer_host?.name ?? f.remote_ip} · {f.protocol.toUpperCase()} {f.port}</span>
              {f.geo?.org && <span className="text-muted">{f.geo.org}</span>}
              <PolicyBadge flow={f} />
            </li>
          ))}
          {notable.data!.items.some((f) => f.geo?.org) && <li><GeoAttribution state={notable.data!.enrichment_state} /></li>}
        </ul>
      )}
    </div>
  );
}

// Which of the host's own addresses a socket accepts connections on (its
// bind address): not who can reach it, which the firewalls decide.
const boundTo = (e: string) =>
  ({ "all addresses": "all of the host's addresses", "one address": "one of the host's addresses", "this host only (loopback)": "loopback only (this host)" })[e] ?? e;

function Listening({ hn }: { hn: NonNullable<ReturnType<typeof useHostNetwork>["data"]> }) {
  return (
    <section aria-label="Listening">
      <h3 className="mb-1 text-sm font-medium">Listening ({hn.listening.length})</h3>
      {hn.listening.length === 0 ? (
        <p className="text-xs text-muted">No listening sockets in the inventory yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="rtable w-full text-left text-sm">
            <caption className="sr-only">Listening sockets</caption>
            <thead>
              <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
                <th scope="col" className="px-2 py-1 font-medium">Listening on</th>
                <th scope="col" className="px-2 py-1 font-medium">Process</th>
                <th scope="col" className="px-2 py-1 font-medium">Service</th>
                <th scope="col" className="px-2 py-1 font-medium">Accepts on</th>
                <th scope="col" className="px-2 py-1 font-medium">Observed inbound</th>
              </tr>
            </thead>
            <tbody>
              {hn.listening.map((l) => (
                <tr key={`${l.protocol}|${l.address}|${l.port}`} className="border-b border-line last:border-0">
                  <td data-primary="" className="px-2 py-1 font-mono text-xs">{l.address.includes(":") ? `[${l.address}]` : l.address}:{l.port} <span className="text-muted">{l.protocol.toUpperCase()}</span></td>
                  <td data-label="Process" className="px-2 py-1">{l.process ? <span className="font-mono text-xs">{l.process}</span> : <span className="text-xs italic text-muted">Unknown</span>}</td>
                  <td data-label="Service" className="px-2 py-1 text-xs">{l.service || "—"}</td>
                  <td data-label="Accepts on" className="px-2 py-1 text-xs">{boundTo(l.exposure)}</td>
                  <td data-label="Observed inbound" className="px-2 py-1 text-xs">{l.flows > 0 ? `${l.flows} flow(s) from ${l.peers} address(es)` : "none seen"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {hn.listening_at && <p className="mt-1 text-[11px] text-muted">From the inventory, collected {formatAgo(hn.listening_at)}. Process names show only where DROOL could see them. Who can reach a port is up to the firewalls.</p>}
    </section>
  );
}
