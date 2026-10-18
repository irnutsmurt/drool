import { Badge } from "@/components/ui/Badge";
import type { NetCollection, NetEnrichState, NetFlow } from "./queries";

// Every status is shown with words and a symbol, never by colour alone.
//
// A status is the policy's verdict on connections like this one. Whether it
// decided anything depends on the host: where DROOL's rules aren't in force
// (enforcement other than "enforced"), the verdict is only what the policy
// would do, and an observed connection was never allowed or blocked by it.

export const policyText: Record<string, { label: string; icon: string; tone: "ok" | "warn" | "danger" | "accent" | "neutral"; help: string }> = {
  covered: { label: "Covered by policy", icon: "✓", tone: "ok", help: "The firewall policy allows connections like this one." },
  management: { label: "Management path", icon: "⛨", tone: "accent", help: "A protected DROOL management path in the firewall policy (SSH from the controller or a jump host, or the Shepherd's connection)." },
  unclassified: { label: "Not in policy", icon: "?", tone: "warn", help: "No firewall policy says anything about connections like this one." },
  denied: { label: "Denied", icon: "✕", tone: "danger", help: "The policy enforced on this host drops connections like this one. This one was observed, so it got through when it was seen: before the policy took effect, or around DROOL's rules." },
};

/** The host firewall's state behind a connection's policy status. */
export const enforcementText: Record<string, string> = {
  unmanaged: "DROOL does not filter this host (mode Off)",
  monitor: "Monitor mode: DROOL does not filter this host",
  guided: "Guided mode: DROOL does not filter this host",
  enforce_pending: "Enforce mode, but the policy isn't applied yet: DROOL does not filter this host",
  enforced: "DROOL's policy is in force on this host",
};

export const isEnforced = (p: Pick<NetFlow["policy"], "enforcement">) => p.enforcement === "enforced";

export function PolicyBadge({ flow }: { flow: Pick<NetFlow, "policy"> }) {
  const p = flow.policy;
  const enforced = isEnforced(p);
  let t = policyText[p.status] ?? policyText.unclassified!;
  // Nothing was denied where the policy isn't in force.
  if (p.status === "denied" && !enforced) t = { ...t, label: "Would be denied", help: "The policy would drop connections like this one, but it isn't in force on this host: nothing was blocked." };
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge tone={t.tone}><span aria-hidden className="mr-1">{t.icon}</span>{t.label}</Badge>
      {p.status !== "denied" && p.would_deny && <Badge tone="danger"><span aria-hidden className="mr-1">✕</span>would be denied</Badge>}
      {!enforced && p.status !== "unclassified" && <Badge tone="neutral"><span aria-hidden className="mr-1">◌</span>not enforced</Badge>}
      {p.applied_verdict && <Badge tone="warn"><span aria-hidden className="mr-1">↻</span>not applied yet</Badge>}
    </span>
  );
}

/** What a flow is (its classification), worded with a symbol. */
export const classText: Record<string, { kind: string; icon: string; tone: "ok" | "warn" | "danger" | "accent" | "neutral"; help: string }> = {
  drool: { kind: "DROOL management", icon: "⛨", tone: "accent", help: "DROOL's own traffic: DROOL has direct evidence it is its connection." },
  approved: { kind: "Expected", icon: "✓", tone: "ok", help: "Classified, and marked as expected." },
  user: { kind: "Classified", icon: "◆", tone: "neutral", help: "Classified by someone on your team." },
  service: { kind: "Known service", icon: "≡", tone: "neutral", help: "Named after its port: nothing else says what it is." },
  unknown: { kind: "Unknown", icon: "?", tone: "neutral", help: "Nothing says what this is yet." },
};

export const classSource: Record<string, string> = {
  drool: "DROOL's own evidence",
  rule: "a classification",
  manual: "labeled from one connection",
  port: "its port",
  none: "nothing yet",
};

/** The classification: kind as a badge, the label under it. */
export function ClassBadge({ flow }: { flow: Pick<NetFlow, "classification"> }) {
  const c = flow.classification;
  const t = classText[c.kind] ?? classText.unknown!;
  return (
    <span className="inline-flex flex-col items-start gap-0.5" title={`${t.help} How DROOL knows: ${classSource[c.source] ?? c.source}.`}>
      <Badge tone={t.tone}><span aria-hidden className="mr-1">{t.icon}</span>{t.kind}</Badge>
      {c.kind !== "unknown" && <span className="text-[11px] text-muted">{c.label}</span>}
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
      {flow.peer_host && flow.peer_host.groups.length > 0 && <span className="text-[11px] text-muted">pack: {flow.peer_host.groups.join(", ")}</span>}
      {!flow.peer_host && flow.peer_network && <span className="text-[11px] text-muted">network: {flow.peer_network}</span>}
    </span>
  );
}

/** Why a public address has no country or network owner, when it wasn't
 * looked up at all: lookups off, the database still being prepared, or no
 * database of that kind. "Unknown" is kept for a database that genuinely
 * has no answer (state "on"). */
export function noLookup(state: NetEnrichState | undefined, kind: "country" | "asn"): string | null {
  const s = state?.[kind];
  if (s === "off") return kind === "country" ? "country lookups are off" : "network-owner lookups are off";
  if (s === "preparing") return kind === "country" ? "the country database is still being prepared" : "the ASN database is still being prepared";
  if (s === "unavailable") {
    if (state?.downloads) return kind === "country" ? "the country database couldn't be downloaded yet" : "the ASN database couldn't be downloaded yet";
    return kind === "country" ? "no country database is configured" : "no ASN database is configured";
  }
  return null;
}

/** Not looked up: a dash (the page says why once), with the reason for
 * screen readers and on hover. */
function NotLookedUp({ why }: { why: string }) {
  return <span className="text-xs text-muted" title={why}><span aria-hidden="true">—</span><span className="sr-only">{why}</span></span>;
}

export function CountryLabel({ flow, state }: { flow: NetFlow; state?: NetEnrichState }) {
  if (flow.scope !== "public") return <span className="text-xs text-muted">{flow.scope === "internal" ? "internal" : "—"}</span>;
  if (!flow.geo?.country_code) {
    const why = noLookup(state, "country");
    return why ? <NotLookedUp why={why} /> : <span className="text-xs text-muted">country unknown</span>;
  }
  return <span className="text-xs" title={flow.geo.country_code}>{flow.geo.country || flow.geo.country_code} <span className="text-muted">({flow.geo.country_code})</span></span>;
}

export function NetworkLabel({ flow, state }: { flow: NetFlow; state?: NetEnrichState }) {
  if (flow.scope !== "public") return null;
  if (!flow.geo?.asn) {
    const why = noLookup(state, "asn");
    return why ? <NotLookedUp why={why} /> : <span className="text-xs text-muted">network unknown</span>;
  }
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
