import { describe, expect, it } from "vitest";
import type { HealthCheck } from "./queries";
import { packConflicts } from "./HostHealth";
import { ruleText } from "@/components/alerts/AlertRule";
import { alertMonitoringLink } from "@/components/alerts/ui";
import { issueLink } from "@/components/monitoring/links";
import type { Alert } from "@/components/alerts/queries";

// An effective field: its value and source (here: inherited), and what it inherits.
const field = (name: string, label: string, inherited: { value: number; source: object; conflict?: object }, own: number | null = null) =>
  ({ name, label, type: "percent", unit: "%", min: 0, max: 100, value: inherited.value, source: inherited.source, own, inherited }) as never;
const check = (fields: unknown[], extra: object = {}): HealthCheck =>
  ({ check: "filesystem", label: "Disk space", category: "disk", description: "", enabled: true, customized: false, conflicts: 0, fields, ...extra }) as never;

describe("Pack conflicts on a host", () => {
  it("lists each field the server reports a conflict for, with the Packs and their values", () => {
    const c = check([
      field("warning", "Warning at", { value: 80, source: { kind: "global", label: "Global" },
        conflict: { priority: 0, groups: [{ id: "g1", name: "media-servers", priority: 0, value: 90 }, { id: "g2", name: "backup-sources", priority: 0, value: 85 }] } }),
      field("critical", "Critical at", { value: 95, source: { kind: "recommended", label: "Recommended" } }),
    ], { conflicts: 1 });
    const got = packConflicts([c]);
    expect(got).toHaveLength(1);
    expect(got[0]!.field.name).toBe("warning");
    expect(got[0]!.priority).toBe(0);
    expect(got[0]!.groups.map((g) => `${g.name} ${g.value}`)).toEqual(["media-servers 90", "backup-sources 85"]);
  });
  it("is empty without conflicts", () => {
    expect(packConflicts([check([field("warning", "Warning at", { value: 85, source: { kind: "recommended", label: "Recommended" } })])])).toEqual([]);
  });
});

describe("the rule behind an alert, in one line", () => {
  it("names the values and where they come from", () => {
    const c = check([
      field("enabled", "Enabled", { value: 1, source: { kind: "recommended", label: "Recommended" } }),
      field("warning", "Warning at", { value: 80, source: { kind: "global", label: "Global" } }),
      field("critical", "Critical at", { value: 95, source: { kind: "recommended", label: "Recommended" } }),
    ]);
    (c.fields[0] as { type: string }).type = "bool";
    expect(ruleText(c)).toBe("warning 80% · critical 95% (Global)");
  });
  it("says Recommended when nothing is set anywhere", () => {
    expect(ruleText(check([field("warning", "Warning at", { value: 85, source: { kind: "recommended", label: "Recommended" } })]))).toBe("warning 85% (Recommended)");
  });
});

describe("where an alert or a health issue is looked at", () => {
  const a = (category: string, check: string, subject = "") => ({ host_id: "h1", category, check, subject }) as Alert;
  it("sends disk space, CPU and memory to the charts, services to the unit, storage health to its tab", () => {
    expect(alertMonitoringLink(a("disk", "filesystem", "/srv")).to).toBe("/hosts/h1/monitoring?tab=metrics");
    expect(alertMonitoringLink(a("cpu", "cpu")).to).toBe("/hosts/h1/monitoring?tab=metrics");
    expect(alertMonitoringLink(a("services", "service_failed", "backup.service")).to).toBe("/hosts/h1/monitoring?tab=services&unit=backup.service");
    expect(alertMonitoringLink(a("storage", "storage_health", "sda")).to).toBe("/hosts/h1/monitoring?tab=storage");
    expect(alertMonitoringLink(a("stale", "stale")).to).toBe("/hosts/h1/monitoring");
  });
  it("names the place a health issue links to", () => {
    expect(issueLink({ host_id: "h1", check: "filesystem", subject: "/srv" })).toEqual({ to: "/hosts/h1/monitoring?tab=metrics", label: "View metrics" });
    expect(issueLink({ host_id: "h1", check: "service_stopped", subject: "jellyfin.service" }).label).toBe("View jellyfin.service");
    expect(issueLink({ host_id: "h1", check: "fidelity" })).toEqual({ to: "/hosts/h1/monitoring?tab=agent", label: "View collection" });
    expect(issueLink({ host_id: "h1", check: "stale" })).toEqual({ to: "/hosts/h1/monitoring", label: "View monitoring" });
  });
});
