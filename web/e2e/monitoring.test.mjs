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
  assert.match(t, /with a Shepherd \(agent, continuous\)/);
  assert.match(t, /over SSH \(polled\)/);
  assert.match(t, /Shepherd · continuous/);
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
  await waitText(page, "Health now");
  let t = await text(page);
  // Services counts what is monitored (jellyfin and smbd, the fixture's);
  // the failed backup.service nobody monitors is a note, not a problem.
  for (const want of ["CPU", "Memory", "Storage", "Temperature", "Network", "Services", "Storage devices", "Active health issues",
    "2/2 monitored as expected", "1 failed, not monitored."]) {
    assert.ok(t.includes(want), `host page shows ${want}`);
  }
  await click(page, "main a", "Storage devices");
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
  // A unit's history and its monitoring settings are under its ⋯ menu.
  await click(page, 'button[aria-label="More for jellyfin.service"]', "");
  await click(page, '[role="menuitem"]', "State history");
  await page.waitForSelector('[role="dialog"]');
  await page.keyboard.press("Escape");
  t = await text(page);
  assert.ok(!t.includes("state changes"), "dialog closed");
  await shot(page, "monitoring-services");
  assert.deepEqual(page.errors, []);
});

test("services: Monitor is the one control; failed but unmonitored never alerts; Customize… sets the rest; actions warn against it", async () => {
  const id = page.ids["nas-01"];
  await go(`/hosts/${id}/monitoring?tab=services`);
  await waitText(page, "jellyfin.service");
  // plex.service is stopped and disabled: hidden as noise until all units show.
  await click(page, "label", "Show all units");
  await waitText(page, "plex.service");
  const row = (name) => page.$$eval("table tbody tr", (rs, n) => rs.find((r) => r.innerText.includes(n))?.innerText ?? "", name);
  const box = (unit) => `input[type="checkbox"][aria-label="Monitor ${unit}"]`;
  const monitored = (unit) => page.$eval(box(unit), (b) => b.checked);
  const settle = () => page.waitForNetworkIdle({ idleTime: 300 }).catch(() => {});
  const customize = async (unit, choice) => {
    await click(page, `button[aria-label="More for ${unit}"]`, "");
    await click(page, '[role="menuitem"]', "Customize monitoring…");
    await page.waitForSelector('[data-testid="service-monitor"]');
    await click(page, '[data-testid="service-monitor"] label', choice);
    await click(page, '[data-testid="service-monitor"] button[type="submit"]', "Save");
    await waitFor(page, async () => !(await page.$('[data-testid="service-monitor"]')), "customize closed");
    await settle();
  };
  // The table answers "what runs, and is it monitored": a Monitor box, no
  // per-row policy editor.
  assert.equal(await page.$$eval('table select[aria-label^="Expected state of"]', (s) => s.length), 0, "no expected-state dropdown per row");
  const head = await page.$$eval("table thead th", (th) => th.map((x) => x.innerText.trim()));
  assert.ok(head.includes("Monitor") && !head.includes("Expected"), "columns: " + head.join(", "));
  // jellyfin.service is monitored by this host (the fixture's watched
  // service): ticked, nothing more to say, not "customized".
  assert.equal(await monitored("jellyfin.service"), true);
  assert.match(await row("jellyfin.service"), /Expected to run \(set on this host\)/);
  assert.doesNotMatch(await row("jellyfin.service"), /Customized/);
  // backup.service failed, and nobody monitors it: shown as failed, says
  // it doesn't alert, unticked.
  assert.equal(await monitored("backup.service"), false);
  assert.match(await row("backup.service"), /Failed/);
  assert.match(await row("backup.service"), /Failed, but not monitored, so it doesn't alert/);
  const alerts = (await api(page, "GET", `/alerts?view=open&host_id=${id}`)).body.items;
  assert.ok(!alerts.some((a) => a.subject === "backup.service"), "an unmonitored failed service alerted");
  await shot(page, "monitoring-services-monitor");
  try {
    // Ticking Monitor expects plex.service to run: stopped is then a problem.
    await page.click(box("plex.service"));
    await waitFor(page, async () => (await row("plex.service")).includes("Not running, though expected to run (set on this host)"), "plex monitored");
    assert.equal(await monitored("plex.service"), true);
    // Its actions fit a stopped, disabled unit: no Stop, no Disable.
    await click(page, 'button[aria-label="Actions for plex.service"]', "Actions");
    await waitFor(page, async () => !!(await page.$('[data-testid="service-action"] [aria-label="Actions"]')), "actions");
    const offered = await page.$$eval('[data-testid="service-action"] [aria-label="Actions"] button', (bs) => bs.map((b) => b.getAttribute("aria-label")));
    assert.deepEqual(offered, ["Start plex.service", "Enable plex.service", "Enable & Start plex.service"]);
    await page.keyboard.press("Escape");
    // Unticking takes the host's setting away again.
    await page.click(box("plex.service"));
    await waitFor(page, async () => !(await row("plex.service")).includes("expected to run"), "plex unmonitored");
    assert.equal(await monitored("plex.service"), false);

    // Customize… (the advanced path): jellyfin.service expected stopped.
    // Running is then the problem; the row says so and is "customized".
    await customize("jellyfin.service", "Monitor: expect it to be stopped");
    await waitFor(page, async () => (await row("jellyfin.service")).includes("Running, though expected to be stopped (set on this host)"), "jellyfin expected stopped");
    assert.equal(await monitored("jellyfin.service"), true, "expected stopped is monitored");
    assert.match(await row("jellyfin.service"), /expected stopped/);
    assert.match(await row("jellyfin.service"), /Customized/);
    await click(page, 'button[aria-label="Actions for jellyfin.service"]', "Actions");
    await waitFor(page, async () => !!(await page.$('[data-testid="service-action"] [aria-label="Actions"]')), "actions");
    const jelly = await page.$$eval('[data-testid="service-action"] [aria-label="Actions"] button', (bs) => bs.map((b) => b.getAttribute("aria-label")));
    assert.deepEqual(jelly, ["Stop jellyfin.service", "Restart jellyfin.service", "Disable jellyfin.service", "Disable & Stop jellyfin.service"]);
    await click(page, '[data-testid="service-action"] button', "Restart jellyfin.service");
    await waitFor(page, async () => (await page.$eval('[data-testid="service-action"]', (e) => e.innerText)).includes("is expected to be stopped (set on this host)"), "the warning");
    assert.match(await page.$eval('[data-testid="service-action"]', (e) => e.innerText), /DROOL doesn.t change that for you/);
    await shot(page, "service-action-warning");
    await click(page, '[data-testid="service-action"] button', "Back");
    await page.keyboard.press("Escape");

    // backup.service: static, so nothing to enable (the dialog says why);
    // ignored on this host from Customize…: shown, never an alert.
    await click(page, 'button[aria-label="Actions for backup.service"]', "Actions");
    await waitFor(page, async () => !!(await page.$('[data-testid="action-note"]')), "action note");
    assert.match(await page.$eval('[data-testid="action-note"]', (e) => e.innerText), /Static: started by other units/);
    await page.keyboard.press("Escape");
    await customize("backup.service", "Ignore it on this host");
    await waitFor(page, async () => (await row("backup.service")).includes("Ignored (set on this host): its state is shown, never a health issue or alert"), "backup ignored");
    assert.match(await row("backup.service"), /ignored on this host/);
    // The Monitored filter lists what is monitored.
    await click(page, '[aria-label="Show services"] button', "Monitored");
    await waitFor(page, async () => {
      const rows = await page.$$eval("table tbody tr", (r) => r.map((x) => x.innerText));
      return rows.length >= 1 && rows.some((r) => r.includes("jellyfin.service")) && !rows.some((r) => r.includes("backup.service"));
    }, "monitored filter");
    await click(page, '[aria-label="Show services"] button', "All");
    await shot(page, "monitoring-services-expected");
  } finally {
    // Back to what the other tests expect: jellyfin.service monitored by
    // this host, backup.service and plex.service not.
    await customize("jellyfin.service", "Monitor: expect it to run").catch(() => {});
    await customize("backup.service", "Reset to inherited").catch(() => {});
    if (await monitored("plex.service")) await page.click(box("plex.service"));
    await settle();
  }
  await waitFor(page, async () => (await row("jellyfin.service")).includes("Expected to run (set on this host)") && !(await row("jellyfin.service")).includes("Customized"),
    "jellyfin monitored as before");
  await waitFor(page, async () => !(await row("backup.service")).includes("Ignored"), "backup back to unmonitored");
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
  // The storage section never calls an SD card healthy. (The header above
  // it names the host's Health: monitoring's verdict on the whole host.)
  const section = await page.evaluate(() => {
    const main = document.querySelector("main").cloneNode(true);
    main.querySelector('[data-testid="host-header"]')?.remove();
    return main.textContent;
  });
  assert.ok(!/SD health: Good|Healthy/.test(section), "an SD card is never called healthy");
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

test("bulk: helpers and Shepherd (agent) conversion previews", async () => {
  await go("/monitoring?category=all");
  await waitText(page, "web-1");
  for (const n of ["gpu-01", "web-1"]) await click(page, "input[type=checkbox]", `Select ${n}`);
  await click(page, "button", "Install recommended helpers");
  await page.waitForSelector('[role="dialog"]');
  await waitText(page, "hosts selected");
  await click(page, '[role="dialog"] button', "Cancel");
  await click(page, "button", "Convert to Shepherd");
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
  await go("/monitoring/settings");
  await waitText(page, "Health checks");
  assert.match(await text(page), /Recommended profile/);
  await click(page, "button", "Save monitoring settings");
  await waitText(page, "Saved and sent to connected Shepherds");
  assert.deepEqual(page.errors, []);
});
