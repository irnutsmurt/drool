import type { DesiredResourceView, DesiredSource } from "./queries";

type Level = DesiredSource["level"];
type Decl = NonNullable<DesiredSource["competing"]>[number];
type Ref = { id: string; name: string; revision: number };
type Pack = { id: string; name: string; priority: number };

/** A desired-state value at one scope, as the server reported it. */
export interface DesiredLayer {
  key: string;
  level: Level;
  scope: string;
  value: string;
  baselines: Ref[];
  label: string;
  state: "effective" | "overridden" | "conflict";
}

/** The scopes desired state inherits through, broadest first. */
export const desiredScopes = ["Global baseline", "Pack baseline", "Baseline for this host", "Host override"];

const rank: Record<Level, number> = { global: 0, group: 1, assigned: 2, host: 3 };

export function desiredScope(level: Level, packs: Pack[] = []): string {
  switch (level) {
    case "global":
      return "Global baseline";
    case "group":
      return packs.length ? `Pack baseline · ${packs.map((g) => `${g.name} (priority ${g.priority})`).join(", ")}` : "Pack baseline";
    case "assigned":
      return "Baseline for this host";
    case "host":
      return "Host override";
  }
}

const fromDecl = (d: Decl, key: string, state: DesiredLayer["state"]): DesiredLayer => ({
  key, level: d.level, scope: desiredScope(d.level, d.group ? [d.group] : []), value: d.want || "—",
  baselines: d.baseline ? [d.baseline] : [], label: d.label, state,
});

/** Where a resource's desired value comes from: the declarations it
 * overrides, then the one in effect — or, in a conflict, the competing
 * declarations, none in effect. Everything comes from the server's
 * resolution; this only orders it broadest first for display. */
export function desiredLayers(r: Pick<DesiredResourceView, "status" | "want" | "source">): DesiredLayer[] {
  const src = r.source;
  const out: DesiredLayer[] = (src.shadowed ?? []).map((d, i) => fromDecl(d, `shadowed-${i}`, "overridden"));
  const competing = src.competing ?? [];
  if (r.status === "conflict" || competing.length > 0) {
    competing.forEach((d, i) => out.push(fromDecl(d, `competing-${i}`, "conflict")));
  } else {
    out.push({ key: "effective", level: src.level, scope: desiredScope(src.level, src.groups ?? []), value: r.want || "—",
      baselines: src.baselines ?? [], label: src.label, state: "effective" });
  }
  return out.map((l, i) => ({ l, i })).sort((a, b) => rank[a.l.level] - rank[b.l.level] || a.i - b.i).map((x) => x.l);
}
