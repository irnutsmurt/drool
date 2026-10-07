import { useNavigate } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage, formatDateTime } from "@/lib/format";
import { CollectionBadge, DirectionLabel, PolicyBadge, ScopeBadge, ep, policyText, when } from "./labels";
import { useNetFlow } from "./queries";

const enforcementText: Record<string, string> = {
  unmanaged: "DROOL does not manage this host's firewall",
  monitor: "Monitor mode (not enforced)",
  guided: "Guided mode (not enforced)",
  enforce_pending: "Enforce mode, policy not applied yet",
  enforced: "Enforced",
};

/** One observed connection: who, where, what, and what the firewall
 * policy says about it. */
export function FlowDialog({ id, onClose }: { id: number; onClose: () => void }) {
  const q = useNetFlow(id);
  const { can } = useAuth();
  const navigate = useNavigate();
  const f = q.data;
  const src = f && (f.direction === "out" ? { name: f.host.name, ep: ep(f.local_ip, f.local_port) } : { name: f.peer_host?.name ?? f.remote_ip, ep: ep(f.remote_ip, f.remote_port) });
  const dst = f && (f.direction === "out" ? { name: f.peer_host?.name ?? f.remote_ip, ep: ep(f.remote_ip, f.remote_port) } : { name: f.host.name, ep: ep(f.local_ip, f.local_port) });
  const t = f && (policyText[f.policy.status] ?? policyText.unclassified!);
  return (
    <Dialog title="Observed connection" onClose={onClose} wide>
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {!f ? <p className="text-sm text-muted">Loading…</p> : (
        <div className="space-y-4 text-sm" data-testid="flow-details">
          <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1.5">
            <dt className="text-muted">Direction</dt><dd><DirectionLabel dir={f.direction} /> · <ScopeBadge flow={f} /></dd>
            <dt className="text-muted">Source</dt><dd><strong>{src!.name}</strong> <span className="font-mono text-xs">{src!.ep}</span></dd>
            <dt className="text-muted">Destination</dt><dd><strong>{dst!.name}</strong> <span className="font-mono text-xs">{dst!.ep}</span></dd>
            {f.peer_host && <><dt className="text-muted">Remote host</dt><dd>{f.peer_host.name}{f.peer_host.groups.length > 0 && <> · group: {f.peer_host.groups.join(", ")}</>}</dd></>}
            {f.peer_network && <><dt className="text-muted">Network object</dt><dd>{f.peer_network}</dd></>}
            {f.scope === "internal" && f.scope_range && <><dt className="text-muted">Internal range</dt><dd>{f.scope_range}</dd></>}
            {f.scope === "public" && (
              <>
                <dt className="text-muted">Country</dt><dd>{f.geo?.country_code ? `${f.geo.country || f.geo.country_code} (${f.geo.country_code})` : "Unknown"}</dd>
                <dt className="text-muted">Network</dt><dd>{f.geo?.asn ? `AS${f.geo.asn} ${f.geo.org}` : "Unknown"}</dd>
              </>
            )}
            <dt className="text-muted">Protocol</dt><dd>{f.protocol.toUpperCase()}</dd>
            <dt className="text-muted">Port</dt><dd>{f.port}{f.service && <> ({f.service})</>}</dd>
            <dt className="text-muted">Process</dt><dd>{f.process ? <span className="font-mono">{f.process}{f.pid ? ` (pid ${f.pid})` : ""}</span> : <span className="italic text-muted">Unknown — DROOL couldn't tie this connection to a process</span>}</dd>
            <dt className="text-muted">First seen</dt><dd>{formatDateTime(f.first_seen)}</dd>
            <dt className="text-muted">Last seen</dt><dd>{formatDateTime(f.last_seen)}</dd>
            <dt className="text-muted">Connections</dt><dd>{f.count.toLocaleString()}</dd>
            {f.peer_first_seen && <><dt className="text-muted">Address first seen</dt><dd>{when(f.peer_first_seen)} by {f.host.name}{f.fleet_first_seen && <>; {when(f.fleet_first_seen)} by any host</>}</dd></>}
            <dt className="text-muted">Observed by</dt><dd><CollectionBadge c={f.collection} /> {f.collection.detail && <span className="text-xs text-muted">{f.collection.detail}</span>}</dd>
          </dl>

          <section className="rounded-ui border border-line p-3" aria-label="Firewall">
            <h3 className="mb-2 font-semibold">Firewall</h3>
            <p className="flex flex-wrap items-center gap-2">
              <PolicyBadge flow={f} />
              {f.policy.policy_name ? <span>{f.policy.status === "denied" ? "Denied" : "Allowed"} by policy <strong>“{f.policy.policy_name}”</strong>{f.policy.rule && <span className="text-muted"> ({f.policy.rule})</span>}</span>
                : <span>{t!.help} <span className="text-muted">({f.policy.reason})</span></span>}
            </p>
            <p className="mt-1 text-xs text-muted">Host firewall: {enforcementText[f.policy.enforcement] ?? f.policy.enforcement}.</p>
            {f.policy.notes.length > 0 && <ul className="mt-2 list-disc space-y-0.5 pl-5 text-xs">{f.policy.notes.map((n) => <li key={n}>{n}</li>)}</ul>}
            {f.suggestion && (
              <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-line pt-3">
                <span className="text-xs text-muted">Suggested: {f.suggestion.title}</span>
                {can("firewall.read") && (
                  <Button size="sm" variant="primary" onClick={() => navigate(`/firewall?tab=designer&view=rules&suggest=${f.id}`)}>
                    Create policy from observed connection…
                  </Button>
                )}
                <span className="w-full text-[11px] text-muted">Opens a draft in the Firewall Designer. Nothing is saved or applied until you review it, save it, and apply it.</span>
              </div>
            )}
          </section>

          {f.scope === "public" && (
            <p className="text-[11px] text-muted">Country and network come from the local GeoIP/ASN databases. They are context, not a verdict, and may be inaccurate or out of date.</p>
          )}

          {f.related.length > 0 && (
            <section aria-label="Other connections with this address">
              <h3 className="mb-1 font-semibold">Other connections between {f.host.name} and {f.remote_ip}</h3>
              <ul className="space-y-0.5 text-xs">
                {f.related.map((r) => (
                  <li key={r.id}><DirectionLabel dir={r.direction} /> {r.protocol.toUpperCase()} {r.port} · {r.count} connections · last {when(r.last_seen)} · {policyText[r.policy.status]?.label}</li>
                ))}
              </ul>
            </section>
          )}
          <div className="flex justify-end"><Button onClick={onClose}>Close</Button></div>
        </div>
      )}
    </Dialog>
  );
}
