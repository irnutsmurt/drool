/**
 * The Firewall Designer's model: pure functions over the canonical firewall
 * document (the server's FwDocument). Topology, Rules and YAML are views of
 * one draft document; every edit in any view goes through these functions
 * and produces a new document. Nothing here evaluates policy — previews,
 * Explain, simulation and management-path checks come from the server,
 * which compiles the draft with the real compiler.
 */

export type Entry = { protocol: "tcp" | "udp" | "icmp" | "icmpv6"; ports?: string; icmp_type?: string };
export type Network = { name: string; description: string; cidrs: string[] };
export type Group = { name: string; description: string; members: string[] };
export type Service = { name: string; description: string; entries: Entry[] };
export type Rule = {
  name: string;
  description: string;
  enabled: boolean;
  action: "allow" | "deny";
  from: string[];
  to: string[];
  services: string[];
};
export type Doc = { networks: Network[]; groups: Group[]; services: Service[]; policy: Rule[] };

export const emptyDoc = (): Doc => ({ networks: [], groups: [], services: [], policy: [] });

// --- references ---------------------------------------------------------

export type RefKind = "host" | "group" | "network" | "cidr" | "internet" | "internal" | "any";
export type Ref = { kind: RefKind; name: string };

export const keywords: RefKind[] = ["internet", "internal", "any"];

export function parseRef(s: string): Ref {
  if ((keywords as string[]).includes(s)) return { kind: s as RefKind, name: "" };
  const i = s.indexOf(":");
  const kind = s.slice(0, i) as RefKind;
  return { kind, name: s.slice(i + 1) };
}

export function refString(r: Ref): string {
  return (keywords as string[]).includes(r.kind) ? r.kind : `${r.kind}:${r.name}`;
}

/** Plain-language label of a reference. */
export function refLabel(s: string): string {
  const r = parseRef(s);
  switch (r.kind) {
    case "internet":
      return "Internet";
    case "internal":
      return "Internal networks";
    case "any":
      return "Anywhere";
    case "cidr":
      return r.name;
  }
  return r.name;
}

export const kindLabel: Record<RefKind, string> = {
  host: "Host",
  group: "Pack",
  network: "Network",
  cidr: "Address range (CIDR)",
  internet: "Internet",
  internal: "Internal networks",
  any: "Anywhere",
};

// --- the rule builder's choices ------------------------------------------

/** One choice in the rule builder's pickers (ui/ComboPicker's PickOption). */
export type RefChoice = { value: string; label: string; detail?: string; group: string; disabled?: string; search?: string };

/** An address or range typed into a picker ("10.0.0.5", "192.168.1.0/24",
 * "2001:db8::/32"), as a cidr reference; null when it isn't one. */
export function cidrFromQuery(q: string): string | null {
  const v = q.trim();
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?$/.exec(v);
  if (v4) {
    if (v4.slice(1, 5).some((o) => Number(o) > 255) || (v4[5] !== undefined && Number(v4[5]) > 32)) return null;
    return `cidr:${v4.slice(1, 5).join(".")}/${v4[5] ?? "32"}`;
  }
  const v6 = /^([0-9a-fA-F:]+)(?:\/(\d{1,3}))?$/.exec(v);
  if (v6 && v6[1]!.includes(":") && /^[0-9a-fA-F]{0,4}(:[0-9a-fA-F]{0,4}){2,7}$/.test(v6[1]!) && (v6[2] === undefined || Number(v6[2]) <= 128)) {
    return `cidr:${v6[1]!.toLowerCase()}/${v6[2] ?? "128"}`;
  }
  return null;
}

/** The choices for a rule's source or destination: every kind at once.
 * For a destination, hosts and Packs come first: DROOL puts a rule on the
 * hosts its destination names, so an address there (a network, Internal
 * networks, the Internet) gives no host an inbound rule — it only limits
 * what a host or Pack source may send (outbound). */
export function refChoices(doc: Doc, hosts: { name: string }[], side: "source" | "destination"): RefChoice[] {
  const packOf = new Map<string, string[]>();
  for (const g of doc.groups) for (const m of g.members) packOf.set(m.toLowerCase(), [...(packOf.get(m.toLowerCase()) ?? []), g.name]);
  const hostChoices = hosts.map((h) => ({ value: `host:${h.name}`, label: h.name, group: "Hosts", detail: (packOf.get(h.name.toLowerCase()) ?? []).join(", ") || undefined }));
  const packChoices = doc.groups.map((g) => ({ value: `group:${g.name}`, label: g.name, group: "Packs", detail: `${g.members.length} host${g.members.length === 1 ? "" : "s"}` }));
  const networkChoices = (group: string) => doc.networks.map((n) => ({ value: `network:${n.name}`, label: n.name, group, detail: n.cidrs.join(", ") }));
  if (side === "source") {
    return [
      { value: "internal", label: "Internal networks", group: "Common", detail: "private address ranges (your LAN)", search: "lan local private" },
      { value: "any", label: "Anywhere", group: "Common", detail: "any address", search: "all everyone" },
      { value: "internet", label: "Internet", group: "Common", detail: "everything outside the internal ranges", search: "public external wan" },
      ...hostChoices, ...packChoices, ...networkChoices("Networks"),
    ];
  }
  const addr = "Addresses (outbound only)";
  return [
    ...hostChoices, ...packChoices,
    { value: "any", label: "Anywhere", group: "Every host", detail: "every host DROOL manages", search: "all hosts" },
    { value: "internal", label: "Internal networks", group: addr, detail: "limits what a host or Pack source sends", search: "lan local private" },
    { value: "internet", label: "Internet", group: addr, detail: "limits what a host or Pack source sends", search: "public external wan" },
    ...networkChoices(addr),
  ];
}

/** An address typed into a picker, offered as a choice. */
export function addressChoice(q: string, side: "source" | "destination"): RefChoice[] {
  const c = cidrFromQuery(q);
  if (!c) return [];
  return [{ value: c, label: c.slice(5), group: side === "source" ? "Address" : "Addresses (outbound only)", detail: "an address or range" }];
}

/** A port typed into the service picker: "8812", "tcp 8812", "udp/53",
 * "8000-8010", "80,443", "8812/udp". Without a protocol, both are offered
 * (TCP first). */
export function portChoices(q: string): { protocol: "tcp" | "udp"; ports: string }[] {
  const v = q.trim().toLowerCase();
  const m = /^(?:(tcp|udp)[\s/:]*)?([\d][\d,\s-]*?)(?:[\s/:]*(tcp|udp))?$/.exec(v);
  if (!m) return [];
  const ports = m[2]!.replace(/\s+/g, "");
  if (!validPorts(ports)) return [];
  const proto = (m[1] ?? m[3]) as "tcp" | "udp" | undefined;
  return proto ? [{ protocol: proto, ports }] : [{ protocol: "tcp", ports }, { protocol: "udp", ports }];
}

/** A rule name from what the rule does ("allow-ssh-internal-to-pizero"),
 * unique in the document and within the server's name rules (letters,
 * digits, ".", "_", "-"; at most 63 characters). */
export function suggestRuleName(doc: Doc, rule: Pick<Rule, "action" | "from" | "to" | "services">, ignore?: number): string {
  const short = (r: string) => {
    const p = parseRef(r);
    return p.kind === "any" ? "anywhere" : p.kind === "cidr" ? p.name.replace(/[/:]/g, "-") : p.kind === "internet" || p.kind === "internal" ? p.kind : p.name;
  };
  const parts = [rule.action, rule.services[0] ?? "", rule.from[0] ? short(rule.from[0]) : "", "to", rule.to[0] ? short(rule.to[0]) : ""].filter(Boolean);
  let base = parts.join("-").toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/-+/g, "-").replace(/^[^a-z0-9]+|-+$/g, "");
  base = (base || "rule").slice(0, 56).replace(/-+$/, "");
  const others: Doc = ignore === undefined ? doc : { ...doc, policy: doc.policy.filter((_, i) => i !== ignore) };
  return uniqueRuleName(others, base);
}

// --- services -------------------------------------------------------------

export type ServiceInfo = { name: string; description: string; builtin: boolean; entries: Entry[] };

/** "TCP 22", "UDP 53 + TCP 53", "ICMP echo-request". */
export function entriesLabel(es: Entry[]): string {
  return es
    .map((e) => (e.protocol === "tcp" || e.protocol === "udp" ? `${e.protocol.toUpperCase()} ${e.ports ?? ""}` : `${e.protocol.toUpperCase()} ${e.icmp_type ?? ""}`))
    .join(" + ");
}

/** A service with what it means on the wire, e.g. "ssh (TCP 22)". */
export function serviceLabel(name: string, all: ServiceInfo[]): string {
  const s = all.find((x) => x.name === name);
  return s ? `${name} (${entriesLabel(s.entries).trim()})` : `${name} (unknown)`;
}

export function allServices(doc: Doc, builtin: ServiceInfo[]): ServiceInfo[] {
  return [...builtin.filter((b) => b.builtin), ...doc.services.map((s) => ({ ...s, builtin: false }))];
}

/** Port syntax check matching the server's (1-65535, ranges, lists). */
export function validPorts(p: string): boolean {
  const parts = p.split(",").map((x) => x.trim()).filter(Boolean);
  if (parts.length === 0) return false;
  return parts.every((part) => {
    const [lo, hi, extra] = part.split("-");
    if (extra !== undefined) return false;
    const a = Number(lo);
    const b = hi === undefined ? a : Number(hi);
    return Number.isInteger(a) && Number.isInteger(b) && a >= 1 && b <= 65535 && a <= b && /^\d+$/.test(lo!.trim()) && (hi === undefined || /^\d+$/.test(hi.trim()));
  });
}

/**
 * The service for a custom protocol/port: an existing service with exactly
 * that definition, or a new one named like "tcp-8812".
 */
export function ensureCustomService(doc: Doc, builtin: ServiceInfo[], protocol: "tcp" | "udp", ports: string): { doc: Doc; name: string } {
  const p = ports.replace(/\s+/g, "");
  for (const s of allServices(doc, builtin)) {
    if (s.entries.length === 1 && s.entries[0]!.protocol === protocol && s.entries[0]!.ports === p) return { doc, name: s.name };
  }
  let name = `${protocol}-${p.replace(/,/g, "_")}`;
  for (let i = 2; doc.services.some((s) => s.name === name) || builtin.some((b) => b.name === name); i++) name = `${protocol}-${p.replace(/,/g, "_")}-${i}`;
  return { doc: { ...doc, services: sortByName([...doc.services, { name, description: "", entries: [{ protocol, ports: p }] }]) }, name };
}

// --- edits ------------------------------------------------------------------

function sortByName<T extends { name: string }>(xs: T[]): T[] {
  return [...xs].sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
}

export function uniqueRuleName(doc: Doc, base: string): string {
  const clean = base.trim() || "rule";
  let name = clean;
  for (let i = 2; doc.policy.some((r) => r.name.toLowerCase() === name.toLowerCase()); i++) name = `${clean}-${i}`;
  return name;
}

/** Adds a rule (at the end unless index is given). */
export function addRule(doc: Doc, rule: Rule, index?: number): Doc {
  const policy = [...doc.policy];
  policy.splice(index ?? policy.length, 0, rule);
  return { ...doc, policy };
}

export function updateRule(doc: Doc, index: number, rule: Rule): Doc {
  return { ...doc, policy: doc.policy.map((r, i) => (i === index ? rule : r)) };
}

export function deleteRule(doc: Doc, index: number): Doc {
  return { ...doc, policy: doc.policy.filter((_, i) => i !== index) };
}

export function moveRule(doc: Doc, index: number, delta: number): Doc {
  const j = index + delta;
  if (j < 0 || j >= doc.policy.length) return doc;
  const policy = [...doc.policy];
  [policy[index], policy[j]] = [policy[j]!, policy[index]!];
  return { ...doc, policy };
}

/** Adds a service to the rule between source and destination (same
 * action), or a new rule when there is none. */
export function connect(doc: Doc, from: string, to: string, action: Rule["action"], services: string[], name?: string): Doc {
  const i = doc.policy.findIndex((r) => r.action === action && r.from.length === 1 && r.to.length === 1 && r.from[0] === from && r.to[0] === to);
  if (i >= 0 && !name) {
    const r = doc.policy[i]!;
    return updateRule(doc, i, { ...r, services: [...r.services, ...services.filter((s) => !r.services.includes(s))] });
  }
  const base = name || `${refLabel(from)}-to-${refLabel(to)}`.toLowerCase().replace(/[^a-z0-9._-]+/g, "-");
  return addRule(doc, { name: uniqueRuleName(doc, base), description: "", enabled: true, action, from: [from], to: [to], services });
}

/** Rules that refer to an object. */
export function rulesUsing(doc: Doc, ref: string): number[] {
  return doc.policy.flatMap((r, i) => (r.from.includes(ref) || r.to.includes(ref) ? [i] : []));
}

export function rulesUsingService(doc: Doc, name: string): number[] {
  return doc.policy.flatMap((r, i) => (r.services.includes(name) ? [i] : []));
}

export function upsertNetwork(doc: Doc, n: Network, oldName?: string): Doc {
  const others = doc.networks.filter((x) => x.name !== (oldName ?? n.name));
  let next: Doc = { ...doc, networks: sortByName([...others, n]) };
  if (oldName && oldName !== n.name) next = renameRef(next, `network:${oldName}`, `network:${n.name}`);
  return next;
}

/** Deleting a referenced object is refused by the caller (it lists the rules). */
export function deleteNetwork(doc: Doc, name: string): Doc {
  return { ...doc, networks: doc.networks.filter((n) => n.name !== name) };
}

export function upsertGroup(doc: Doc, g: Group, oldName?: string): Doc {
  const others = doc.groups.filter((x) => x.name !== (oldName ?? g.name));
  let next: Doc = { ...doc, groups: sortByName([...others, { ...g, members: [...g.members].sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase())) }]) };
  if (oldName && oldName !== g.name) next = renameRef(next, `group:${oldName}`, `group:${g.name}`);
  return next;
}

export function upsertService(doc: Doc, s: Service, oldName?: string): Doc {
  const others = doc.services.filter((x) => x.name !== (oldName ?? s.name));
  let next: Doc = { ...doc, services: sortByName([...others, s]) };
  if (oldName && oldName !== s.name) {
    next = { ...next, policy: next.policy.map((r) => ({ ...r, services: r.services.map((x) => (x === oldName ? s.name : x)) })) };
  }
  return next;
}

export function deleteService(doc: Doc, name: string): Doc {
  return { ...doc, services: doc.services.filter((s) => s.name !== name) };
}

export function renameRef(doc: Doc, from: string, to: string): Doc {
  const sub = (xs: string[]) => xs.map((x) => (x === from ? to : x));
  return { ...doc, policy: doc.policy.map((r) => ({ ...r, from: sub(r.from), to: sub(r.to) })) };
}

// --- topology ----------------------------------------------------------------

export type NodeKind = RefKind | "controller" | "asn" | "ip" | "more" | "special";
export type GNode = {
  key: string; // a reference ("host:web1", "internet", ...), "controller", or an observed node ("asn:15169", "ip:…")
  kind: NodeKind;
  label: string;
  detail: string; // CIDRs, member count, ...
  members?: string[]; // groups: host names
  parentGroup?: string; // a host shown as a member of an expanded group
  observed?: ObsNode; // a node only observation knows (external networks, addresses)
};
export type EdgeRule = { index: number; name: string; services: string[]; enabled: boolean };
export type GEdge = {
  id: string;
  kind: "rule" | "member" | "mgmt" | "observed";
  source: string;
  target: string;
  action?: Rule["action"];
  rules: EdgeRule[];
  label: string;
  status?: "protected" | "blocked" | "unverified";
  count?: number; // management paths aggregated into this edge
  detail?: string;
  obs?: ObsEdge; // observed traffic (aggregated onto the visible nodes)
};

/** Observed connections, as the API returns them (GET /network/topology). */
export type ObsNode = { key: string; kind: string; label: string; detail: string; members: { ip: string; country?: string; flows: number }[]; member_total: number; parent?: string };
export type ObsService = { protocol: string; port: number; service: string; count: number };
export type ObsEdge = {
  id: string; source: string; target: string; services: ObsService[]; count: number; flows: number;
  status: Record<string, number>; worst: string; would_deny: number; processes: string[]; first_seen: string; last_seen: string; flow_ids: number[];
};
export type Observed = { nodes: ObsNode[]; edges: ObsEdge[]; truncated: boolean; collapsed: number; considered: number };
export type TopologyMode = "configured" | "observed" | "both";

const statusRank: Record<string, number> = { management: 0, covered: 1, unclassified: 2, denied: 3 };
export const obsIcon: Record<string, string> = { covered: "✓", management: "⛨", unclassified: "⚠", denied: "✕" };
export const obsStatusLabel: Record<string, string> = {
  covered: "✓ Observed · covered", management: "⛨ Observed · DROOL management", unclassified: "⚠ Observed · unclassified", denied: "✕ Observed · denied or would be",
};

/** "HTTPS 443, TCP 8812 +2" */
export function obsServicesLabel(svcs: ObsService[]): string {
  const parts = svcs.slice(0, 3).map((s) => `${s.service ? s.service + " " : ""}${s.protocol.toUpperCase()} ${s.port}`);
  return parts.join(", ") + (svcs.length > 3 ? ` +${svcs.length - 3}` : "");
}

export type MgmtHop = { from: string; from_name: string; to: string; to_name: string; protocol: string; port: number; status: string; detail: string; blocked_by: string[]; at_risk?: boolean };
export type MgmtPath = { host_id: string; host: string; kind: string; mode: string; hops: MgmtHop[]; blocked: boolean; unverified: boolean; at_risk?: boolean };

export type TopologyOptions = {
  mode?: TopologyMode; // configured (default), observed, or both
  expanded: Set<string>; // group refs shown with their members
  pinned: Set<string>; // nodes kept on the canvas without rules
  showAllHosts: boolean;
  showManagement: boolean;
  filter: string; // case-insensitive substring; matching nodes and their neighbours stay
  hosts: { id: string; name: string }[];
};

/** Turns the document into nodes and grouped edges. One edge per source,
 * destination and action, listing every rule and service on it, so a group
 * rule is one line however many members the group has. */
export function toGraph(doc: Doc, all: ServiceInfo[], opt: TopologyOptions, mgmt: MgmtPath[] = [], observed?: Observed): { nodes: GNode[]; edges: GEdge[] } {
  const mode = opt.mode ?? "configured";
  const nodes = new Map<string, GNode>();
  const groupOf = new Map<string, string[]>(); // host name → group refs it belongs to
  for (const g of doc.groups) for (const m of g.members) groupOf.set(m.toLowerCase(), [...(groupOf.get(m.toLowerCase()) ?? []), `group:${g.name}`]);

  const addNode = (ref: string) => {
    if (nodes.has(ref)) return;
    const r = parseRef(ref);
    let detail = "";
    let members: string[] | undefined;
    if (r.kind === "network") {
      detail = doc.networks.find((n) => n.name === r.name)?.cidrs.join(", ") ?? "not defined";
    } else if (r.kind === "group") {
      members = doc.groups.find((g) => g.name === r.name)?.members ?? [];
      detail = `${members.length} host${members.length === 1 ? "" : "s"}`;
    } else if (r.kind === "internet") {
      detail = "everything outside the internal ranges";
    } else if (r.kind === "internal") {
      detail = "the internal ranges";
    } else if (r.kind === "any") {
      detail = "any address";
    } else if (r.kind === "host") {
      detail = (groupOf.get(r.name.toLowerCase()) ?? []).map((g) => parseRef(g).name).join(", ");
    }
    nodes.set(ref, { key: ref, kind: r.kind, label: refLabel(ref), detail, members });
  };

  const ruleEdges = new Map<string, GEdge>();
  // Observed only: the configured rules stay off the canvas.
  if (mode !== "observed") doc.policy.forEach((r, index) => {
    for (const f of r.from) {
      for (const t of r.to) {
        addNode(f);
        addNode(t);
        const id = `${r.action}|${f}|${t}`;
        const e = ruleEdges.get(id) ?? { id, kind: "rule" as const, source: f, target: t, action: r.action, rules: [], label: "" };
        e.rules.push({ index, name: r.name, services: r.services, enabled: r.enabled });
        ruleEdges.set(id, e);
      }
    }
  });
  for (const e of ruleEdges.values()) {
    const svcs = [...new Set(e.rules.filter((r) => r.enabled).flatMap((r) => r.services))];
    e.label = (e.action === "deny" ? "Deny " : "") + (svcs.length ? svcs.map((s) => serviceLabel(s, all)).join(", ") : "(disabled)");
  }
  for (const p of opt.pinned) if (p !== "controller") addNode(p);
  if (opt.showAllHosts) for (const h of opt.hosts) addNode(`host:${h.name}`);
  for (const g of doc.groups) if (opt.expanded.has(`group:${g.name}`)) addNode(`group:${g.name}`);

  // Expanded groups show their members, linked by membership edges.
  const edges: GEdge[] = [...ruleEdges.values()];
  for (const n of [...nodes.values()]) {
    if (n.kind !== "group" || !opt.expanded.has(n.key)) continue;
    for (const m of n.members ?? []) {
      const ref = `host:${m}`;
      if (!nodes.has(ref)) {
        addNode(ref);
        nodes.get(ref)!.parentGroup = n.key;
      }
      edges.push({ id: `member|${n.key}|${ref}`, kind: "member", source: n.key, target: ref, rules: [], label: "member" });
    }
  }

  // Observed traffic: each end attaches to the node that shows it (a host,
  // the collapsed group it belongs to, a named network, an external
  // network owner); edges landing on the same pair merge.
  if (mode !== "configured" && observed) {
    const obsNodes = new Map(observed.nodes.map((n) => [n.key, n]));
    const place = (key: string): string => {
      if (key.startsWith("host:")) {
        const name = key.slice(5);
        const own = nodes.get(key);
        if (own) return key;
        // Large fleets: a host in a (collapsed) group is drawn as the group.
        const group = (groupOf.get(name.toLowerCase()) ?? []).find((g) => nodes.has(g) && !opt.expanded.has(g))
          ?? (groupOf.get(name.toLowerCase()) ?? []).find((g) => !opt.expanded.has(g));
        if (group) {
          addNode(group);
          return group;
        }
        addNode(key);
        return key;
      }
      if (key === "controller") {
        if (!nodes.has("controller")) nodes.set("controller", { key: "controller", kind: "controller", label: "DROOL Controller", detail: "management (read-only)" });
        return key;
      }
      const on = obsNodes.get(key);
      if (on && (on.kind === "asn" || on.kind === "ip" || on.kind === "more" || on.kind === "special")) {
        if (!nodes.has(key)) nodes.set(key, { key, kind: on.kind as NodeKind, label: on.label, detail: on.detail, observed: on });
        return key;
      }
      addNode(key); // network:NAME, internal, internet
      const n = nodes.get(key)!;
      if (on) n.observed = on;
      return key;
    };
    const merged = new Map<string, GEdge>();
    for (const oe of observed.edges) {
      const s0 = place(oe.source);
      const t0 = place(oe.target);
      if (s0 === t0) continue;
      const id = `obs|${s0}|${t0}`;
      const prev = merged.get(id);
      if (!prev) {
        merged.set(id, { id, kind: "observed", source: s0, target: t0, rules: [], label: "", obs: { ...oe, id, source: s0, target: t0, services: [...oe.services], status: { ...oe.status }, processes: [...oe.processes], flow_ids: [...oe.flow_ids] } });
        continue;
      }
      const o = prev.obs!;
      o.count += oe.count;
      o.flows += oe.flows;
      o.would_deny += oe.would_deny;
      for (const [k, v] of Object.entries(oe.status)) o.status[k] = (o.status[k] ?? 0) + v;
      if ((statusRank[oe.worst] ?? 0) > (statusRank[o.worst] ?? 0)) o.worst = oe.worst;
      for (const sv of oe.services) {
        const m = o.services.find((x) => x.protocol === sv.protocol && x.port === sv.port);
        if (m) m.count += sv.count; else o.services.push({ ...sv });
      }
      o.services.sort((a, b) => b.count - a.count);
      o.processes = [...new Set([...o.processes, ...oe.processes])].slice(0, 5);
      o.flow_ids = [...o.flow_ids, ...oe.flow_ids].slice(0, 20);
      if (oe.first_seen < o.first_seen) o.first_seen = oe.first_seen;
      if (oe.last_seen > o.last_seen) o.last_seen = oe.last_seen;
    }
    for (const e of merged.values()) {
      const o = e.obs!;
      e.status = undefined;
      // Short on the canvas (symbol, main service, count); the full wording
      // is the line's tooltip, accessible name and details.
      const first = o.services[0];
      const svc = first ? `${first.service || first.protocol.toUpperCase()} ${first.port}${o.services.length > 1 ? ` +${o.services.length - 1}` : ""}` : "";
      e.label = `${obsIcon[o.worst] ?? "•"} ${svc} · ${o.count.toLocaleString()}`;
      e.detail = `${obsStatusLabel[o.worst] ?? "Observed"}: ${obsServicesLabel(o.services)} · ${o.count.toLocaleString()} connections`;
      edges.push(e);
    }
  }

  // Management overlay: each hop attaches to the node that shows the host
  // (the host itself, else a collapsed group containing it); hops that land
  // on the same pair are drawn once with a count.
  if (opt.showManagement && mgmt.length > 0) {
    const nameOf = new Map(opt.hosts.map((h) => [h.id, h.name]));
    const visible = (id: string): string | undefined => {
      if (id === "controller") return "controller";
      const name = nameOf.get(id);
      if (!name) return undefined;
      const own = nodes.get(`host:${name}`);
      // A host shown only as a member of an expanded group: its management
      // path stays aggregated on the group (no line per member).
      if (own?.parentGroup) return own.parentGroup;
      if (own) return `host:${name}`;
      return (groupOf.get(name.toLowerCase()) ?? []).find((g) => nodes.has(g));
    };
    const agg = new Map<string, GEdge>();
    for (const p of mgmt) {
      for (const h of p.hops) {
        const s = visible(h.from);
        const t = visible(h.to);
        if (!s || !t || s === t) continue;
        const id = `mgmt|${s}|${t}`;
        const status = h.status as GEdge["status"];
        const e = agg.get(id) ?? { id, kind: "mgmt" as const, source: s, target: t, rules: [], label: "", status: "protected" as const, count: 0, detail: "" };
        e.count = (e.count ?? 0) + 1;
        if (status === "blocked" || (status === "unverified" && e.status === "protected")) {
          e.status = status;
          e.detail = h.detail;
        }
        e.label = `${p.kind === "agent" ? "mTLS" : "SSH"} ${h.protocol.toUpperCase()} ${h.port || ""}`.trim();
        agg.set(id, e);
      }
    }
    if (agg.size > 0) nodes.set("controller", { key: "controller", kind: "controller", label: "DROOL Controller", detail: "management (read-only)" });
    for (const e of agg.values()) {
      // Line 1 says what it is; line 2 what it carries (the canvas splits on
      // "\n"). Hops after the first are drawn compactly; the legend, the
      // tooltip and Details carry the full wording.
      const what = `${e.label}${(e.count ?? 0) > 1 ? ` · ${e.count} hosts` : ""}`;
      e.detail = e.detail || `Protected DROOL management path · ${what}`;
      e.label = e.status === "blocked" ? `BLOCKED · Protected DROOL management path\n${what}`
        : e.source === "controller" ? `Protected DROOL management path\n${what}` : `DROOL management · ${what}`;
      edges.push(e);
    }
  }

  // Filtering keeps matching nodes and their direct neighbours.
  const f = opt.filter.trim().toLowerCase();
  if (f) {
    const match = new Set([...nodes.values()].filter((n) => n.label.toLowerCase().includes(f) || n.detail.toLowerCase().includes(f)).map((n) => n.key));
    const keep = new Set(match);
    for (const e of edges) {
      if (match.has(e.source)) keep.add(e.target);
      if (match.has(e.target)) keep.add(e.source);
    }
    return {
      nodes: [...nodes.values()].filter((n) => keep.has(n.key)),
      edges: edges.filter((e) => keep.has(e.source) && keep.has(e.target)),
    };
  }
  return { nodes: [...nodes.values()], edges };
}

/** Hosts a reference covers (for "affected hosts"). */
export function hostsOf(doc: Doc, ref: string, hosts: { name: string }[]): string[] {
  const r = parseRef(ref);
  if (r.kind === "host") return [r.name];
  if (r.kind === "group") return doc.groups.find((g) => g.name === r.name)?.members ?? [];
  if (r.kind === "any") return hosts.map((h) => h.name);
  return [];
}

/** Hosts a rule changes: destinations (inbound) and, for denies, sources. */
export function affectedHosts(doc: Doc, rule: Rule, hosts: { name: string }[]): string[] {
  const set = new Set(rule.to.flatMap((t) => hostsOf(doc, t, hosts)));
  if (rule.action === "deny") rule.from.flatMap((f) => hostsOf(doc, f, hosts)).forEach((h) => set.add(h));
  return [...set].sort();
}
