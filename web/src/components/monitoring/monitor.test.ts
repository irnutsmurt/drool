import { describe, expect, it } from "vitest";
import { isCustomized, monitorNote } from "./ServiceActions";
import type { MonService } from "./queries";

// A service row's Monitor box and its small line (dogfooding correction
// batch): the plain case (ticked on this host) says nothing more, where
// monitoring comes from is shown when inherited, and "Customized" is kept
// for settings worth calling out.
const unit = (u: Partial<MonService>): MonService => ({
  name: "nginx.service", description: "", load: "loaded", active: "active", sub: "running", file_state: "enabled", transient: false, known: true,
  changed_at: 0, state: "running", notable: true, watched: false, expected: "", monitored: false, inherited: "", customized: false,
  health: "unmonitored", actions: [], ...u,
});
const own = { kind: "subject", label: "set on this host", override_id: "mho_1" };

describe("the Monitor box's note", () => {
  it("says nothing for a service nobody monitors, or one ticked on this host", () => {
    expect(monitorNote(unit({}))).toBe("");
    expect(monitorNote(unit({ expected: "running", monitored: true, expected_source: own }))).toBe("");
    expect(isCustomized(unit({ expected: "running", monitored: true, expected_source: own }))).toBe(false);
  });
  it("names a pack or every host when monitoring comes from there", () => {
    const pack = unit({ expected: "running", monitored: true, inherited: "running", inherited_source: "from pack lab", expected_source: { kind: "group", label: "from pack lab" } });
    expect(monitorNote(pack)).toBe("from pack lab");
    expect(isCustomized(pack)).toBe(false);
    const every = unit({ expected: "stopped", monitored: true, inherited: "stopped", inherited_source: "set for every host", expected_source: { kind: "global", label: "set for every host" } });
    expect(monitorNote(every)).toBe("expected stopped, set for every host");
  });
  it("calls out an exception to a pack, and the advanced states set here", () => {
    const exception = unit({ expected: "ignored", inherited: "running", inherited_source: "from pack lab", expected_source: own });
    expect(monitorNote(exception)).toBe("not monitored here (from pack lab monitors it)");
    expect(isCustomized(exception)).toBe(true);
    const stopped = unit({ expected: "stopped", monitored: true, expected_source: own });
    expect(monitorNote(stopped)).toBe("expected stopped");
    expect(isCustomized(stopped)).toBe(true);
    const runningOverStopped = unit({ expected: "running", monitored: true, inherited: "stopped", inherited_source: "from pack lab", expected_source: own });
    expect(monitorNote(runningOverStopped)).toBe("expected running here (from pack lab: stopped)");
    expect(isCustomized(runningOverStopped)).toBe(true);
    expect(monitorNote(unit({ expected: "ignored", expected_source: own }))).toBe("ignored on this host");
  });
  it("calls a check's own settings customized", () => {
    expect(isCustomized(unit({ expected: "running", monitored: true, expected_source: own, customized: true }))).toBe(true);
  });
});
