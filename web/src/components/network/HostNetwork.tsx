import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatAgo } from "@/lib/format";
import { CollectionBadge, DirectionLabel, PolicyBadge } from "./labels";
import { FlowTable } from "./FlowTable";
import { nk, useHostNetwork, useNetFlows, type NetFilter } from "./queries";

const views: { id: string; label: string; f: NetFilter }[] = [
  { id: "all", label: "All", f: {} },
  { id: "in", label: "Inbound", f: { direction: "in" } },
  { id: "out", label: "Outbound", f: { direction: "out" } },
  { id: "internal", label: "Internal", f: { scope: "internal" } },
  { id: "external", label: "External", f: { scope: "external" } },
  { id: "new", label: "New external (24 h)", f: { new: "host", since: "24h", direction: "out" } },
  { id: "unclassified", label: "Unclassified", f: { policy: "unclassified" } },
];

/** A host's network activity, on its own page: how it is observed, what
 * listens, what connects in, what it connects out to — and what the
 * firewall policy says. */
export function HostNetworkPanel({ hostId }: { hostId: string }) {
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
    <div className="grid gap-4">
      <Card title="How this host is observed" actions={
        <span className="flex flex-wrap items-center gap-2">
          {hn && <CollectionBadge c={hn.collection} />}
          {hn && hn.collection.mode !== "continuous" && can("network.snapshot") && (
            <Button size="sm" onClick={() => snap.mutate()} disabled={snap.isPending}>{snap.isPending ? "Taking snapshot…" : "Take snapshot"}</Button>
          )}
        </span>
      }>
        <div className="space-y-3">
          {q.error && <Alert>{errorMessage(q.error)}</Alert>}
          {!hn && !q.error && <p className="text-sm text-muted">Loading…</p>}
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
        </div>
      </Card>
      <Card title="Connections">
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
      </Card>
    </div>
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
            {hn.counts.unclassified > 0 && <> · <span className="text-warn">{hn.counts.unclassified} unclassified</span></>}
            {hn.counts.denied > 0 && <> · <span className="text-danger">{hn.counts.denied} denied or would be</span></>}
            {hn.counts.new_external_24h > 0 && <> · {hn.counts.new_external_24h} new external (24 h)</>}
          </span>
        )}
      </p>
      <p className="text-xs text-muted">Listening on {hn.listening.length} socket{hn.listening.length === 1 ? "" : "s"}{hn.listening.length > 0 && `: ${hn.listening.slice(0, 4).map((l) => `${l.port}/${l.protocol}`).join(", ")}${hn.listening.length > 4 ? " …" : ""}`}</p>
      {(notable.data?.items ?? []).length > 0 && (
        <ul className="space-y-1 text-xs" aria-label="Unclassified connections">
          {notable.data!.items.map((f) => (
            <li key={f.id} className="flex flex-wrap items-baseline gap-x-2">
              <DirectionLabel dir={f.direction} />
              <span className="font-mono">{f.process || "unknown process"}</span>
              <span>{f.direction === "in" ? "from" : "to"} {f.peer_host?.name ?? f.remote_ip} · {f.protocol.toUpperCase()} {f.port}</span>
              {f.geo?.org && <span className="text-muted">{f.geo.org}</span>}
              <PolicyBadge flow={f} />
            </li>
          ))}
        </ul>
      )}
    </div>
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
          <table className="rtable w-full text-left text-sm">
            <caption className="sr-only">Listening sockets</caption>
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
                  <td data-primary="" className="px-2 py-1 font-mono text-xs">{l.address.includes(":") ? `[${l.address}]` : l.address}:{l.port} <span className="text-muted">{l.protocol.toUpperCase()}</span></td>
                  <td data-label="Process" className="px-2 py-1">{l.process ? <span className="font-mono text-xs">{l.process}</span> : <span className="text-xs italic text-muted">Unknown</span>}</td>
                  <td data-label="Service" className="px-2 py-1 text-xs">{l.service || "—"}</td>
                  <td data-label="Reachable from" className="px-2 py-1 text-xs">{l.exposure}</td>
                  <td data-label="Observed inbound" className="px-2 py-1 text-xs">{l.flows > 0 ? `${l.flows} flow(s) from ${l.peers} address(es)` : "none seen"}</td>
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
