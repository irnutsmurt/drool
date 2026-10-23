// Health overrides (M12.1): Recommended as the simple path, customizing a
// fleet-wide check with an impact preview, a group's overrides, a host's
// health settings with sources, a GPU customized by its own identity from
// the keyboard, and an alert closed by configuration reading "cleared",
// never "recovered". Needs test/integration TestBrowser's environment
// (fixture hosts nas-01, gpu-01, pi-sd …).
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;

before(async () => {
  browser = await launch();
});
after(async () => {
  await browser?.close();
});
beforeEach(async () => {
  page?.close().catch(() => {});
  page = await session(browser);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  page.ids = Object.fromEntries(hosts.map((h) => [h.name, h.id]));
  await waitFor(page, async () => {
    const v = (await api(page, "GET", `/hosts/${page.ids["gpu-01"]}/monitoring/health`)).body;
    return (v?.subjects ?? []).some((s) => s.kind === "gpu");
  }, "gpu-01's reports", 120000);
});

const go = async (path) => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await sleep(400);
};

// Takes away the overrides these tests made. Those there before the first
// test stay: the fixture's monitored services on nas-01 and pi-sd are
// host overrides (service_expected), and other test files rely on them.
let kept;
const cleanup = async () => {
  const all = (await api(page, "GET", "/monitoring/health/overrides")).body.items;
  kept ??= new Set(all.map((o) => o.id));
  for (const o of all) {
    if (!kept.has(o.id)) await api(page, "DELETE", `/monitoring/health/overrides/${o.id}`);
  }
};

const dialogText = () => page.$eval('[role="dialog"]', (d) => d.innerText);

test("settings: Recommended by default; a global change shows its impact, its source, and resets", async () => {
  await cleanup();
  // Health settings live under Monitoring; the old Settings address redirects there.
  await go("/settings/monitoring");
  await waitFor(page, async () => page.url().endsWith("/monitoring/settings"), "redirect to Monitoring");
  await waitText(page, "The Recommended profile, unchanged");
  assert.match(await text(page), /high GPU utilization is off/);
  // The checks are on the page: no reveal step.
  await waitText(page, "High CPU utilization");
  await click(page, "button", "Customize Memory pressure");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "editor");
  const d = await dialogText();
  // Where the value comes from: the shared inheritance pattern.
  assert.match(d, /Recommended\s*90%\s*✓ In effect/);
  assert.match(d, /Global\s*Not set/);
  assert.match(d, /inherited: 90%/);
  assert.match(d, /Who gets notified is set under/);
  // Typing a value sets it here (no Custom box first); the impact appears before saving.
  const num = await page.$('[role="dialog"] input[type="number"]');
  assert.equal(await num.evaluate((x) => x.disabled), false, "the field takes a value at once");
  await num.focus();
  // Out of range: said, not just a disabled button.
  await num.type("180");
  await waitFor(page, async () => /Must be 1–100/.test(await dialogText()), "range message");
  for (let i = 0; i < 4; i++) await page.keyboard.press("Backspace");
  await num.type("80");
  await waitFor(page, async () => /hosts? affected/.test(await dialogText()), "impact preview");
  assert.match(await dialogText(), /set here/);
  await shot(page, "health-global-editor-preview");
  await click(page, '[role="dialog"] button', "Save");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) === null, "dialog closed");
  await waitText(page, "The Recommended profile, with changes for every host");
  const row = await page.$$eval("tbody tr", (rs) => rs.map((r) => r.innerText).find((t) => t.startsWith("Memory pressure")));
  assert.match(row, /warning 80%/);
  assert.match(row, /Global/);
  // Reset to inherited: no value to copy back.
  await click(page, "button", "Reset Memory pressure to inherited");
  await waitText(page, "The Recommended profile, unchanged");
  const audit = (await api(page, "GET", "/audit?action=monitoring.health_override.create")).body.items;
  assert.ok(audit.length > 0, "audited");
  assert.deepEqual(page.errors, []);
});

test("host health settings: summary, identities, a GPU customized by keyboard, hardware limit vs custom", async () => {
  await cleanup();
  const id = page.ids["gpu-01"];
  await go(`/hosts/${id}/monitoring?tab=hardware`);
  // The tabs are a keyboard tablist: arrow to Health checks.
  await page.focus('[role="tab"][aria-selected="true"]');
  await page.keyboard.press("ArrowRight");
  await waitFor(page, async () => page.url().includes("tab=health"), "arrow to Health checks");
  await waitText(page, "Using recommended defaults");
  const t = await text(page);
  assert.match(t, /NVIDIA GeForce RTX 4090/);
  assert.match(t, /Stable identity/);
  assert.match(t, /Who gets notified is set in/);
  await shot(page, "health-host-tab");
  // Keyboard: Tab to the GPU's Customize button, Enter opens, Escape closes.
  await tabTo(page, () => document.activeElement?.getAttribute("aria-label")?.startsWith("Customize health check for NVIDIA"), "GPU customize button");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "subject dialog");
  await waitFor(page, async () => /High GPU utilization/.test(await dialogText()), "GPU checks");
  let d = await dialogText();
  assert.match(d, /NVIDIA GPU UUID/);
  assert.match(d, /Off \(off by default\)/);
  assert.match(d, /Temperature/);
  await page.keyboard.press("Escape");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) === null, "Escape closes");
  assert.match(await page.evaluate(() => document.activeElement?.getAttribute("aria-label") ?? ""), /Customize health check for NVIDIA/, "focus returns");
  // Enable utilization for this GPU only, by keyboard.
  await page.keyboard.press("Enter");
  await waitFor(page, async () => /High GPU utilization/.test((await page.$('[role="dialog"]')) ? await dialogText() : ""), "subject dialog again");
  // Customizing a check is the dialog's next step, never a dialog on top;
  // Escape goes back from it, and focus returns to the check's button.
  await click(page, '[role="dialog"] button', "Customize High GPU utilization");
  await waitFor(page, async () => !!(await page.$('[role="dialog"] [data-testid="health-editor"]')), "the editor step");
  assert.equal((await page.$$('[role="dialog"]')).length, 1, "no dialog on top");
  assert.match(await page.$eval('[role="dialog"] [data-dialog-back]', (b) => b.getAttribute("aria-label")), /^Back to .*NVIDIA/, "Back");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[role="dialog"] [data-testid="health-editor"]')) && (await page.$$('[role="dialog"]')).length === 1, "Escape: back to the checks");
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), "Customize High GPU utilization", "focus back on the check");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => !!(await page.$('[role="dialog"] [data-testid="health-editor"]')), "the editor step again");
  const sel = await page.$('[role="dialog"] [data-testid="health-editor"] select');
  await sel.select("on");
  await click(page, '[role="dialog"] button', "Save");
  await waitFor(page, async () => !(await page.$('[role="dialog"] [data-testid="health-editor"]')), "saved: back to the checks");
  // The source in the shared scope vocabulary (Recommended → Global → Pack → Host → Device).
  await waitFor(page, async () => /Device: NVIDIA GeForce RTX 4090/.test(await dialogText()), "subject source");
  await shot(page, "health-gpu-subject");
  await page.keyboard.press("Escape");
  // The effective setting comes from the server, with its source.
  const v = (await api(page, "GET", `/hosts/${id}/monitoring/health?subject=${encodeURIComponent("gpu:GPU-4a1f6c2e-8d3b-4f5a-9c71-2b0e9d6a1f30")}`)).body;
  const util = v.checks.find((c) => c.check === "gpu_utilization");
  assert.equal(util.enabled, true);
  assert.equal(util.fields.find((f) => f.name === "enabled").source.kind, "subject");
  // Sensors tab: the AMD sensor's limit is the hardware's; the row offers customizing.
  await go(`/hosts/${id}/monitoring?tab=hardware`);
  await click(page, "button", "Customize health check for amdgpu edge");
  await waitFor(page, async () => /Hardware limit/.test((await page.$('[role="dialog"]')) ? await dialogText() : ""), "hardware limit source");
  d = await dialogText();
  assert.match(d, /Chip amdgpu at PCI address 0000:0c:00.0/);
  await click(page, '[role="dialog"] button', "Customize Temperature");
  await waitFor(page, async () => !!(await page.$('[role="dialog"] [data-testid="health-editor"]')), "temperature editor");
  const ed = await dialogText();
  assert.match(ed, /Hardware limit\s*100 °C\s*✓ In effect/);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await cleanup();
  assert.deepEqual(page.errors, []);
});

test("group: overrides only when chosen, with an impact preview; hosts show the group as the source", async () => {
  await cleanup();
  const g = (await api(page, "POST", "/groups", { name: "e2e-render" })).body;
  await api(page, "PATCH", `/groups/${g.id}`, { host_ids: [page.ids["gpu-01"], page.ids["nas-01"]] });
  try {
    await go(`/groups/${g.id}`);
    await click(page, "button", "Health checks of e2e-render");
    await waitFor(page, async () => /Health checks: e2e-render/.test((await page.$('[role="dialog"]')) ? await dialogText() : ""), "group dialog");
    assert.match(await dialogText(), /2 hosts/);
    await click(page, '[role="dialog"] button', "Customize High CPU utilization");
    await waitFor(page, async () => !!(await page.$('[role="dialog"] [data-testid="health-editor"]')), "editor step");
    await (await page.$('[role="dialog"] [data-testid="health-editor"] select')).select("off");
    await waitFor(page, async () => /2 hosts affected/.test(await dialogText()), "preview: 2 hosts");
    assert.match(await dialogText(), /turns a check off/);
    await shot(page, "health-group-preview");
    await click(page, '[role="dialog"] button', "Save");
    await waitFor(page, async () => !(await page.$('[role="dialog"] [data-testid="health-editor"]')), "saved");
    await page.keyboard.press("Escape");
    await waitFor(page, async () => /Set here: High CPU utilization/.test(await page.$eval('[data-testid="pack-settings"]', (d) => d.innerText)), "the Pack's settings say it");
    await go(`/hosts/${page.ids["nas-01"]}/monitoring?tab=health`);
    await waitText(page, "1 inherited pack override");
    const row = await page.$$eval("tbody tr", (rs) => rs.map((r) => r.innerText).find((t) => t.startsWith("High CPU utilization")));
    assert.match(row, /Off/);
    assert.match(row, /Pack: e2e-render/);
  } finally {
    await cleanup();
    await api(page, "DELETE", `/groups/${g.id}`);
  }
  assert.deepEqual(page.errors, []);
});

test("an alert closed by configuration reads as cleared, never recovered", async () => {
  await cleanup();
  // Any open alert on a fixture host other than nas-01 (its critical alerts belong to other tests).
  let a;
  await waitFor(page, async () => {
    const open = (await api(page, "GET", "/alerts?view=open&limit=100")).body.items;
    a = open.find((x) => x.host_name !== "nas-01" && x.host_name !== "old-vm" && !x.host_name.startsWith("crash-"));
    return !!a;
  }, "an open alert", 120000);
  const r = await api(page, "PUT", "/monitoring/health/overrides", { scope: "host", host_id: a.host_id, check: a.check, fields: { enabled: false } });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  try {
    await waitFor(page, async () => (await api(page, "GET", `/alerts/${a.id}`)).body.alert.state === "resolved", "closed", 30000);
    await go(`/alerts/${a.id}`);
    await waitText(page, "Cleared: check turned off");
    const t = await text(page);
    assert.match(t, /not a recovery/);
    assert.match(t, /Cleared by configuration/);
    assert.doesNotMatch(t, /Condition recovered/);
    await shot(page, "health-alert-cleared");
  } finally {
    await cleanup();
  }
  assert.deepEqual(page.errors, []);
});
