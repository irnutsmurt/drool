import { useState } from "react";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { errorMessage } from "@/lib/format";
import { HealthEditor } from "./HealthEditor";
import { useDeleteOverride, type HealthCheck, type HealthField, type HealthSubject, type OverrideKey } from "./queries";
import { healthScope } from "./provenance";

type Src = HealthField["source"];

/** Formats a value the server resolved (null: none, or decided per sensor). */
export function fieldValue(v: number | null | undefined, f: { type: string; unit?: string }, src?: Src) {
  if (v === null || v === undefined) return src?.kind === "hardware" ? "per sensor" : src?.kind === "derived" ? "per sensor" : "none";
  if (f.type === "bool") return v ? "On" : "Off";
  return `${v}${f.unit === "%" ? "%" : f.unit ? ` ${f.unit}` : ""}`;
}

/** Where a value comes from. A custom temperature limit looks different
 * from the hardware's own. */
export function SourceBadge({ src }: { src: Src }) {
  switch (src.kind) {
    case "recommended":
      return <Badge>Recommended</Badge>;
    case "hardware":
      return <Badge tone="ok">⚙ Hardware limit</Badge>;
    case "derived":
      return <span title={src.label}><Badge>Derived</Badge></span>;
    case "unset":
      return <span title={src.label}><Badge>{src.label}</Badge></span>;
    case "global":
      return <Badge tone="accent">Global</Badge>;
    case "group":
      return <Badge tone="accent">{healthScope(src)}</Badge>;
    case "host":
      return <Badge tone="accent">This host</Badge>;
    case "subject":
      return <Badge tone="accent">{healthScope(src)}</Badge>;
  }
  return <Badge>{src.label}</Badge>;
}

const shown = ["enabled", "warning", "critical", "duration_min", "missed_intervals"];

/** One line: what the check does now. */
export function effectText(c: HealthCheck) {
  if (!c.enabled) return c.default_off ? "Off (off by default)" : "Off";
  const parts: string[] = [];
  for (const f of c.fields) {
    if (!shown.includes(f.name) || f.name === "enabled") continue;
    if (f.value === null) {
      if (f.name === "warning" && c.check === "temperature" && f.source.kind === "unset") parts.push("no limit reported");
      continue;
    }
    const v = fieldValue(f.value, f);
    parts.push(f.name === "warning" ? `warning ${v}` : f.name === "critical" ? `critical ${v}` : f.name === "duration_min" ? `for ${v}` : `after ${v} missed`);
  }
  if (c.check === "temperature" && c.fields.find((f) => f.name === "warning")?.source.kind === "hardware") parts.unshift("hardware limits");
  return parts.filter(Boolean).join(" · ") || "On";
}

/** The sources a check's values come from (excluding Recommended). */
export function sources(c: HealthCheck): Src[] {
  const seen = new Map<string, Src>();
  for (const f of c.fields) {
    if (["recommended", "derived", "unset"].includes(f.source.kind)) continue;
    seen.set(f.source.label, f.source);
  }
  return [...seen.values()];
}

/** The checks of one scope with their effective values, sources and a
 * Customize / Reset action each. */
export function HealthChecksTable({ checks, k, title, only }: {
  checks: HealthCheck[];
  k: Omit<OverrideKey, "check">;
  title: (c: HealthCheck) => string;
  only?: (c: HealthCheck) => boolean;
}) {
  const { can } = useAuth();
  const [editing, setEditing] = useState<HealthCheck>();
  const del = useDeleteOverride();
  const edit = k.scope === "host" ? can("monitoring.manage") : can("monitoring.settings");
  const rows = checks.filter((c) => !c.info && (!only || only(c)));
  return (
    <>
      {del.error && <div className="mb-2"><Alert>{errorMessage(del.error)}</Alert></div>}
      <div className="overflow-x-auto">
        <table className="rtable w-full text-left text-sm">
          <caption className="sr-only">Health checks</caption>
          <thead>
            <tr className="border-b border-line text-xs text-muted">
              <th className="px-2 py-1.5 font-medium">Check</th>
              <th className="px-2 py-1.5 font-medium">Now</th>
              <th className="px-2 py-1.5 font-medium">Source</th>
              <th className="px-2 py-1.5"><span className="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.check} className="border-b border-line last:border-0 align-top">
                <td data-primary="" className="px-2 py-2">
                  <div className="font-medium">{c.label}</div>
                  <div className="text-xs text-muted">{c.description}</div>
                </td>
                <td data-label="Now" className="px-2 py-2">{effectText(c)}</td>
                <td data-label="Source" className="px-2 py-2">
                  <div className="flex flex-wrap gap-1">
                    {sources(c).length === 0 ? <Badge>Recommended</Badge> : sources(c).map((s) => <SourceBadge key={s.label} src={s} />)}
                    {c.conflicts > 0 && <span title="Packs of equal priority set different values"><Badge tone="warn">⚠ Pack conflict</Badge></span>}
                  </div>
                </td>
                <td data-label="" className="whitespace-nowrap px-2 py-2 text-right">
                  <Button size="sm" onClick={() => setEditing(c)} aria-label={`${edit ? "Customize" : "View"} ${c.label}`}>{c.customized ? "Edit" : edit ? "Customize" : "View"}</Button>
                  {edit && c.override_id && (
                    <Button size="sm" variant="ghost" className="ml-1" disabled={del.isPending} onClick={() => del.mutate(c.override_id!)} aria-label={`Reset ${c.label} to inherited`}>
                      Reset
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && <HealthEditor k={{ ...k, check: editing.check }} check={editing} title={title(editing)} onClose={() => setEditing(undefined)} />}
    </>
  );
}

const strengthWord: Record<string, [string, "ok" | "neutral" | "warn"]> = {
  stable: ["Stable identity", "ok"],
  location: ["Identified by location", "neutral"],
  name: ["Identified by name", "neutral"],
  weak: ["Weak identity", "warn"],
};

export function IdentityBadge({ s }: { s: HealthSubject }) {
  if (!s.present) return <Badge tone="warn">Currently unavailable</Badge>;
  const [w, tone] = strengthWord[s.strength] ?? ["", "neutral"];
  return <span title={s.identity}><Badge tone={tone}>{w}</Badge></span>;
}
