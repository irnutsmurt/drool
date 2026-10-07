import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  addRule, affectedHosts, allServices, connect, deleteRule, emptyDoc, ensureCustomService, moveRule, rulesUsing, rulesUsingService,
  toGraph, upsertGroup, upsertNetwork, upsertService, validPorts, type Doc, type ServiceInfo, type TopologyOptions,
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
