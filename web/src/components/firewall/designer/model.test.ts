import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  addressChoice, addRule, affectedHosts, allServices, cidrFromQuery, connect, deleteRule, emptyDoc, ensureCustomService, moveRule, portChoices, refChoices,
  rulesUsing, rulesUsingService, suggestRuleName, toGraph, upsertGroup, upsertNetwork, upsertService, validPorts,
  type Doc, type ObsEdge, type Observed, type ServiceInfo, type TopologyOptions,
} from "./model";

// The cross-view golden test (with internal/firewall/document TestGolden):
// the same canonical document built through Topology operations and
// through Rules operations; the Go side checks model ⇄ YAML.
const golden: Doc = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../../../../internal/firewall/document/testdata/designer.json", import.meta.url)), "utf8"),
);

const builtin: ServiceInfo[] = [
  { name: "ssh", description: "SSH", builtin: true, entries: [{ protocol: "tcp", ports: "22" }] },
  { name: "https", description: "HTTPS", builtin: true, entries: [{ protocol: "tcp", ports: "443" }] },
  { name: "dns", description: "DNS", builtin: true, entries: [{ protocol: "udp", ports: "53" }, { protocol: "tcp", ports: "53" }] },
];
const hosts = ["jump-a", "server-a", "server-b", "server-c"].map((name, i) => ({ id: `h${i}`, name }));
const opts = (o: Partial<TopologyOptions> = {}): TopologyOptions => ({
  expanded: new Set(), pinned: new Set(), showAllHosts: false, showManagement: false, filter: "", hosts, ...o,
});

/** What a user does on the canvas: drop objects, drag connections, pick
 * services in the edge editor (named, or Custom protocol/port). */
function viaTopology(): Doc {
  let d = emptyDoc();
  d = upsertNetwork(d, { name: "admin-lan", description: "", cidrs: ["192.168.1.0/24"] });
  d = upsertNetwork(d, { name: "v6-lan", description: "", cidrs: ["2001:db8:10::/48"] });
  d = upsertGroup(d, { name: "ssh-managed", description: "", members: ["server-c", "server-a", "server-b"] });
  d = connect(d, "network:admin-lan", "host:jump-a", "allow", ["ssh"], "admin-to-jump");
  d = connect(d, "host:jump-a", "group:ssh-managed", "allow", ["ssh"], "jump-to-servers");
  // Custom: TCP 8812, then a second service on the same edge.
  let r = ensureCustomService(d, builtin, "tcp", "8812");
  d = r.doc;
  d = connect(d, "host:server-a", "host:server-b", "allow", [r.name], "server-a-to-server-b");
  d = connect(d, "host:server-a", "host:server-b", "allow", ["https"]);
  d = connect(d, "internet", "host:server-b", "allow", ["https"], "web-from-internet");
  d = connect(d, "network:v6-lan", "host:jump-a", "allow", ["ssh"], "v6-admin");
  r = ensureCustomService(d, builtin, "tcp", "23");
  d = r.doc;
  d = connect(d, "any", "group:ssh-managed", "deny", [r.name], "no-telnet");
  // The generated name for 8812 is tcp-8812; the golden file calls it app-8812.
  return upsertService(d, { name: "app-8812", description: "", entries: [{ protocol: "tcp", ports: "8812" }] }, "tcp-8812");
}

/** What a user does in the Rules view: forms, in a different order. */
function viaRules(): Doc {
  let d = emptyDoc();
  d = upsertService(d, { name: "tcp-23", description: "", entries: [{ protocol: "tcp", ports: "23" }] });
  d = upsertService(d, { name: "app-8812", description: "", entries: [{ protocol: "tcp", ports: "8812" }] });
  d = upsertGroup(d, { name: "ssh-managed", description: "", members: ["server-a", "server-b", "server-c"] });
  d = upsertNetwork(d, { name: "v6-lan", description: "", cidrs: ["2001:db8:10::/48"] });
  d = upsertNetwork(d, { name: "admin-lan", description: "", cidrs: ["192.168.1.0/24"] });
  const rule = (name: string, action: "allow" | "deny", from: string[], to: string[], services: string[]) =>
    ({ name, description: "", enabled: true, action, from, to, services });
  d = addRule(d, rule("no-telnet", "deny", ["any"], ["group:ssh-managed"], ["tcp-23"]));
  d = addRule(d, rule("admin-to-jump", "allow", ["network:admin-lan"], ["host:jump-a"], ["ssh"]), 0);
  d = addRule(d, rule("jump-to-servers", "allow", ["host:jump-a"], ["group:ssh-managed"], ["ssh"]), 1);
  d = addRule(d, rule("server-a-to-server-b", "allow", ["host:server-a"], ["host:server-b"], ["app-8812", "https"]), 2);
  d = addRule(d, rule("v6-admin", "allow", ["network:v6-lan"], ["host:jump-a"], ["ssh"]), 3);
  d = addRule(d, rule("web-from-internet", "allow", ["internet"], ["host:server-b"], ["https"]), 3);
  return d;
}

describe("one canonical model behind Topology and Rules", () => {
  it("Topology → canonical model (= golden, which Go renders to the golden YAML)", () => {
    expect(viaTopology()).toEqual(golden);
  });
  it("Rules → canonical model (= golden)", () => {
    expect(viaRules()).toEqual(golden);
  });
  it("canonical model (from YAML, see Go TestGolden) → Topology", () => {
    const all = allServices(golden, builtin);
    const g = toGraph(golden, all, opts());
    const keys = g.nodes.map((n) => n.key).sort();
    expect(keys).toEqual(["any", "group:ssh-managed", "host:jump-a", "host:server-a", "host:server-b", "internet", "network:admin-lan", "network:v6-lan"]);
    const edge = (s: string, t: string) => g.edges.find((e) => e.source === s && e.target === t);
    // Multiple services between the same nodes: one edge, both listed with
    // what they mean on the wire.
    expect(edge("host:server-a", "host:server-b")?.label).toBe("app-8812 (TCP 8812), https (TCP 443)");
    // A group rule is one relationship, not a line per member.
    expect(g.edges.filter((e) => e.target === "group:ssh-managed" && e.kind === "rule")).toHaveLength(2);
    expect(edge("any", "group:ssh-managed")?.action).toBe("deny");
    expect(edge("any", "group:ssh-managed")?.label).toBe("Deny tcp-23 (TCP 23)");
    expect(g.nodes.find((n) => n.key === "network:v6-lan")?.detail).toBe("2001:db8:10::/48");
    expect(g.nodes.find((n) => n.key === "group:ssh-managed")?.detail).toBe("3 hosts");
  });
  it("Rules → Topology: the Rules-built model draws the same graph", () => {
    const all = allServices(golden, builtin);
    expect(toGraph(viaRules(), all, opts())).toEqual(toGraph(golden, all, opts()));
  });
});

describe("topology", () => {
  const all = allServices(golden, builtin);
  it("expands a group into its members without duplicating hosts", () => {
    const g = toGraph(golden, all, opts({ expanded: new Set(["group:ssh-managed"]) }));
    expect(g.nodes.filter((n) => n.key === "host:server-a")).toHaveLength(1);
    expect(g.nodes.find((n) => n.key === "host:server-c")?.parentGroup).toBe("group:ssh-managed");
    expect(g.edges.filter((e) => e.kind === "member").map((e) => e.target).sort()).toEqual(["host:server-a", "host:server-b", "host:server-c"]);
  });
  it("scales: 50 group members are one edge while collapsed", () => {
    const many = Array.from({ length: 50 }, (_, i) => ({ id: `m${i}`, name: `srv${i}` }));
    let d = upsertGroup(emptyDoc(), { name: "linux", description: "", members: many.map((h) => h.name) });
    d = connect(d, "host:jump-a", "group:linux", "allow", ["ssh"]);
    const g = toGraph(d, all, opts({ hosts: many }));
    expect(g.nodes).toHaveLength(2);
    expect(g.edges).toHaveLength(1);
  });
  it("filters to matching nodes and their neighbours", () => {
    const g = toGraph(golden, all, opts({ filter: "server-b" }));
    expect(g.nodes.map((n) => n.key).sort()).toEqual(["host:server-a", "host:server-b", "internet"]);
  });
  it("draws management paths as a separate, aggregated, read-only overlay", () => {
    const mgmt = [
      { host_id: "h1", host: "server-a", kind: "ssh", mode: "enforce", blocked: true, unverified: false, hops: [
        { from: "controller", from_name: "DROOL Controller", to: "h0", to_name: "jump-a", protocol: "tcp", port: 22, status: "protected", detail: "", blocked_by: [] },
        { from: "h0", from_name: "jump-a", to: "h1", to_name: "server-a", protocol: "tcp", port: 22, status: "blocked", detail: "policy x", blocked_by: ["x"] },
      ] },
      { host_id: "h2", host: "server-b", kind: "ssh", mode: "off", blocked: false, unverified: false, hops: [
        { from: "controller", from_name: "DROOL Controller", to: "h0", to_name: "jump-a", protocol: "tcp", port: 22, status: "protected", detail: "", blocked_by: [] },
        { from: "h0", from_name: "jump-a", to: "h2", to_name: "server-b", protocol: "tcp", port: 22, status: "protected", detail: "", blocked_by: [] },
      ] },
    ];
    const g = toGraph(golden, all, opts({ showManagement: true }), mgmt);
    const m = g.edges.filter((e) => e.kind === "mgmt");
    expect(m.map((e) => `${e.source}>${e.target}:${e.status}:${e.count}`).sort()).toEqual([
      "controller>host:jump-a:protected:2", "host:jump-a>host:server-a:blocked:1", "host:jump-a>host:server-b:protected:1",
    ]);
    expect(m.find((e) => e.status === "blocked")?.label).toBe("BLOCKED · Protected DROOL management path\nSSH TCP 22");
    expect(m.find((e) => e.source === "controller")?.label).toBe("Protected DROOL management path\nSSH TCP 22 · 2 hosts");
    expect(m.find((e) => e.target === "host:server-b")?.label).toBe("DROOL management · SSH TCP 22");
    expect(g.nodes.find((n) => n.key === "controller")?.kind).toBe("controller");
    // Collapsed into a group: hops to its members land on the group.
    const collapsed = connect(upsertGroup(emptyDoc(), { name: "ssh-managed", description: "", members: ["server-a", "server-b"] }),
      "host:jump-a", "group:ssh-managed", "allow", ["ssh"]);
    const g2 = toGraph(collapsed, all, opts({ showManagement: true }), mgmt);
    expect(g2.edges.filter((e) => e.kind === "mgmt").map((e) => `${e.source}>${e.target}:${e.status}:${e.count}`).sort()).toEqual([
      "controller>host:jump-a:protected:2", "host:jump-a>group:ssh-managed:blocked:2",
    ]);
    // Expanding the group shows its members, but the overlay stays on the group.
    const g3 = toGraph(collapsed, all, opts({ showManagement: true, expanded: new Set(["group:ssh-managed"]) }), mgmt);
    expect(g3.nodes.some((n) => n.key === "host:server-a")).toBe(true);
    expect(g3.edges.filter((e) => e.kind === "mgmt").map((e) => `${e.source}>${e.target}`).sort()).toEqual([
      "controller>host:jump-a", "host:jump-a>group:ssh-managed",
    ]);
  });
});

describe("edits", () => {
  it("validates ports like the server", () => {
    for (const ok of ["22", "8000-8010", "80,443", "1", "65535"]) expect(validPorts(ok)).toBe(true);
    for (const bad of ["", "0", "65536", "10-5", "a", "1-2-3", "22a", "-5"]) expect(validPorts(bad)).toBe(false);
  });
  it("reuses an existing service with the same definition", () => {
    const r = ensureCustomService(golden, builtin, "tcp", "443");
    expect(r.name).toBe("https");
    expect(r.doc).toBe(golden);
  });
  it("finds references before deleting", () => {
    expect(rulesUsing(golden, "network:admin-lan")).toEqual([0]);
    expect(rulesUsingService(golden, "app-8812")).toEqual([2]);
  });
  it("renames update references", () => {
    const d = upsertNetwork(golden, { name: "admins", description: "", cidrs: ["192.168.1.0/24"] }, "admin-lan");
    expect(d.policy[0]!.from).toEqual(["network:admins"]);
    const d2 = upsertService(golden, { name: "app", description: "", entries: [{ protocol: "tcp", ports: "8812" }] }, "app-8812");
    expect(d2.policy[2]!.services).toEqual(["app", "https"]);
  });
  it("reorders and deletes rules", () => {
    expect(moveRule(golden, 1, -1).policy.map((r) => r.name).slice(0, 2)).toEqual(["jump-to-servers", "admin-to-jump"]);
    expect(moveRule(golden, 0, -1)).toBe(golden);
    expect(deleteRule(golden, 0).policy).toHaveLength(5);
  });
  it("lists affected hosts", () => {
    const hs = hosts;
    expect(affectedHosts(golden, golden.policy[1]!, hs)).toEqual(["server-a", "server-b", "server-c"]);
    expect(affectedHosts(golden, golden.policy[5]!, hs)).toEqual(["jump-a", "server-a", "server-b", "server-c"]);
  });
});

// --- M10: observed traffic on the same canvas ------------------------------

const obsEdge = (source: string, target: string, worst: string, count: number, port = 443): ObsEdge => ({
  id: `obs|${source}|${target}`, source, target, services: [{ protocol: "tcp", port, service: port === 443 ? "https" : "", count }],
  count, flows: 1, status: { [worst]: 1 }, worst, would_deny: worst === "denied" ? 1 : 0, processes: ["curl"],
  first_seen: "2026-10-01T00:00:00Z", last_seen: "2026-10-07T00:00:00Z", flow_ids: [1],
});
const observed: Observed = {
  nodes: [
    { key: "asn:15169", kind: "asn", label: "Google LLC", detail: "AS15169 · US", members: [{ ip: "142.250.1.1", flows: 1 }], member_total: 3 },
    { key: "asn:other", kind: "asn", label: "Other external networks", detail: "", members: [], member_total: 40 },
  ],
  edges: [
    obsEdge("host:server-a", "asn:15169", "covered", 10),
    obsEdge("host:server-b", "asn:15169", "unclassified", 5, 8812),
    obsEdge("host:server-a", "host:server-b", "covered", 7),
    obsEdge("host:server-c", "asn:other", "denied", 1, 25),
    obsEdge("controller", "host:jump-a", "management", 9, 22),
  ],
  truncated: false, collapsed: 12, considered: 5,
};

describe("observed topology", () => {
  it("configured mode ignores observations", () => {
    const g = toGraph(golden, allServices(golden, builtin), opts({ mode: "configured" }), [], observed);
    expect(g.edges.some((e) => e.kind === "observed")).toBe(false);
    expect(g.nodes.some((n) => n.key.startsWith("asn:"))).toBe(false);
  });

  it("observed mode draws only what was seen, grouped by network owner and host group", () => {
    const g = toGraph(golden, allServices(golden, builtin), opts({ mode: "observed" }), [], observed);
    expect(g.edges.every((e) => e.kind === "observed")).toBe(true);
    const ids = g.edges.map((e) => e.id).sort();
    // server-a, server-b and server-c are members of ssh-managed: one group
    // node; the two edges to Google merge, the edge inside the group is
    // drawn on the group itself (skipped).
    expect(ids).toEqual(["obs|controller|host:jump-a", "obs|group:ssh-managed|asn:15169", "obs|group:ssh-managed|asn:other"]);
    const google = g.edges.find((e) => e.target === "asn:15169")!;
    expect(google.obs!.count).toBe(15);
    expect(google.obs!.worst).toBe("unclassified"); // the most notable status wins
    expect(google.label).toMatch(/^⚠ https 443 \+1 · 15$/); // short on the canvas
    expect(google.detail).toContain("⚠ Observed · unclassified"); // full wording: tooltip, name, details
    expect(google.obs!.services.map((s) => s.port).sort()).toEqual([443, 8812]);
    const asn = g.nodes.find((n) => n.key === "asn:15169")!;
    expect(asn.kind).toBe("asn");
    expect(asn.observed!.member_total).toBe(3);
    expect(g.edges.find((e) => e.target === "asn:other")!.detail).toContain("✕ Observed · denied");
  });

  it("expanded groups show members; both mode overlays reality on the policy", () => {
    const g = toGraph(golden, allServices(golden, builtin), opts({ mode: "both", expanded: new Set(["group:ssh-managed"]) }), [], observed);
    expect(g.edges.some((e) => e.kind === "rule")).toBe(true);
    const obsIds = g.edges.filter((e) => e.kind === "observed").map((e) => e.id).sort();
    expect(obsIds).toContain("obs|host:server-a|host:server-b");
    expect(obsIds).toContain("obs|host:server-a|asn:15169");
    expect(obsIds).toContain("obs|host:server-b|asn:15169");
  });

  it("stays usable without data", () => {
    const g = toGraph(emptyDoc(), builtin, opts({ mode: "observed" }), [], { nodes: [], edges: [], truncated: false, collapsed: 0, considered: 0 });
    expect(g.nodes).toEqual([]);
    expect(g.edges).toEqual([]);
    expect(toGraph(emptyDoc(), builtin, opts({ mode: "both" }), [], undefined).edges).toEqual([]);
  });
});

// The rule builder (one search per field instead of kind, value and Add).
describe("rule builder choices", () => {
  const doc = upsertNetwork(upsertGroup(emptyDoc(), { name: "web", description: "", members: ["server-a", "server-b"] }),
    { name: "home-lan", description: "", cidrs: ["192.168.1.0/24"] });

  it("offers every kind of source at once, Internal networks first", () => {
    const c = refChoices(doc, hosts, "source");
    expect(c[0]).toMatchObject({ value: "internal", label: "Internal networks" });
    expect(c.map((x) => x.value)).toEqual(expect.arrayContaining(["any", "internet", "host:server-a", "group:web", "network:home-lan"]));
    expect(c.find((x) => x.value === "host:server-a")?.detail).toBe("web");
  });

  it("puts hosts and Packs first for a destination, and marks addresses outbound only", () => {
    const c = refChoices(doc, hosts, "destination");
    expect(c[0]!.group).toBe("Hosts");
    const addr = c.filter((x) => x.group === "Addresses (outbound only)").map((x) => x.value);
    expect(addr).toEqual(["internal", "internet", "network:home-lan"]);
    expect(c.find((x) => x.value === "any")?.detail).toBe("every host DROOL manages");
  });

  it("reads an address or range typed in", () => {
    expect(cidrFromQuery("10.0.0.5")).toBe("cidr:10.0.0.5/32");
    expect(cidrFromQuery(" 192.168.1.0/24 ")).toBe("cidr:192.168.1.0/24");
    expect(cidrFromQuery("2001:DB8::/32")).toBe("cidr:2001:db8::/32");
    expect(cidrFromQuery("fe80::1")).toBe("cidr:fe80::1/128");
    for (const bad of ["10.0.0.256", "10.0.0.0/33", "web", "22", "1.2.3", "2001:db8::/129"]) expect(cidrFromQuery(bad)).toBeNull();
    expect(addressChoice("10.1.2.0/24", "destination")).toEqual([
      { value: "cidr:10.1.2.0/24", label: "10.1.2.0/24", group: "Addresses (outbound only)", detail: "an address or range" }]);
    expect(addressChoice("web", "source")).toEqual([]);
  });

  it("reads a port typed in, with or without a protocol", () => {
    expect(portChoices("8812")).toEqual([{ protocol: "tcp", ports: "8812" }, { protocol: "udp", ports: "8812" }]);
    expect(portChoices("udp 53")).toEqual([{ protocol: "udp", ports: "53" }]);
    expect(portChoices("tcp/8000-8010")).toEqual([{ protocol: "tcp", ports: "8000-8010" }]);
    expect(portChoices("8812/udp")).toEqual([{ protocol: "udp", ports: "8812" }]);
    expect(portChoices("80, 443")).toEqual([{ protocol: "tcp", ports: "80,443" }, { protocol: "udp", ports: "80,443" }]);
    for (const bad of ["ssh", "0", "70000", "tcp", "10-5", ""]) expect(portChoices(bad)).toEqual([]);
  });

  it("names a rule after what it does, uniquely and within the server's rules", () => {
    const r = { action: "allow" as const, from: ["internal"], to: ["host:PiZeroWopenwx"], services: ["ssh"] };
    expect(suggestRuleName(doc, r)).toBe("allow-ssh-internal-to-pizerowopenwx");
    const taken = addRule(doc, { ...r, name: "allow-ssh-internal-to-pizerowopenwx", description: "", enabled: true });
    expect(suggestRuleName(taken, r)).toBe("allow-ssh-internal-to-pizerowopenwx-2");
    // Editing rule 0 keeps its own name available.
    expect(suggestRuleName(taken, r, 0)).toBe("allow-ssh-internal-to-pizerowopenwx");
    expect(suggestRuleName(doc, { action: "deny", from: ["cidr:2001:db8::/32"], to: ["any"], services: ["tcp-8812"] }))
      .toBe("deny-tcp-8812-2001-db8-32-to-anywhere");
    const long = suggestRuleName(doc, { action: "allow", from: ["host:" + "a".repeat(80)], to: ["group:web"], services: ["https"] });
    expect(long.length).toBeLessThanOrEqual(63);
    expect(long).toMatch(/^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/);
    expect(suggestRuleName(doc, { action: "allow", from: [], to: [], services: [] })).toBe("allow-to");
  });
});
