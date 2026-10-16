import type { Schemas } from "@/api/client";
import type { SearchResult as PageResult } from "./navsearch";

export type RecordResult = Schemas["SearchResult"];
export type RecordKind = RecordResult["kind"];

/** Group headings, in the order records are listed. */
export const kindLabels: Record<RecordKind, string> = {
  host: "Hosts",
  service: "Services",
  alert: "Active alerts",
  pack: "Packs",
  run: "Runs",
};
export const kindOrder: RecordKind[] = ["host", "service", "alert", "pack", "run"];

/** Where a record opens: its own page, or the part of its host's
 * workspace that shows it (a service: the host's Services). */
export function recordPath(r: RecordResult): string {
  switch (r.kind) {
    case "host":
      return `/hosts/${r.id}`;
    case "pack":
      return `/groups/${r.id}`;
    case "service":
      return `/hosts/${r.host_id}/monitoring?tab=services&unit=${encodeURIComponent(r.id)}`;
    case "alert":
      return `/alerts/${r.id}`;
    case "run":
      return `/jobs/${r.id}`;
  }
}

/** Where "more" of a kind are listed, for a query. */
export function morePath(kind: RecordKind, q: string): string {
  switch (kind) {
    case "host":
      return `/hosts?q=${encodeURIComponent(q)}`;
    case "pack":
      return "/groups";
    case "service":
      return "/monitoring";
    case "alert":
      return "/alerts";
    case "run":
      return "/jobs";
  }
}

export type Entry =
  | { type: "page"; key: string; to: string; page: PageResult }
  | { type: "record"; key: string; to: string; record: RecordResult };

export interface Group {
  id: string;
  label: string;
  entries: Entry[];
  /** A link to everything of this kind ("more"). */
  more?: { to: string; label: string };
}

/** The palette's groups: records by kind and the matching pages. Pages
 * come first when one matches the query by its own name (a destination
 * someone typed: "alerts", "cron"); otherwise the records do ("media-01",
 * "jellyfin", an address). */
export function buildGroups(q: string, pages: PageResult[], records: RecordResult[] | undefined, more: RecordKind[]): Group[] {
  const pageGroup: Group = {
    id: "pages", label: "Pages and settings",
    entries: pages.map((p) => ({ type: "page", key: `page:${p.dest.id}`, to: p.dest.to, page: p })),
  };
  const recordGroups: Group[] = kindOrder
    .map((kind) => ({
      id: kind, label: kindLabels[kind],
      entries: (records ?? []).filter((r) => r.kind === kind)
        .map((r): Entry => ({ type: "record", key: `${kind}:${r.kind === "service" ? `${r.host_id}/` : ""}${r.id}`, to: recordPath(r), record: r })),
      more: more.includes(kind) ? { to: morePath(kind, q), label: `More ${kindLabels[kind].toLowerCase()}…` } : undefined,
    }))
    .filter((g) => g.entries.length > 0);
  const pagesFirst = !q.trim() || (pages[0]?.score ?? 0) >= 90;
  const out = pagesFirst ? [pageGroup, ...recordGroups] : [...recordGroups, pageGroup];
  return out.filter((g) => g.entries.length > 0);
}
