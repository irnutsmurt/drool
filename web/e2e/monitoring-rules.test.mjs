// Phase 2 (ARCHITECTURE-ALPHA §47): from an alert to the health check
// behind it — what fired, where it comes from, and changing it for the
// device, the host, a Pack or every host, in place — and Packs that
// disagree about a host, said plainly and settled in one step. Needs
// test/integration TestBrowser's environment (the monitoring fixture:
// nas-01's /srv/media at 96 %, a critical Disk space alert).
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;
const ids = {};

before(async () => {
  browser = await launch();
  page = await session(browser);
  for (const h of (await api(page, "GET", "/hosts")).body.items) ids[h.name] = h.id;
  await waitFor(page, async () => !!(await mediaAlert()), "nas-01's /srv/media alert (the monitoring fixture)", 120000);
});
after(async () => browser?.close());

const mediaAlert = async () => ((await api(page, "GET", `/alerts?view=active&host_id=${ids["nas-01"]}`)).body?.items ?? [])
  .find((a) => a.check === "filesystem" && a.subject === "/srv/media");
const inner = (sel) => page.$eval(sel, (e) => e.innerText).catch(() => "");
const overrides = async () => {
  const b = (await api(page, "GET", "/monitoring/health/overrides")).body;
  return Array.isArray(b) ? b : b.items ?? [];
};
const cleanup = async (match) => {
  for (const o of await overrides()) if (match(o)) await api(page, "DELETE", `/monitoring/health/overrides/${o.id}`);
};

test("from an alert's panel: what fired, the scopes, and a change for one filesystem in place", async () => {
  const a = await mediaAlert();
  await cleanup((o) => o.host_id === ids["nas-01"] && o.check === "filesystem");
  try {
    await page.goto(`${base}/alerts`, { waitUntil: "networkidle0" });
    await click(page, "button", `Actions for ${a.title} on nas-01`);
    await waitFor(page, async () => (await inner('[data-testid="alert-actions"]')).includes("Adjust when this alerts…"), "the choice", 30000);
    // The choice already says what fired.
    assert.match(await inner('[data-testid="alert-actions"]'), /Now: warning \d+% · critical 95% \((Global|Recommended)/);
    await click(page, '[data-testid="alert-actions"] button', "Adjust when this alerts…");
    await page.waitForSelector('[data-testid="alert-rule"]');
    const r = await inner('[data-testid="alert-rule"]');
    assert.match(r, /What fired\s*Disk space for \/srv\/media: warning \d+% · critical 95%/i);
    for (const scope of ["Only /srv/media on nas-01", "Every filesystem on nas-01", "Every host"]) assert.ok(r.includes(scope), `scope ${scope}`);
    // The narrowest scope is chosen; focus is in the step.
    assert.equal(await page.$eval('input[name="alert-rule-scope"]:checked', (x) => x.closest("label").innerText.split("\n")[0]), "Only /srv/media on nas-01");
    assert.ok(await page.evaluate(() => !!document.activeElement?.closest('[data-testid="alert-inspector"]')), "focus inside the panel");
    await shot(page, "rules-alert-step");
    // Typing a value sets it here: warning 97, critical 99.
    const nums = await page.$$('[data-testid="alert-rule"] [data-testid="health-editor"] input[type="number"]');
    await nums[0].type("97");
    await nums[1].type("99");
    await click(page, '[data-testid="alert-rule"] button', "Save");
    await waitFor(page, async () => !(await page.$('[data-testid="alert-rule"]')), "back to the panel");
    const o = (await overrides()).find((x) => x.host_id === ids["nas-01"] && x.check === "filesystem");
    assert.ok(o, "an override");
    assert.ok(o.subject, "for the filesystem, not the host");
    assert.deepEqual(o.fields, { warning: 97, critical: 99 });
    assert.deepEqual(page.errors, []);
  } finally {
    await cleanup((o) => o.host_id === ids["nas-01"] && o.check === "filesystem");
  }
});

test("from the alert's page: Adjust when this alerts… for every host shows the impact before anything is saved", async () => {
  // Its condition is re-raised within a minute once the filesystem's own setting is gone.
  await waitFor(page, async () => !!(await mediaAlert()), "the alert open again", 120000);
  const a = await mediaAlert();
  await page.goto(`${base}/alerts/${a.id}`, { waitUntil: "networkidle0" });
  await waitFor(page, async () => /Adjust when it alerts/.test(await inner('[data-testid="alert-quieter"]')), "the quieter card");
  await click(page, '[data-testid="alert-quieter"] button', "Adjust when this alerts…");
  await page.waitForSelector('[role="dialog"] [data-testid="alert-rule"]');
  await click(page, '[role="dialog"] label', "Every host");
  await waitFor(page, async () => !!(await page.$('[role="dialog"] [data-testid="health-editor"]')), "the editor for every host");
  const num = await page.$('[role="dialog"] [data-testid="health-editor"] input[type="number"]');
  await num.type("98");
  await waitFor(page, async () => /hosts? affected/.test(await inner('[role="dialog"]')), "the impact preview");
  await shot(page, "rules-alert-page-every-host");
  await click(page, '[role="dialog"] button', "Cancel");
  await waitFor(page, async () => !(await page.$('[role="dialog"]')), "nothing saved");
  assert.equal((await overrides()).filter((o) => o.scope === "global" && o.check === "filesystem" && o.fields.warning === 98).length, 0);
  assert.deepEqual(page.errors, []);
});

test("Packs that disagree about a host: said plainly on its health checks, settled by Let … win", async () => {
  const nas = ids["nas-01"];
  const p1 = (await api(page, "POST", "/groups", { name: "e2e-rules-a" })).body;
  const p2 = (await api(page, "POST", "/groups", { name: "e2e-rules-b" })).body;
  try {
    for (const p of [p1, p2]) await api(page, "POST", `/groups/${p.id}/members`, { host_ids: [nas] });
    await api(page, "PUT", "/monitoring/health/overrides", { scope: "group", group_id: p1.id, check: "memory", fields: { warning: 70 } });
    await api(page, "PUT", "/monitoring/health/overrides", { scope: "group", group_id: p2.id, check: "memory", fields: { warning: 75 } });
    await page.goto(`${base}/hosts/${nas}/monitoring?tab=health`, { waitUntil: "networkidle0" });
    await page.waitForSelector('[data-testid="pack-conflicts"]');
    const c = await inner('[data-testid="pack-conflicts"]');
    assert.match(c, /Packs disagree about one value on this host/);
    for (const want of ["Memory pressure, warning at:", "e2e-rules-a (70%)", "e2e-rules-b (75%)", "both monitoring priority 0. Meanwhile 90% from Recommended applies."]) {
      assert.ok(c.includes(want), `the conflict says ${want}: ${c}`);
    }
    await shot(page, "rules-pack-conflict");
    await click(page, '[data-testid="pack-conflicts"] button', "Let e2e-rules-b win…");
    await waitText(page, "Let e2e-rules-b win over e2e-rules-a?");
    assert.match(await inner('[role="dialog"]'), /monitoring priority becomes 1, above e2e-rules-a \(0\)/);
    await click(page, '[role="dialog"] button', "Set e2e-rules-b's monitoring priority to 1");
    await waitFor(page, async () => !(await page.$('[data-testid="pack-conflicts"]')), "settled");
    const row = await page.$$eval("tbody tr", (rs) => rs.map((r) => r.innerText).find((t) => t.startsWith("Memory pressure")));
    assert.match(row, /warning 75%/);
    assert.match(row, /Pack: e2e-rules-b/);
    assert.deepEqual(page.errors, []);
  } finally {
    await cleanup((o) => o.group_id === p1.id || o.group_id === p2.id);
    for (const p of [p1, p2]) await api(page, "DELETE", `/groups/${p.id}`);
  }
});
