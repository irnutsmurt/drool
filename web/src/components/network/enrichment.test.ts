import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { CountryLabel, NetworkLabel, noLookup } from "./labels";
import type { NetEnrichState, NetFlow } from "./queries";
import { EnrichmentNote } from "@/pages/network/NetworkPage";

// Country and network-owner data say why they're missing (dogfooding
// correction batch): "Unknown" only when a database is loaded and has no
// answer; a dash, explained once on the page, when nothing was looked up.
const flow = (geo: NetFlow["geo"], scope: NetFlow["scope"] = "public") => ({ scope, geo }) as NetFlow;
const html = (el: Parameters<typeof renderToStaticMarkup>[0]) => renderToStaticMarkup(el);
const st = (country: NetEnrichState["country"], asn: NetEnrichState["asn"]): NetEnrichState => ({ country, asn });

describe("a public address without country or network data", () => {
  it("is unknown only when a database of that kind is loaded", () => {
    const both = st("on", "on");
    expect(html(createElement(CountryLabel, { flow: flow(null), state: both }))).toContain("country unknown");
    expect(html(createElement(NetworkLabel, { flow: flow(null), state: both }))).toContain("network unknown");
  });
  it("is a dash, with the reason, when there is no database", () => {
    const none = st("unavailable", "unavailable");
    const c = html(createElement(CountryLabel, { flow: flow(null), state: none }));
    expect(c).not.toContain("unknown");
    expect(c).toContain("—");
    expect(c).toContain("no country database is configured");
    expect(html(createElement(NetworkLabel, { flow: flow(null), state: none }))).toContain("no ASN database is configured");
  });
  it("is a dash when lookups are off", () => {
    const off = st("off", "off");
    expect(html(createElement(CountryLabel, { flow: flow(null), state: off }))).toContain("country lookups are off");
    expect(html(createElement(NetworkLabel, { flow: flow(null), state: off }))).toContain("network-owner lookups are off");
  });
  it("shows the side that is available, and says why the other isn't", () => {
    const countryOnly = st("on", "unavailable");
    const f = flow({ country_code: "NL", country: "Netherlands" });
    expect(html(createElement(CountryLabel, { flow: f, state: countryOnly }))).toContain("Netherlands");
    const n = html(createElement(NetworkLabel, { flow: f, state: countryOnly }));
    expect(n).toContain("no ASN database is configured");
    expect(n).not.toContain("unknown");
    const asnOnly = st("unavailable", "on");
    const g = flow({ asn: 15169, org: "Google LLC" });
    expect(html(createElement(NetworkLabel, { flow: g, state: asnOnly }))).toContain("AS15169");
    expect(html(createElement(CountryLabel, { flow: g, state: asnOnly }))).toContain("no country database is configured");
  });
  it("private and special addresses are never looked up, whatever the state", () => {
    expect(html(createElement(CountryLabel, { flow: flow(null, "internal"), state: st("on", "on") }))).toContain("internal");
    expect(html(createElement(NetworkLabel, { flow: flow(null, "special"), state: st("unavailable", "unavailable") }))).toBe("");
  });
  it("noLookup names the reason per kind", () => {
    expect(noLookup(st("on", "unavailable"), "country")).toBeNull();
    expect(noLookup(st("on", "unavailable"), "asn")).toBe("no ASN database is configured");
    expect(noLookup(undefined, "asn")).toBeNull();
  });
});

describe("the Network Activity note", () => {
  const note = (s: NetEnrichState) => html(createElement(MemoryRouter, null, createElement(EnrichmentNote, { state: s })));
  it("explains missing databases once, with the way to configure them", () => {
    const n = note(st("unavailable", "unavailable"));
    expect(n).toContain("Country and network-owner information is unavailable because GeoIP/ASN databases are not configured.");
    expect(n).toContain('href="/network?tab=settings"');
    expect(n).toContain("Configure enrichment");
  });
  it("names the missing side when only one is configured", () => {
    expect(note(st("on", "unavailable"))).toContain("Network-owner information is unavailable because no ASN database is configured");
    expect(note(st("unavailable", "on"))).toContain("Country information is unavailable because no GeoIP country database is configured");
  });
  it("says lookups are off when they are", () => {
    expect(note(st("off", "off"))).toContain("Country and network-owner lookups are turned off.");
  });
  it("is absent when both databases are loaded", () => {
    expect(note(st("on", "on"))).toBe("");
  });
});
