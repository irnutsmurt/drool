import { describe, expect, it } from "vitest";
import { desiredLayers } from "@/components/desired/provenance";
import { healthLayers, healthScope } from "@/components/health/provenance";

const ref = (name: string) => ({ id: "bl_" + name, name, revision: 1 });

describe("desired-state provenance", () => {
  it("lists what the effective value overrides, broadest first, ending at the value in effect", () => {
    const layers = desiredLayers({
      status: "drift", want: "absent",
      source: {
        level: "host", label: "Custom for this host", auto: false,
        shadowed: [
          { level: "group", label: "Inherited from group: web (baseline Web tools)", want: "installed", group: { id: "g1", name: "web", priority: 0 }, baseline: ref("Web tools") },
          { level: "global", label: "Required by Global baseline Server Defaults", want: "installed", baseline: ref("Server Defaults") },
        ],
      },
    });
    expect(layers.map((l) => [l.scope, l.value, l.state])).toEqual([
      ["Global baseline", "installed", "overridden"],
      ["Pack baseline · web (priority 0)", "installed", "overridden"],
      ["Host override", "absent", "effective"],
    ]);
  });

  it("shows an equal-priority conflict as competing values, none in effect", () => {
    const layers = desiredLayers({
      status: "conflict", want: "",
      source: {
        level: "group", label: "Conflict between groups web and lab (priority 0)", auto: false,
        competing: [
          { level: "group", label: "web", want: "installed", group: { id: "g1", name: "web", priority: 0 } },
          { level: "group", label: "lab", want: "absent", group: { id: "g2", name: "lab", priority: 0 } },
        ],
      },
    });
    expect(layers.map((l) => l.state)).toEqual(["conflict", "conflict"]);
    expect(layers.some((l) => l.state === "effective")).toBe(false);
    expect(layers.map((l) => l.value)).toEqual(["installed", "absent"]);
  });

  it("never invents scopes the server didn't report", () => {
    const layers = desiredLayers({ status: "compliant", want: "running", source: { level: "group", label: "x", auto: false, groups: [{ id: "g", name: "media", priority: 5 }], baselines: [ref("Media")] } });
    expect(layers).toHaveLength(1);
    expect(layers[0]).toMatchObject({ scope: "Pack baseline · media (priority 5)", state: "effective", value: "running" });
  });
});

describe("health provenance", () => {
  const field = (inherited: object) => ({ inherited } as never);

  it("names scopes in the shared vocabulary", () => {
    expect(healthScope({ kind: "group", label: "Inherited from group: NAS", groups: [{ id: "g", name: "NAS", priority: 0 }] })).toBe("Pack: NAS");
    expect(healthScope({ kind: "group", label: "Inherited from group: NAS" })).toBe("Pack: NAS");
    expect(healthScope({ kind: "subject", label: "Custom for /srv", subject_label: "/srv" })).toBe("Device: /srv");
    expect(healthScope({ kind: "recommended", label: "Recommended default" })).toBe("Recommended");
  });

  it("marks the inherited value in effect until this scope sets its own", () => {
    const f = field({ value: 90, source: { kind: "recommended", label: "Recommended default" } });
    expect(healthLayers(f, "global", undefined).map((l) => [l.scope, l.state])).toEqual([["Recommended", "effective"], ["Global", "unset"]]);
    expect(healthLayers(f, "host", 80).map((l) => [l.scope, l.value, l.state])).toEqual([["Recommended", 90, "overridden"], ["This host", 80, "effective"]]);
  });

  it("shows a pack conflict the server reports, between the fallback and this scope", () => {
    const f = field({ value: 85, source: { kind: "global", label: "Global" }, conflict: { priority: 0, groups: [{ id: "a", name: "lab", priority: 0 }, { id: "b", name: "edge", priority: 0 }] } });
    const l = healthLayers(f, "host", undefined);
    expect(l.map((x) => [x.scope, x.state])).toEqual([["Global", "effective"], ["Packs: lab, edge", "conflict"], ["This host", "unset"]]);
  });
});
