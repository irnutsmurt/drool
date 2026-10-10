import type { HealthField } from "./queries";

type Src = HealthField["source"];

/** The scopes health settings inherit through, broadest first. */
export const healthScopes = ["Recommended", "Global", "Pack", "Host", "Device"];

/** A health setting's source as a scope name. */
export function healthScope(src: Src): string {
  switch (src.kind) {
    case "recommended":
      return "Recommended";
    case "hardware":
      return "Hardware limit";
    case "derived":
      return "Derived";
    case "global":
      return "Global";
    case "group": {
      const names = (src.groups ?? []).map((g) => g.name);
      return names.length ? `Pack: ${names.join(", ")}` : src.label.replace(/^Inherited from group:?\s*/i, "Pack: ");
    }
    case "host":
      return "This host";
    case "subject":
      return `Device: ${src.subject_label || src.subject || src.label}`;
    case "unset":
      return src.label;
  }
  return src.label;
}

export type EditScope = "global" | "group" | "host" | "subject";
export const editScopeName: Record<EditScope, string> = { global: "Global", group: "This pack", host: "This host", subject: "This device" };

export interface HealthLayer {
  key: string;
  scope: string;
  value?: number | null;
  /** Show the value as the server's "none"/"per sensor" wording. */
  src?: Src;
  state: "effective" | "overridden" | "conflict" | "unset";
  conflict?: { priority: number; packs: string[] };
}

/** Where one field's value comes from while it is edited at `scope`: the
 * inherited value (the server's answer), a conflict between packs of
 * equal priority if the server reports one, and this scope's own value. */
export function healthLayers(f: Pick<HealthField, "inherited">, scope: EditScope, own: number | undefined): HealthLayer[] {
  const out: HealthLayer[] = [
    { key: "inherited", scope: healthScope(f.inherited.source), value: f.inherited.value, src: f.inherited.source, state: own === undefined ? "effective" : "overridden" },
  ];
  const c = f.inherited.conflict;
  if (c) out.push({ key: "conflict", scope: `Packs: ${c.groups.map((g) => g.name).join(", ")}`, state: "conflict", conflict: { priority: c.priority, packs: c.groups.map((g) => g.name) } });
  out.push({ key: "own", scope: editScopeName[scope], value: own, state: own === undefined ? "unset" : "effective" });
  return out;
}
