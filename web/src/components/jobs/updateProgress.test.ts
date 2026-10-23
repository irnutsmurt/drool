import { describe, expect, it } from "vitest";
import { recheckState, updateProgress } from "./updateProgress";

const h = (host_name: string, state: Parameters<typeof updateProgress>[2][number]["state"]) => ({ host_name, state });

describe("system update progress in words", () => {
  it("says what is happening, never an engine state twice", () => {
    expect(updateProgress("running", false, [h("media-02", "running")], "media-02")).toEqual(
      { title: "Installing updates on media-02", summary: "Installing updates on media-02…" });
    expect(updateProgress("running", false, [h("a", "succeeded"), h("b", "rebooting"), h("c", "queued")], "3 hosts")).toEqual(
      { title: "Installing updates on 3 hosts", summary: "1 of 3 hosts done." });
    expect(updateProgress("pending", false, [], "media-02").summary).toBe("Starting…");
    expect(updateProgress("paused", false, [h("a", "failed"), h("b", "queued")], "2 hosts").summary).toMatch(/^Paused after a failure/);
  });

  it("says the outcome once the run has ended, never installing", () => {
    expect(updateProgress("completed", true, [h("media-02", "succeeded")], "media-02")).toEqual(
      { title: "Updates installed on media-02", summary: "media-02 is updated." });
    expect(updateProgress("completed", true, [h("a", "succeeded"), h("b", "succeeded")], "2 hosts")).toEqual(
      { title: "Updates installed on 2 hosts", summary: "All 2 hosts are updated." });
    expect(updateProgress("completed", true, [h("a", "succeeded"), h("b", "failed")], "2 hosts")).toEqual(
      { title: "Updates finished on 2 hosts, not everywhere", summary: "1 updated · 1 failed." });
    expect(updateProgress("completed", true, [h("a", "timed_out")], "a")).toEqual({ title: "Updates failed on a", summary: "1 timed out." });
    expect(updateProgress("completed", true, [h("a", "skipped")], "a")).toEqual({ title: "No updates installed on a", summary: "1 skipped." });
    expect(updateProgress("cancelled", true, [h("a", "succeeded"), h("b", "cancelled")], "2 hosts").title).toBe("Updates stopped on 2 hosts");
    for (const s of ["completed", "cancelled", "interrupted"]) {
      expect(updateProgress(s, true, [h("a", "succeeded")], "a").title).not.toMatch(/Installing/);
    }
  });

  it("counts are the run's outcome only once the host was checked again after it", () => {
    const end = "2026-10-22T10:00:00Z";
    const t = Date.parse(end);
    expect(recheckState(end, "2026-10-22T09:59:00Z", t + 5_000)).toBe("checking");
    expect(recheckState(end, "2026-10-22T10:00:03Z", t + 5_000)).toBe("checked");
    expect(recheckState(end, null, t + 5_000)).toBe("checking");
    expect(recheckState(end, "2026-10-22T09:59:00Z", t + 200_000)).toBe("not_rechecked");
  });
});
