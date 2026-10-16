import { describe, expect, it } from "vitest";
import { advancedFilters } from "./FlowFilters";

describe("connection filters in effect beyond the search row", () => {
  it("counts what isn't the search, the host, the direction or a common question", () => {
    expect(advancedFilters({ q: "nas", host: ["h1"], direction: "out" })).toEqual([]);
    expect(advancedFilters({ scope: "external", class: "unknown", policy: "unclassified" })).toEqual([]);
    expect(advancedFilters({ new: "host", since: "24h", direction: "out" })).toEqual([]);
    expect(advancedFilters({ scope: "external", country: "US", asn: "15169" })).toEqual(["country", "asn"]);
  });
  it("counts the other values of a common filter's field", () => {
    expect(advancedFilters({ scope: "internal" })).toEqual(["scope"]);
    expect(advancedFilters({ policy: "denied" })).toEqual(["policy"]);
    expect(advancedFilters({ class: "drool" })).toEqual(["class"]);
    expect(advancedFilters({ new: "fleet", since: "7d" })).toEqual(["new"]);
  });
});
