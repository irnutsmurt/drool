import { describe, expect, it } from "vitest";
import { healthOf, worstOf } from "./Status";

// A host's health in words (final UI pass): no data, stale, healthy,
// degraded and unhealthy stay apart, and the severity is in the text, not
// only in the icon and colour.
describe("a host's health", () => {
  const h = (issues: { severity: string }[], extra: { hasData?: boolean; stale?: boolean } = {}) =>
    healthOf({ hasData: extra.hasData ?? true, stale: extra.stale ?? false, issues: issues.length, worst: worstOf(issues) });
  it("says when there is nothing to judge by", () => {
    expect(h([], { hasData: false })).toEqual({ status: "unknown", text: "No data" });
    expect(h([{ severity: "critical" }], { stale: true })).toEqual({ status: "warning", text: "Stale" });
  });
  it("tells degraded (a warning) from unhealthy (something critical)", () => {
    expect(h([])).toEqual({ status: "ok", text: "Healthy" });
    expect(h([{ severity: "warning" }])).toEqual({ status: "warning", text: "Degraded · 1 issue" });
    expect(h([{ severity: "warning" }, { severity: "critical" }, { severity: "info" }])).toEqual({ status: "critical", text: "Unhealthy · 3 issues" });
  });
  it("keeps informational notices from reading as a problem", () => {
    expect(h([{ severity: "info" }])).toEqual({ status: "info", text: "Healthy · 1 notice" });
  });
  it("finds the worst severity", () => {
    expect(worstOf([])).toBe("ok");
    expect(worstOf([{ severity: "info" }, { severity: "warning" }])).toBe("warning");
    expect(worstOf([{ severity: "critical" }, { severity: "info" }])).toBe("critical");
  });
});
