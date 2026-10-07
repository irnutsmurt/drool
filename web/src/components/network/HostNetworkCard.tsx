import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatAgo } from "@/lib/format";
import { CollectionBadge } from "./labels";
import { FlowTable } from "./FlowTable";
import { nk, useHostNetwork, type NetFilter } from "./queries";

const views: { id: string; label: string; f: NetFilter }[] = [
  { id: "all", label: "All", f: {} },
  { id: "in", label: "Inbound", f: { direction: "in" } },
  { id: "out", label: "Outbound", f: { direction: "out" } },
  { id: "internal", label: "Internal", f: { scope: "internal" } },
  { id: "external", label: "External", f: { scope: "external" } },
  { id: "new", label: "New external (24 h)", f: { new: "host", since: "24h", direction: "out" } },
  { id: "unclassified", label: "Unclassified", f: { policy: "unclassified" } },
];

/** A host's Network connections: how it is observed, what listens, what
 * connects in, what it connects out to — and what the policy says. */
export function HostNetworkCard({ hostId }: { hostId: string }) {
  const q = useHostNetwork(hostId);
  const { can } = useAuth();
  const qc = useQueryClient();
  const [view, setView] = useState("all");
  const snap = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/network/snapshot", { params: { path: { id: hostId } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
  const hn = q.data;
  const filter = { host: [hostId], ...views.find((v) => v.id === view)!.f };
  return (
    <Card title="Network connections" actions={
      <span className="flex items-center gap-2">
        {hn && <CollectionBadge c={hn.collection} />}
        {hn && hn.collection.mode !== "continuous" && can("network.snapshot") && (
          <Button size="sm" onClick={() => snap.mutate()} disabled={snap.isPending}>{snap.isPending ? "Taking snapshot…" : "Take snapshot"}</Button>
        )}
        <Link className="text-xs text-accent" to={`/network?host=${hostId}`}>Open in Network activity</Link>
      </span>
    }>
      <div id="network" className="space-y-4">
        {q.error && <Alert>{errorMessage(q.error)}</Alert>}
        {snap.error && <Alert>{errorMessage(snap.error)}</Alert>}
        {snap.isSuccess && <Alert tone="ok">Snapshot taken: {snap.data.flows} connection group(s) open right now.</Alert>}
        {hn && (
          <p className="text-xs text-muted" data-testid="collection-detail">
            {hn.collection.detail}
            {hn.collection.reported_at && <> · last report {formatAgo(hn.collection.reported_at)}</>}
            {hn.collection.snapshot_at && <> · last snapshot {formatAgo(hn.collection.snapshot_at)}</>}
            {hn.collection.events_lost > 0 && <> · <span className="text-warn">⚠ some new-connection events were lost (counts may be low)</span></>}
            {hn.collection.forwarded > 0 && <> · {hn.collection.forwarded} routed connection(s) through this host not shown (containers, routing)</>}
            {!hn.enrichment && <> · country and network: not available (no GeoIP/ASN database)</>}
          </p>
        )}
        {hn && <Listening hn={hn} />}
        <div>
          <div className="mb-2 flex flex-wrap items-center gap-1.5" role="group" aria-label="Show connections">
            {views.map((v) => (
              <button key={v.id} type="button" aria-pressed={view === v.id} onClick={() => setView(v.id)}
                className={cx("rounded-full border px-2.5 py-0.5 text-xs", view === v.id ? "border-accent bg-accent text-accent-fg" : "border-line bg-surface hover:bg-surface-2")}>
                {v.label}
              </button>
            ))}
            {hn && <span className="ml-auto text-xs text-muted">{hn.counts.flows} total · {hn.counts.unclassified} unclassified · {hn.counts.new_external_24h} new external (24 h)</span>}
          </div>
          <FlowTable filter={filter} showHost={false} pageSize={25}
            empty={hn?.collection.mode === "snapshot" && !hn.collection.snapshot_at ? "No snapshot taken yet. SSH hosts are observed only when you take a snapshot." : "No connections observed."} />
        </div>
      </div>
    </Card>
  );
}

function Listening({ hn }: { hn: NonNullable<ReturnType<typeof useHostNetwork>["data"]> }) {
  return (
    <section aria-label="Listening">
      <h3 className="mb-1 text-sm font-medium">Listening ({hn.listening.length})</h3>
      {hn.listening.length === 0 ? (
        <p className="text-xs text-muted">No listening sockets in the inventory yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
                <th scope="col" className="px-2 py-1 font-medium">Listening on</th>
                <th scope="col" className="px-2 py-1 font-medium">Process</th>
                <th scope="col" className="px-2 py-1 font-medium">Service</th>
                <th scope="col" className="px-2 py-1 font-medium">Reachable from</th>
                <th scope="col" className="px-2 py-1 font-medium">Observed inbound</th>
              </tr>
            </thead>
            <tbody>
              {hn.listening.map((l) => (
                <tr key={`${l.protocol}|${l.address}|${l.port}`} className="border-b border-line last:border-0">
                  <td className="px-2 py-1 font-mono text-xs">{l.address.includes(":") ? `[${l.address}]` : l.address}:{l.port} <span className="text-muted">{l.protocol.toUpperCase()}</span></td>
                  <td className="px-2 py-1">{l.process ? <span className="font-mono text-xs">{l.process}</span> : <span className="text-xs italic text-muted">Unknown</span>}</td>
                  <td className="px-2 py-1 text-xs">{l.service || "—"}</td>
                  <td className="px-2 py-1 text-xs">{l.exposure}</td>
                  <td className="px-2 py-1 text-xs">{l.flows > 0 ? `${l.flows} flow(s) from ${l.peers} address(es)` : "none seen"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {hn.listening_at && <p className="mt-1 text-[11px] text-muted">From the inventory, collected {formatAgo(hn.listening_at)}. Process names show only where DROOL could see them.</p>}
    </section>
  );
}
