import { describe, expect, it } from "vitest";
import { searchDestinations } from "./navsearch";
import { destinations } from "@/components/layout/destinations";
import { isActive, navSections, visibleSections } from "@/components/layout/nav";

const admin = () => true;
const top = (q: string, can: (p: string) => boolean = admin) => searchDestinations(q, destinations, can)[0]?.dest;

describe("navigation search", () => {
  // The M15 examples: conventional words find the themed destinations.
  const cases: [string, string][] = [
    ["dashboard", "/"], ["overview", "/"],
    ["host", "/hosts"], ["server", "/hosts"],
    ["group", "/groups"], ["pack", "/groups"],
    ["agent", "/agents"], ["shepherd", "/agents"],
    ["monitor", "/monitoring"], ["metrics", "/monitoring"],
    ["threshold", "/monitoring/settings"], ["health setting", "/monitoring/settings"],
    ["alert", "/alerts"],
    ["maintenance", "/alerts?view=maintenance"], ["downtime", "/alerts?view=maintenance"],
    ["network", "/network"], ["traffic", "/network"], ["connections", "/network"],
    ["job", "/jobs"], ["run", "/jobs"],
    ["schedule", "/jobs/schedules"], ["recurring", "/jobs/schedules"], ["cron", "/jobs/schedules"],
    ["workflow", "/jobs/workflows"],
    ["drift", "/desired"], ["baseline", "/desired"], ["desired", "/desired"],
    ["firewall", "/firewall"],
    ["can A reach B", "/firewall?tab=simulate"], ["reachability", "/firewall?tab=simulate"],
    ["ssh key", "/credentials"], ["credential", "/credentials"],
    ["api token", "/settings/tokens"], ["api key", "/settings/tokens"],
    ["notification", "/settings/notifications"], ["email", "/settings/notifications"], ["discord", "/settings/notifications"],
    ["slack", "/settings/notifications"], ["telegram", "/settings/notifications"], ["webhook", "/settings/notifications"],
    ["audit", "/audit"], ["who changed", "/audit"],
    ["hardening", "/settings/system#secure"], ["security checklist", "/settings/system#secure"], ["master key", "/settings/system#secure"],
    ["secure", "/settings/system#secure"],
  ];
  for (const [q, to] of cases) {
    it(`"${q}" opens ${to}`, () => expect(top(q)?.to).toBe(to));
  }

  it("finds a section's destinations by its heading", () => {
    const r = searchDestinations("settings", destinations, admin).map((x) => x.dest.to);
    // "settings" opens the directory of every setting first.
    expect(r[0]).toBe("/settings");
    for (const to of ["/settings/tokens", "/settings/users", "/settings/notifications", "/settings/system", "/monitoring/settings"]) expect(r).toContain(to);
    expect(searchDestinations("protect", destinations, admin).map((x) => x.dest.to)).toEqual(expect.arrayContaining(["/firewall", "/credentials", "/audit"]));
  });

  it("ignores case, punctuation and extra spaces", () => {
    expect(top("  Can a reach B?  ")?.to).toBe("/firewall?tab=simulate");
    expect(top("API-TOKEN")?.to).toBe("/settings/tokens");
  });

  it("says which word matched when it wasn't the title", () => {
    const r = searchDestinations("group", destinations, admin)[0]!;
    expect(r.dest.title).toBe("Packs");
    expect(r.via?.toLowerCase()).toBe("group");
  });

  it("lists every allowed destination, in order, for an empty query", () => {
    const all = searchDestinations("", destinations, admin);
    expect(all.map((r) => r.dest.id)).toEqual(destinations.map((d) => d.id));
  });

  it("never offers a page the user can't view", () => {
    // The viewer role (internal/auth/rbac.go).
    const viewer = new Set(["hosts.read", "jobs.read", "agents.read", "firewall.read", "monitoring.read", "alerts.read", "schedules.read", "workflows.read", "desired.read"]);
    const can = (p: string) => viewer.has(p);
    const ids = searchDestinations("", destinations, can).map((r) => r.dest.id);
    for (const hidden of ["credentials", "audit", "network", "settings-users", "settings-notifications", "hosts-new", "runs-new", "baselines-new"]) {
      expect(ids).not.toContain(hidden);
    }
    expect(top("ssh key", can)?.id).not.toBe("credentials");
    expect(top("api token", can)?.to).toBe("/settings/tokens");
    expect(top("who changed", can)).toBeUndefined();
    // A destination needing two permissions needs both.
    const half = (p: string) => p === "schedules.manage";
    expect(searchDestinations("new schedule", destinations, half).map((r) => r.dest.id)).not.toContain("schedules-new");
  });

  it("covers every sidebar destination", () => {
    const tos = new Set(destinations.map((d) => d.to));
    for (const s of navSections) for (const i of s.items) expect(tos.has(i.to) || i.to === "/settings").toBe(true);
  });
});

describe("sidebar", () => {
  const item = (label: string) => navSections.flatMap((s) => s.items).find((i) => i.label === label)!;

  it("has the M15 headings and destinations, in order", () => {
    expect(navSections.map((s) => [s.heading ?? "", s.items.map((i) => i.label)])).toEqual([
      ["", ["Overview"]],
      ["Fleet", ["Hosts", "Packs", "Shepherds"]],
      ["Observe", ["Monitoring", "Alerts", "Network Activity"]],
      ["Automate", ["Runs", "Schedules", "Workflows", "Desired State"]],
      ["Protect", ["Firewall", "Credentials", "Audit Log"]],
      ["", ["Settings"]],
    ]);
    expect(item("Packs").hint).toBe("Groups");
    expect(item("Shepherds").hint).toBe("DROOL agents");
  });

  it("highlights the right entry for nested pages", () => {
    expect(isActive(item("Runs"), "/jobs")).toBe(true);
    expect(isActive(item("Runs"), "/jobs/job_123")).toBe(true);
    expect(isActive(item("Runs"), "/jobs/new")).toBe(true);
    expect(isActive(item("Runs"), "/jobs/schedules")).toBe(false);
    expect(isActive(item("Schedules"), "/jobs/schedules/s1/edit")).toBe(true);
    expect(isActive(item("Workflows"), "/jobs/workflows/new")).toBe(true);
    expect(isActive(item("Overview"), "/hosts")).toBe(false);
    expect(isActive(item("Hosts"), "/hosts/h1/monitoring")).toBe(true);
    expect(isActive(item("Hosts"), "/file-deployments/d1")).toBe(true);
    expect(isActive(item("Hosts"), "/hostsx")).toBe(false);
    expect(isActive(item("Settings"), "/settings/tokens")).toBe(true);
  });

  it("hides what the user can't view, and empty headings with it", () => {
    const viewer = new Set(["hosts.read", "jobs.read", "monitoring.read", "alerts.read"]);
    const s = visibleSections((p) => viewer.has(p));
    expect(s.map((x) => x.heading ?? "")).toEqual(["", "Fleet", "Observe", "Automate", ""]);
    expect(s.flatMap((x) => x.items.map((i) => i.label))).toEqual(["Overview", "Hosts", "Packs", "Monitoring", "Alerts", "Runs", "Settings"]);
  });
});
