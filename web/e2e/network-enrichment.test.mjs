// Country and network owner when their databases aren't there (dogfooding
// correction batch). Run by test/integration TestBrowser after it removed
// the test ASN database (DROOL_E2E_ENRICH=country), then the country one
// too (none). Network Activity explains it once at the top, with the way to
// configure enrichment; rows show a dash rather than an "unknown" that would
// be untrue; the details say why; "unknown" stays for a database that has
// no answer; lookups turned off are told apart from databases missing.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, text, waitFor } from "./lib.mjs";

const state = process.env.DROOL_E2E_ENRICH;
let browser, page;
before(async () => {
  if (!state) return;
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

const rows = () => page.$$eval('[data-testid="flow-table"] tbody tr[data-flow-id]', (rs) => rs.map((r) => r.innerText));
const note = () => page.$eval('[data-testid="enrichment-note"]', (e) => e.innerText).catch(() => "");

test("Network Activity without GeoIP/ASN databases: one explanation, dashes, the reason, and the way to configure them", { skip: !state && "needs DROOL_E2E_ENRICH" }, async () => {
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length > 0, "rows");
  const n = await note();
  if (state === "none") {
    assert.equal(n, "Country and network-owner information is unavailable because GeoIP/ASN databases are not configured. Configure enrichment →");
  } else {
    assert.equal(n, "Network-owner information is unavailable because no ASN database is configured; countries are shown. Configure enrichment →");
  }
  assert.equal(await page.$eval('[data-testid="enrichment-note"] a', (a) => a.getAttribute("href")), "/network?tab=settings");
  // No "unknown" for what was never looked up.
  const all = (await rows()).join("\n");
  assert.ok(!all.includes("network unknown"), "network unknown without an ASN database");
  if (state === "none") assert.ok(!all.includes("country unknown"), "country unknown without a country database");
  else assert.match(all, /\(US\)/, "countries are still shown");
  await shot(page, `network-enrichment-${state}`);

  // The address no database knows: with the country database loaded, its
  // country is genuinely unknown; without it, not looked up.
  await page.goto(base + "/network?peer=49.12.200.77", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length === 1, "the unknown address");
  const r = (await rows())[0];
  if (state === "country") assert.match(r, /country unknown/);
  else assert.doesNotMatch(r, /unknown/);
  // Its details say why.
  await click(page, '[data-testid="flow-table"] tbody button', "Details");
  await waitFor(page, async () => !!(await page.$('[data-testid="flow-details"]')), "details");
  const d = await page.$eval('[data-testid="flow-details"]', (e) => e.innerText);
  assert.match(d, /Network\s+Not looked up: no ASN database is configured/);
  if (state === "country") assert.match(d, /Country\s+Unknown \(the GeoIP country database has no answer for this address\)/);
  else assert.match(d, /Country\s+Not looked up: no country database is configured/);
  await page.keyboard.press("Escape");

  // The note's link: Collection and enrichment, which says the same.
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await note()) !== "", "the note");
  await click(page, '[data-testid="enrichment-note"] a', "Configure enrichment");
  await waitFor(page, async () => !!(await page.$('[data-testid="enrichment-status"]')), "enrichment status");
  const st = await page.$eval('[data-testid="enrichment-status"]', (e) => e.innerText);
  assert.match(st, /Network owner \(ASN\)\s+Not available/);
  if (state === "none") assert.match(st, /No databases\. Monitoring works without them/);

  // Lookups turned off: told apart from databases missing.
  const s = (await api(page, "GET", "/network/settings")).body;
  const put = (enrichment) => api(page, "PUT", "/network/settings", { collection: s.collection, retention_days: s.retention_days, max_flows_per_host: s.max_flows_per_host, enrichment });
  assert.equal((await put(false)).status, 200);
  try {
    await page.goto(base + "/network", { waitUntil: "networkidle0" });
    await waitFor(page, async () => (await note()).startsWith("Country and network-owner lookups are turned off."), "the off note");
    assert.ok(!(await text(page)).includes("not configured"), "off isn't called not configured");
  } finally {
    await put(true);
  }
  assert.deepEqual(page.errors, []);
});
