import { describe, expect, it } from "vitest";
import { destinations } from "@/components/layout/destinations";
import { buildGroups, type RecordResult } from "./entitysearch";
import { searchDestinations } from "./navsearch";

const admin = () => true;
const rec = (kind: RecordResult["kind"], title: string, extra: Partial<RecordResult> = {}): RecordResult =>
  ({ kind, id: `${kind}_${title}`, title, ...extra }) as RecordResult;
const order = (q: string, records: RecordResult[]) =>
  buildGroups(q, searchDestinations(q, destinations, admin), records, []).map((g) => g.id);

describe("search result groups", () => {
  it("lead with pages when the words name one", () => {
    expect(order("cron", [rec("run", "cron cleanup")])[0]).toBe("pages");
  });

  it("put hosts first, then pages a word of whose title or alias starts with the query, then the rest", () => {
    // "update": Shepherd updates, New run (update packages)… before units that merely contain the word.
    const records = [rec("host", "update-server"), rec("service", "man-db-cache-update.service", { host_id: "h1", host_name: "db-01" })];
    expect(order("update", records)).toEqual(["host", "pages", "service"]);
  });

  it("put records first when no page title starts a word with the query", () => {
    const records = [rec("host", "media-01"), rec("service", "jellyfin.service", { host_id: "h1", host_name: "media-01" })];
    const groups = order("jellyfin", records);
    expect(groups[0]).toBe("host");
    expect(groups.indexOf("service")).toBeLessThan(groups.includes("pages") ? groups.indexOf("pages") : Infinity);
  });

  it("show only pages for an empty query", () => {
    expect(order("", [])).toEqual(["pages"]);
  });
});
