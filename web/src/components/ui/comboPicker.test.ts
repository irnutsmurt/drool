import { describe, expect, it } from "vitest";
import { matchOptions, type PickOption } from "./ComboPicker";

const opt = (label: string, group: string, extra: Partial<PickOption> = {}): PickOption => ({ value: label, label, group, ...extra });

describe("ComboPicker matching", () => {
  const many = Array.from({ length: 12 }, (_, i) => opt(`host-${i}`, "Hosts"));
  const options = [opt("Internal networks", "Common", { search: "lan" }), ...many, opt("web", "Packs", { detail: "2 hosts" })];

  it("shows a few of each group before anything is typed", () => {
    const shown = matchOptions(options, "");
    expect(shown.filter((o) => o.group === "Hosts")).toHaveLength(8);
    expect(shown.map((o) => o.group)).toEqual(expect.arrayContaining(["Common", "Packs"]));
  });

  it("finds labels, details and search words, in group order, starts-with first within a group", () => {
    expect(matchOptions(options, "lan").map((o) => o.label)).toEqual(["Internal networks"]);
    expect(matchOptions(options, "2 hosts").map((o) => o.label)).toEqual(["web"]);
    expect(matchOptions([opt("my-web", "Hosts"), opt("web-1", "Hosts")], "web").map((o) => o.label)).toEqual(["web-1", "my-web"]);
    expect(matchOptions(options, "HOST-1").map((o) => o.label)).toEqual(["host-1", "host-10", "host-11"]);
    expect(matchOptions(options, "nothing")).toEqual([]);
  });

  it("puts the group with the best match first", () => {
    const opts = [opt("web-1", "Hosts", { detail: "linux-servers" }), opt("db-1", "Hosts", { detail: "linux-servers" }), opt("linux-servers", "Packs")];
    expect(matchOptions(opts, "linux").map((o) => o.label)).toEqual(["linux-servers", "web-1", "db-1"]);
    expect(matchOptions(opts, "web").map((o) => o.label)).toEqual(["web-1"]);
  });
});
