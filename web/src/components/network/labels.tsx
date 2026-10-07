import { Badge } from "@/components/ui/Badge";
import type { NetCollection, NetFlow } from "./queries";

// Every status is shown with words and a symbol, never by colour alone.

export const policyText: Record<string, { label: string; icon: string; tone: "ok" | "warn" | "danger" | "accent" | "neutral"; help: string }> = {
  covered: { label: "Covered by policy", icon: "✓", tone: "ok", help: "A configured firewall policy allows this connection." },
  management: { label: "DROOL management", icon: "⛨", tone: "accent", help: "A protected DROOL management path (SSH from the controller or a jump host, or the agent's connection)." },
  unclassified: { label: "Unclassified", icon: "?", tone: "warn", help: "No firewall policy says anything about this connection." },
  denied: { label: "Denied", icon: "✕", tone: "danger", help: "A deny policy, or the default drop on an enforced host, blocks this connection." },
};

export function PolicyBadge({ flow }: { flow: Pick<NetFlow, "policy"> }) {
  const p = flow.policy;
  const t = policyText[p.status] ?? policyText.unclassified!;
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge tone={t.tone}><span aria-hidden className="mr-1">{t.icon}</span>{t.label}</Badge>
      {p.status !== "denied" && p.would_deny && <Badge tone="danger"><span aria-hidden className="mr-1">✕</span>would be denied</Badge>}
      {p.applied_verdict && <Badge tone="warn"><span aria-hidden className="mr-1">↻</span>not applied yet</Badge>}
    </span>
  );
}

export const scopeText: Record<string, { label: string; icon: string }> = {
  internal: { label: "Internal", icon: "⌂" },
  public: { label: "External", icon: "◎" },
  special: { label: "Special", icon: "◌" },
};

export function ScopeBadge({ flow }: { flow: Pick<NetFlow, "scope" | "scope_range"> }) {
  const s = scopeText[flow.scope] ?? scopeText.special!;
  return (
    <span title={flow.scope_range ? `${s.label}: ${flow.scope_range}` : s.label} className="inline-flex items-center gap-1 text-xs">
      <span aria-hidden>{s.icon}</span>{s.label}
      {flow.scope === "special" && flow.scope_range && <span className="text-muted">({flow.scope_range})</span>}
    </span>
  );
}

export function DirectionLabel({ dir }: { dir: string }) {
  return dir === "in"
    ? <span className="whitespace-nowrap"><span aria-hidden>↘ </span>Inbound</span>
    : <span className="whitespace-nowrap"><span aria-hidden>↗ </span>Outbound</span>;
}

export function ProcessLabel({ flow }: { flow: Pick<NetFlow, "process" | "pid" | "service" | "protocol" | "port"> }) {
  return (
    <span className="inline-flex flex-col">
      {flow.process ? <span className="font-mono text-xs">{flow.process}</span> : <span className="text-xs italic text-muted">Unknown</span>}
      {flow.service && <span className="text-[11px] text-muted">service: {flow.service}</span>}
    </span>
  );
}

export const ep = (ip: string, port: number) => (ip.includes(":") ? `[${ip}]:${port}` : `${ip}:${port}`);

/** An address and port that wraps only inside long IPv6 addresses, never
 * inside an IPv4 address or the port. */
export function Endpoint({ ip, port }: { ip: string; port: number }) {
  if (!ip.includes(":")) return <span className="whitespace-nowrap font-mono text-xs">{ip}:{port}</span>;
  return <span className="font-mono text-xs [overflow-wrap:anywhere]">[{ip}]<span className="whitespace-nowrap">:{port}</span></span>;
}

/** The remote end: a DROOL host's identity for internal addresses,
 * country and network owner for public ones — never without the raw IP. */
export function RemoteLabel({ flow }: { flow: NetFlow }) {
  return (
    <span className="inline-flex flex-col">
      {flow.peer_host && <span className="font-medium">{flow.peer_host.name}</span>}
      <Endpoint ip={flow.remote_ip} port={flow.remote_port} />
      {flow.peer_host && flow.peer_host.groups.length > 0 && <span className="text-[11px] text-muted">group: {flow.peer_host.groups.join(", ")}</span>}
      {!flow.peer_host && flow.peer_network && <span className="text-[11px] text-muted">network: {flow.peer_network}</span>}
    </span>
  );
}

export function CountryLabel({ flow }: { flow: NetFlow }) {
  if (flow.scope !== "public") return <span className="text-xs text-muted">{flow.scope === "internal" ? "internal" : "—"}</span>;
  if (!flow.geo?.country_code) return <span className="text-xs text-muted">country unknown</span>;
  return <span className="text-xs" title={flow.geo.country_code}>{flow.geo.country || flow.geo.country_code} <span className="text-muted">({flow.geo.country_code})</span></span>;
}

export function NetworkLabel({ flow }: { flow: NetFlow }) {
  if (flow.scope !== "public") return null;
  if (!flow.geo?.asn) return <span className="text-xs text-muted">network unknown</span>;
  return <span className="text-xs" title={`AS${flow.geo.asn} ${flow.geo.org}`}><span className="text-muted">AS{flow.geo.asn}</span> {flow.geo.org}</span>;
}

const time = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
const day = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });

/** "Today 14:22", "Yesterday 09:10", "Mar 3 18:00". */
export function when(iso: string | undefined | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const today = new Date();
  const y = new Date();
  y.setDate(today.getDate() - 1);
  const same = (a: Date, b: Date) => a.toDateString() === b.toDateString();
  return `${same(d, today) ? "Today" : same(d, y) ? "Yesterday" : day.format(d)} ${time.format(d)}`;
}

export const sourceText: Record<string, string> = {
  conntrack: "Continuous: conntrack",
  sockets: "Continuous: socket sampling",
  snapshot: "Snapshot",
};

/** How a host is observed, honestly worded. */
export function CollectionBadge({ c }: { c: NetCollection }) {
  const tone = c.source === "conntrack" ? "ok" : c.mode === "continuous" ? "accent" : c.mode === "snapshot" ? "neutral" : "warn";
  return <Badge tone={tone}>{c.label}</Badge>;
}
