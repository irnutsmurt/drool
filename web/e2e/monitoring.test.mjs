// System monitoring (M11): the fleet Monitoring page, a host's monitoring
// tabs, services, storage health (honest words for limited devices), the
// flash probe's wear warning, charts' table view, helpers and conversion
// previews, settings, keyboard use. Needs test/integration TestBrowser's
// environment: container hosts (web-1 over SSH) and fixture hosts whose
// data comes from DROOL_TEST_MONITOR_FILE (nas-01, pi-sd, gpu-01,
// edge-usb, old-vm).
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });
beforeEach(async () => {
  page?.close().catch(() => {});
  page = await session(browser);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  page.ids = Object.fromEntries(hosts.map((h) => [h.name, h.id]));
  // The fixture is loaded shortly after the hosts exist.
  await waitFor(page, async () => (await api(page, "GET", "/monitoring/summary")).body?.counts?.storage > 0, "monitoring fixture", 90000);
});

const go = async (path) => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await sleep(500);
};

test("fleet page: what needs attention, filters, agent vs SSH", async () => {
  await go("/monitoring");
  await waitText(page, "Needs attention");
  const t = await text(page);
  assert.match(t, /with the agent \(continuous\)/);
  assert.match(t, /over SSH \(polled\)/);
  assert.match(t, /Agent · continuous/);
  assert.match(t, /SSH · polled/);
  assert.match(t, /\/srv\/media is over 95% full/);
  assert.match(t, /Stale/, "the stale VM is shown as stale");
  // Keyboard: Tab to a category, Enter filters on the server.
  await tabTo(page, () => document.activeElement?.textContent?.startsWith("Low disk space"), "Low disk space filter");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => page.url().includes("category=disk"), "filtered url");
  await waitFor(page, async () => {
    const rows = await page.$$eval("table tbody tr", (r) => r.map((x) => x.innerText));
    return rows.length === 1 && rows[0].includes("nas-01");
  }, "disk filter");
  await shot(page, "monitoring-fleet-disk");
  assert.deepEqual(page.errors, []);
});

test("host overview: headline tiles link to details; unknown is never zero", async () => {
  await go(`/hosts/${page.ids["nas-01"]}`);
  await waitText(page, "Details and history");
  let t = await text(page);
  for (const want of ["CPU", "Memory", "Storage", "Temperature", "Network", "Services", "Storage devices", "Active health issues", "5/6 healthy"]) {
    assert.ok(t.includes(want), `host page shows ${want}`);
  }
  await click(page, "a", "Storage devices");
  await waitFor(page, async () => page.url().includes("tab=storage"), "storage tab");
  // A container host over SSH: values it can't know say so.
  await go(`/hosts/${page.ids["old-vm"]}/monitoring`);
  t = await text(page);
  assert.match(t, /No monitoring data since/);
  await shot(page, "monitoring-stale-host");
  // Tabs are a keyboard tablist.
  await go(`/hosts/${page.ids["nas-01"]}/monitoring`);
  await page.focus('[role="tab"][aria-selected="true"]');
  await page.keyboard.press("ArrowRight");
  await waitFor(page, async () => page.url().includes("tab=metrics"), "arrow to Metrics");
  assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Metrics");
  assert.deepEqual(page.errors, []);
});

test("charts: ranges, legend without colour, table view, gaps", async () => {
  await go(`/hosts/${page.ids["nas-01"]}/monitoring?tab=metrics`);
  await waitText(page, "Load average");
  for (const r of ["Hour", "7 days", "30 days"]) {
    await click(page, '[role="group"][aria-label="Time range"] button', r);
    await sleep(800);
    assert.equal(await page.$eval('[role="group"][aria-label="Time range"] [aria-pressed="true"]', (e) => e.textContent), r);
  }
  const svgs = await page.$$eval('figure svg[role="img"]', (s) => s.length);
  assert.ok(svgs >= 5, `charts drawn: ${svgs}`);
  // Series differ by dash pattern, not only colour.
  const dashes = await page.$$eval("figure figcaption svg line", (ls) => new Set(ls.map((l) => l.getAttribute("stroke-dasharray") ?? "")).size);
  assert.ok(dashes >= 2, "legend uses dash patterns");
  await click(page, "button", "Show as table");
  await waitFor(page, async () => (await page.$$("figure table tbody tr")).length > 5, "table view");
  await shot(page, "monitoring-chart-table");
  assert.deepEqual(page.errors, []);
});

test("services: filters, all units, history, watching", async () => {
  await go(`/hosts/${page.ids["nas-01"]}/monitoring?tab=services`);
  await waitText(page, "jellyfin.service");
  let t = await text(page);
  assert.ok(!t.includes("systemd-journald.service"), "noise hidden by default");
  await click(page, '[aria-label="Show services"] button', "Failed");
  await waitFor(page, async () => {
    const rows = await page.$$eval("table tbody tr", (r) => r.map((x) => x.innerText));
    return rows.length === 1 && rows[0].includes("backup.service") && rows[0].includes("Failed");
  }, "failed filter");
  await click(page, '[aria-label="Show services"] button', "All");
  await click(page, "label", "Show all units");
  await waitText(page, "systemd-journald.service");
  await click(page, "button", "History");
  await page.waitForSelector('[role="dialog"]');
  await page.keyboard.press("Escape");
  t = await text(page);
  assert.ok(!t.includes("state changes"), "dialog closed");
  await shot(page, "monitoring-services");
  assert.deepEqual(page.errors, []);
});

test("storage health: honest limited devices, failing disk, probe wear warning", async () => {
  await go(`/hosts/${page.ids["pi-sd"]}/monitoring?tab=storage`);
  await waitText(page, "SD / microSD card");
  let t = await text(page);
  assert.match(t, /Health telemetry: Limited/);
  assert.match(t, /No failure indicators detected/);
  assert.match(t, /Wear remaining\s+Not available/);
  assert.match(t, /Filesystem\s+Read\/write/);
  assert.match(t, /Write\/read probe\s+Passed/);
  assert.ok(!/SD health: Good|Healthy/.test(t), "an SD card is never called healthy");
  // A shorter probe interval is allowed, with the wear warning.
  const sel = await page.evaluateHandle(() => [...document.querySelectorAll("label")].find((l) => l.textContent.startsWith("Interval"))?.querySelector("select"));
  await sel.asElement().select("900");
  await waitText(page, "Frequent active storage probes create additional writes to flash media");
  await sel.asElement().select("3600");
  await shot(page, "monitoring-sd");
  await go(`/hosts/${page.ids["nas-01"]}/monitoring?tab=storage`);
  await waitText(page, "ST4000VN008");
  t = await text(page);
  assert.match(t, /Pending sectors\s+24/);
  assert.match(t, /Hard disk/);
  assert.match(t, /Health telemetry: Limited/, "the USB stick");
  await click(page, "summary", "Advanced: raw SMART attributes");
  await waitText(page, "Raw_Read_Error_Rate");
  await go(`/hosts/${page.ids["edge-usb"]}/monitoring?tab=storage`);
  t = await text(page);
  assert.match(t, /eMMC/);
  assert.match(t, /Life time estimate \(A\)\s+10–20% used/);
  assert.match(t, /SATA drive via USB bridge/);
  assert.deepEqual(page.errors, []);
});

test("sensors, GPU and missing helpers", async () => {
  await go(`/hosts/${page.ids["gpu-01"]}/monitoring?tab=hardware`);
  await waitText(page, "NVIDIA GeForce RTX 4090");
  let t = await text(page);
  assert.match(t, /No hardware limit reported/);
  assert.match(t, /GPU telemetry unavailable from the amdgpu driver/);
  assert.match(t, /Utilization\s+Not available/, "unknown GPU values are not zero");
  await go(`/hosts/${page.ids["gpu-01"]}/monitoring?tab=agent`);
  await waitText(page, "Would help");
  await click(page, "button", "Install nvme-cli");
  await page.waitForSelector('[role="dialog"]');
  await waitText(page, "need");
  t = await page.$eval('[role="dialog"]', (d) => d.innerText);
  assert.match(t, /nvme-cli/);
  assert.match(t, /never installs GPU drivers/);
  await shot(page, "monitoring-helper-preview");
  await click(page, '[role="dialog"] button', "Cancel");
  assert.deepEqual(page.errors, []);
});

test("bulk: helpers and agent conversion previews", async () => {
  await go("/monitoring?category=all");
  await waitText(page, "web-1");
  for (const n of ["gpu-01", "web-1"]) await click(page, "input[type=checkbox]", `Select ${n}`);
  await click(page, "button", "Install recommended helpers");
  await page.waitForSelector('[role="dialog"]');
  await waitText(page, "hosts selected");
  await click(page, '[role="dialog"] button', "Cancel");
  await click(page, "button", "Convert to agent");
  await page.waitForSelector('[role="dialog"]');
  await waitText(page, "Compatible:");
  const t = await page.$eval('[role="dialog"]', (d) => d.innerText);
  assert.match(t, /Canary hosts/);
  assert.match(t, /single-use token/);
  await shot(page, "monitoring-convert-preview");
  await page.keyboard.press("Escape");
  assert.deepEqual(page.errors, []);
});

test("settings: the Recommended health profile and saving fleet settings", async () => {
  // Thresholds are health overrides since M12.1 (web/e2e/health.test.mjs);
  // the fleet's sampling and retention settings still save here.
  await go("/settings/monitoring");
  await waitText(page, "Health checks");
  assert.match(await text(page), /Recommended profile/);
  await click(page, "button", "Save monitoring settings");
  await waitText(page, "Saved and sent to connected agents");
  assert.deepEqual(page.errors, []);
});
