import { describe, expect, it } from "vitest";
import { hostPath, hostSections, sectionOf, visibleHostSections } from "./sections";
import { isActive, navSections } from "@/components/layout/nav";

const hosts = navSections.flatMap((s) => s.items).find((i) => i.label === "Hosts")!;
const all = () => true;
const viewer = (p: string) => ["hosts.read", "jobs.read", "agents.read", "firewall.read", "monitoring.read", "alerts.read", "schedules.read",
  "workflows.read", "desired.read"].includes(p);

describe("host workspace", () => {
  it("has the eight sections, in order", () => {
    expect(hostSections.map((s) => s.label)).toEqual(["Overview", "Monitoring", "Files", "Network", "Firewall", "Desired State", "Management", "Activity"]);
  });

  it("finds the section of every host address, old ones included", () => {
    const cases: [string, string][] = [
      ["", "overview"], ["/", "overview"],
      ["/monitoring", "monitoring"], ["/files", "files"], ["/files/edit", "files"], ["/network", "network"],
      ["/firewall", "firewall"], ["/desired", "desired"], ["/management", "management"], ["/activity", "activity"],
      ["/nonsense", "overview"],
    ];
    for (const [rest, key] of cases) expect(sectionOf(rest).key, rest).toBe(key);
  });

  it("keeps Hosts highlighted in the sidebar on every section", () => {
    for (const s of hostSections) expect(isActive(hosts, hostPath("h1", s.path)), s.key).toBe(true);
    expect(isActive(hosts, "/hosts/h1/files/edit")).toBe(true);
  });

  it("shows a role only the sections it may open", () => {
    expect(visibleHostSections({ jump_only: false }, all)).toHaveLength(8);
    // A viewer has no files.read or network.read.
    expect(visibleHostSections({ jump_only: false }, viewer).map((s) => s.key))
      .toEqual(["overview", "monitoring", "firewall", "desired", "management", "activity"]);
  });

  it("offers a jump host only what applies to it", () => {
    expect(visibleHostSections({ jump_only: true }, all).map((s) => s.key)).toEqual(["overview", "firewall", "management", "activity"]);
  });

  it("builds section addresses", () => {
    expect(hostPath("h1")).toBe("/hosts/h1");
    expect(hostPath("h1", "monitoring", "tab=services")).toBe("/hosts/h1/monitoring?tab=services");
  });
});
