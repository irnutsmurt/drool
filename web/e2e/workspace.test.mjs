// The host workspace (1.0 UI/UX correction): every page about one host
// shares its header (name, state, how DROOL reaches it, Run command,
// Install updates, Actions) and its local navigation; the sidebar keeps
// Hosts highlighted; old addresses keep working; the Actions menu works
// from the keyboard; on a phone the sections are a selector. Needs
// test/integration TestBrowser's environment (web-1; fixture host nas-01).
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;
let ids = {};

before(async () => {
  browser = await launch();
  page = await session(browser);
  ids = Object.fromEntries((await api(page, "GET", "/hosts")).body.items.map((h) => [h.name, h.id]));
});
after(async () => browser?.close());

const go = async (p, path) => {
  await p.goto(base + path, { waitUntil: "domcontentloaded" });
  await p.waitForSelector('[data-testid="host-workspace"], main h1');
  await sleep(300);
};

/** What the page says about where it is. */
const where = (p) => p.evaluate(() => ({
  host: document.querySelector('[data-testid="host-workspace"]')?.getAttribute("data-host-id"),
  h1: document.querySelector("main h1")?.textContent?.trim(),
  crumbs: [...document.querySelectorAll('main nav[aria-label="Breadcrumb"] li')].map((li) => li.textContent.replace("›", "").trim()),
  sidebar: [...document.querySelectorAll('nav[aria-label="Main"] a[aria-current="page"]')].map((a) => a.textContent.trim()),
  section: document.querySelector('[data-testid="host-sections"] a[aria-current="page"]')?.textContent?.trim(),
}));

test("every section keeps the host, its header and Hosts in the sidebar", async () => {
  const id = ids["web-1"];
  const sections = [
    ["", "Overview"], ["/monitoring", "Monitoring"], ["/files", "Files"], ["/network", "Network"], ["/firewall", "Firewall"],
    ["/desired", "Desired State"], ["/management", "Management"], ["/activity", "Activity"],
  ];
  for (const [path, label] of sections) {
    await go(page, `/hosts/${id}${path}`);
    await waitFor(page, async () => (await where(page)).section === label, `${label} selected`);
    const w = await where(page);
    assert.equal(w.host, id, path);
    assert.match(w.h1, /^web-1/, `${path}: the host's name is the page title`);
    assert.deepEqual(w.sidebar, ["Hosts"], `${path}: only Hosts is highlighted (not Firewall, Monitoring…): ${w.sidebar}`);
    assert.deepEqual(w.crumbs, label === "Overview" ? ["Hosts", "web-1"] : ["Hosts", "web-1", label], path);
    // The section's own title is a level below the host's.
    if (label !== "Overview") assert.ok(await page.$eval("main h2", (h) => h.textContent.length > 0), `${path}: section heading`);
  }
  // Moving between sections by the local navigation stays on this host.
  await go(page, `/hosts/${id}`);
  await click(page, '[data-testid="host-sections"] a', "Firewall");
  await waitFor(page, async () => page.url().endsWith(`/hosts/${id}/firewall`), "the firewall section");
  await click(page, '[data-testid="host-sections"] a', "Activity");
  await waitFor(page, async () => page.url().endsWith(`/hosts/${id}/activity`), "the activity section");
  await page.waitForSelector('[data-testid="runs-table"]');
  await shot(page, "workspace-activity");
  assert.deepEqual(page.errors, []);
});

test("old addresses still open the right place", async () => {
  const id = ids["nas-01"];
  await go(page, `/hosts/${id}/monitoring?tab=services`);
  await waitFor(page, async () => (await page.$$eval('[role="tab"][aria-selected="true"]', (t) => t.map((x) => x.textContent)))[0] === "Services", "the Services tab");
  assert.equal((await where(page)).section, "Monitoring");
  await go(page, `/hosts/${ids["web-1"]}/files?path=/etc`);
  await waitText(page, "hostname");
  assert.equal((await where(page)).section, "Files");
  // The host page's old #management anchor lands on Management.
  await page.goto(`${base}/hosts/${ids["web-1"]}#management`, { waitUntil: "domcontentloaded" });
  await waitFor(page, async () => page.url().endsWith(`/hosts/${ids["web-1"]}/management`), "the Management section");
  // An unknown host says so inside the workspace frame, not a crash.
  await go(page, "/hosts/host_does_not_exist");
  await waitText(page, "Couldn't load the host");
  assert.deepEqual(page.errors, []);
});

test("the Actions menu works from the keyboard and opens focused dialogs", async () => {
  await go(page, `/hosts/${ids["web-1"]}`);
  const button = await page.waitForSelector('[data-testid="host-actions"]');
  await button.focus();
  await page.keyboard.press("Enter");
  await page.waitForSelector('[role="menu"]');
  const focused = () => page.evaluate(() => document.activeElement?.textContent?.trim());
  assert.match(await focused(), /^Reboot…/, "the first item has focus");
  await page.keyboard.press("ArrowDown");
  assert.match(await focused(), /^Refresh inventory/);
  await page.keyboard.press("End");
  assert.match(await focused(), /^Remove host…/);
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[role="menu"]')), "menu closed");
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("data-testid")), "host-actions", "focus back on the button");
  // Test connection: a dialog with the steps, nothing else changes.
  await page.keyboard.press("Enter");
  await page.waitForSelector('[role="menu"]');
  await click(page, '[role="menuitem"]', "Test connection");
  await page.waitForSelector('[data-testid="connection-test"]');
  await waitFor(page, async () => /✓/.test(await page.$eval('[data-testid="connection-test"]', (d) => d.innerText)), "test steps", 60000);
  await shot(page, "workspace-test-connection");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[role="dialog"]')), "dialog closed");
  // Edit host details goes to Management with the form open.
  await click(page, '[data-testid="host-actions"]', "Actions");
  await click(page, '[role="menuitem"]', "Edit host details");
  await waitFor(page, async () => page.url().includes("/management?edit=1"), "the edit form");
  await waitText(page, "Edit host");
  await click(page, "main button", "Cancel");
  await waitFor(page, async () => !page.url().includes("edit=1"), "edit closed");
  assert.deepEqual(page.errors, []);
});

test("on a phone the sections are a selector and the header stays compact", async () => {
  const phone = await session(browser, { width: 390, height: 844, mobile: true });
  try {
    const id = ids["web-1"];
    await go(phone, `/hosts/${id}`);
    assert.equal(await phone.$eval('[data-testid="host-sections"]', (n) => getComputedStyle(n).display), "none", "no tab row on a phone");
    const select = await phone.waitForSelector('[data-testid="host-section-select"] select');
    assert.deepEqual(await select.evaluate((s) => [...s.options].map((o) => o.textContent)),
      ["Overview", "Monitoring", "Files", "Network", "Firewall", "Desired State", "Management", "Activity"]);
    await select.select("firewall");
    await waitFor(phone, async () => phone.url().endsWith(`/hosts/${id}/firewall`), "the firewall section from the selector");
    // Primary action and Actions only; Install updates is in the menu.
    const visible = await phone.$$eval('[data-testid="host-header"] button', (bs) => bs.filter((b) => b.offsetParent !== null).map((b) => b.textContent.trim()));
    assert.ok(!visible.includes("Install updates"), `header buttons on a phone: ${visible}`);
    await click(phone, '[data-testid="host-actions"]', "Actions");
    const items = await phone.$$eval('[role="menuitem"]', (xs) => xs.filter((x) => x.offsetParent !== null).map((x) => x.textContent));
    assert.ok(items.some((x) => x.startsWith("Install updates")), `menu on a phone: ${items}`);
    const box = await phone.$eval('[role="menu"]', (m) => { const r = m.getBoundingClientRect(); return [r.left, r.right]; });
    assert.ok(box[0] >= 0 && box[1] <= 390, `menu inside the screen: ${box}`);
    await phone.keyboard.press("Escape");
    const w = await phone.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
    assert.ok(w[0] <= w[1] + 1, `no sideways scrolling: ${w}`);
    await shot(phone, "workspace-phone");
    assert.deepEqual(phone.errors, []);
  } finally {
    await phone.close();
  }
});
