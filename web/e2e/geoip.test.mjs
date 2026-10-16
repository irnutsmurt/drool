// Automatic GeoIP/ASN databases (UI/UX correction batch §12), run by
// TestGeoIPDownloads against a controller of its own in three phases
// (DROOL_E2E_GEOIP): the first download in progress, the databases in use,
// and an update that failed while the databases in use were kept.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, click, launch, session, shot, text, waitFor, waitText } from "./lib.mjs";

const phase = process.env.DROOL_E2E_GEOIP;
let browser, page;

before(async () => {
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

const status = () => page.$eval('[data-testid="enrichment-status"]', (e) => ({ state: e.getAttribute("data-state"), text: e.innerText }));

test("while the first download runs: preparing, never a configuration warning", { skip: phase !== "preparing" }, async () => {
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="enrichment-note"][data-state="preparing"]');
  const t = await text(page);
  assert.match(t, /Preparing country and network-owner data/);
  assert.ok(!/not configured|Configure enrichment/.test(t), "no configuration warning while preparing");
  await shot(page, "geoip-preparing-activity");
  await page.goto(base + "/network?tab=settings", { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="enrichment-status"][data-state="preparing"]');
  const s = await status();
  assert.match(s.text, /Preparing the databases/);
  assert.match(s.text, /Automatic downloads: on — DB-IP Lite \(CC BY 4\.0\)/);
  await shot(page, "geoip-preparing-settings");
  assert.deepEqual(page.errors, []);
});

test("in use: where the data comes from, Check now, the attribution", { skip: phase !== "available" }, async () => {
  await page.goto(base + "/network?tab=settings", { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="enrichment-status"][data-state="available"]');
  const month = new Date().toISOString().slice(0, 7);
  const s = await status();
  for (const want of ["Available", `DB-IP Lite, release ${month}`, "Last successful check", "Only the database files are downloaded", "never sends addresses",
    "IP Geolocation by DB-IP"]) {
    assert.ok(s.text.includes(want), `${want} in: ${s.text}`);
  }
  const href = await page.$eval('[data-testid="enrichment-downloads"] a', (a) => a.getAttribute("href"));
  assert.equal(href, "https://db-ip.com");
  await shot(page, "geoip-available-settings");
  // Check now: checks, then is available again.
  await click(page, "main button", "Check now");
  await waitFor(page, async () => (await status()).state === "available" && !(await status()).text.includes("Checking"), "the check finishing", 30000);
  // Network Activity: no note, the attribution with the data.
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await waitFor(page, async () => !!(await page.$('[data-testid="geo-attribution"] a[href="https://db-ip.com"]')), "the attribution under the table");
  assert.equal(await page.$('[data-testid="enrichment-note"]'), null, "no enrichment note when the databases are in use");
  await shot(page, "geoip-available-activity");
  assert.deepEqual(page.errors, []);
});

test("a failed update keeps the databases in use, and says so", { skip: phase !== "failed" }, async () => {
  await page.goto(base + "/network?tab=settings", { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="enrichment-status"][data-state="update_failed"]');
  const s = await status();
  assert.match(s.text, /Update failed — the databases in use are kept/);
  assert.match(s.text, /not usable/);
  await shot(page, "geoip-update-failed");
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await waitText(page, "IP Geolocation by DB-IP");
  assert.deepEqual(page.errors, []);
});
