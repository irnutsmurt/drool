import { describe, expect, it } from "vitest";
import { actionLabel, decide, plan, type Mode } from "./applyChoices";
import type { FwPlan } from "./queries";

const host = (name: string, mode: Mode) => ({ host_id: `id-${name}`, host_name: name, config: { mode } }) as unknown as FwPlan;
const label = (m: string) => m[0]!.toUpperCase() + m.slice(1);

describe("review and apply: a host's mode decides what happens to it", () => {
  it("applies to a host in Enforce mode, without asking", () => {
    expect(decide(host("a", "enforce"), undefined, null)).toEqual({ kind: "apply", switch: false });
    expect(decide(host("a", "enforce"), "keep", null)).toEqual({ kind: "apply", switch: false });
  });
  it("leaves out a host in Enforce mode the server would refuse", () => {
    expect(decide(host("a", "enforce"), undefined, "a firewall transaction is pending")).toEqual({ kind: "none", why: "a firewall transaction is pending" });
  });
  it("never switches a host to Enforce by default: it keeps its mode", () => {
    for (const m of ["off", "monitor", "guided"] as Mode[]) {
      expect(decide(host("a", m), undefined, null).kind).toBe("none");
      expect(decide(host("a", m), "keep", null).kind).toBe("none");
      expect(decide(host("a", m), m, null).kind).toBe("none"); // its own mode: unchanged
    }
  });
  it("switches to Enforce and applies only where Enforce was chosen, and nothing refuses it", () => {
    expect(decide(host("a", "off"), "enforce", null)).toEqual({ kind: "apply", switch: true });
    expect(decide(host("a", "monitor"), "enforce", null)).toEqual({ kind: "apply", switch: true });
    expect(decide(host("a", "monitor"), "enforce", "DROOL can't enforce on this host: Docker is running").kind).toBe("none");
  });
  it("only changes the mode for Off, Monitor or Guided: nothing is applied", () => {
    expect(decide(host("a", "off"), "monitor", null)).toEqual({ kind: "mode", to: "monitor" });
    expect(decide(host("a", "off"), "guided", null)).toEqual({ kind: "mode", to: "guided" });
    expect(decide(host("a", "monitor"), "off", null)).toEqual({ kind: "mode", to: "off" });
  });
});

describe("review and apply with hosts in different modes", () => {
  const hosts = [host("enf", "enforce"), host("mon", "monitor"), host("off", "off"), host("gui", "guided")];
  it("runs exactly what was chosen per host", () => {
    const choices: Record<string, string | undefined> = { "id-mon": "enforce", "id-off": "guided" }; // gui: kept
    const r = plan(hosts, hosts.map((p) => decide(p, choices[p.host_id] as Mode | undefined, null)));
    expect(r.apply.map((p) => p.host_name)).toEqual(["enf", "mon"]);
    expect(r.switching.map((p) => p.host_name)).toEqual(["mon"]);
    expect(r.modes.map((m) => [m.p.host_name, m.to])).toEqual([["off", "guided"]]);
    expect(r.untouched.map((p) => p.host_name)).toEqual(["gui"]);
    expect(actionLabel(r, label)).toBe("Switch to Enforce and apply to 2 hosts and change off's mode to Guided");
  });
  it("with nothing chosen, applies only to the host already in Enforce", () => {
    const r = plan(hosts, hosts.map((p) => decide(p, undefined, null)));
    expect(r.apply.map((p) => p.host_name)).toEqual(["enf"]);
    expect(r.switching).toEqual([]);
    expect(r.modes).toEqual([]);
    expect(actionLabel(r, label)).toBe("Apply policy");
  });
  it("words a mode-only change as one", () => {
    const one = [host("off", "off")];
    expect(actionLabel(plan(one, [decide(one[0]!, "monitor", null)]), label)).toBe("Change off's mode to Monitor");
    expect(actionLabel(plan(one, [decide(one[0]!, "enforce", null)]), label)).toBe("Switch to Enforce and apply");
  });
});
