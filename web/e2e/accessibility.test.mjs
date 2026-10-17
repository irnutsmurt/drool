// The final 1.0 usability and accessibility pass: a host's states are
// named so none reads as another, every page has a title and a navigation
// is announced, tab rows and menus work from the keyboard, focus follows an
// inspector's steps and comes back from popovers, a table that doesn't fit
// becomes cards (no column or action behind a sideways scroll), touch
// screens get 44 px targets, and the firewall's "Can A reach B?" says it
// is a simulation. Needs test/integration TestBrowser's environment (web-1,
// the fixture hosts nas-01 and gpu-01, the synthetic connections).
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
  await p.waitForSelector("main h1", { timeout: 20000 });
  await sleep(400);
};
const active = (p) => p.evaluate(() => {
  const el = document.activeElement;
  return { tag: el?.tagName.toLowerCase(), name: (el?.getAttribute("aria-label") || el?.innerText || "").trim(), role: el?.getAttribute("role") ?? "",
    inInspector: !!el?.closest("[data-inspector]"), testid: el?.getAttribute("data-testid") ?? "" };
});

test("a host's header names each state; the list's Health is monitoring's verdict", async () => {
  // The monitoring fixture arrives shortly after the hosts exist.
  await waitFor(page, async () => (await api(page, "GET", "/monitoring/summary")).body?.counts?.storage > 0, "monitoring fixture", 90000);
  await go(page, `/hosts/${ids["nas-01"]}`);
  await waitFor(page, async () => !!(await page.$('[data-testid="host-health"]')), "the header's Health");
  const h = await page.evaluate(() => ({
    h1: document.querySelector("main h1").textContent.trim(),
    state: document.querySelector('[data-testid="host-state"]').innerText,
    connection: document.querySelector('[data-testid="host-connection"]').innerText,
    health: document.querySelector('[data-testid="host-health"]').innerText,
  }));
  // The name stands alone: DROOL's connection is no longer a badge beside it.
  assert.equal(h.h1, "nas-01");
  assert.match(h.connection, /^Connection\s/, h.connection);
  assert.match(h.state, /Last check/);
  // nas-01's disk is failing: a problem, in words (not only an icon or a colour).
  assert.match(h.health, /^Health\s+[✕⚠]?\s*(Unhealthy|Degraded) · \d+ issues?$/, h.health);
  await shot(page, "a11y-host-header");

  await go(page, "/hosts");
  await waitFor(page, async () => (await page.$$("table.rtable tbody tr")).length > 3, "hosts");
  const headers = await page.$$eval("table.rtable thead th", (ths) => ths.map((t) => t.textContent.trim()));
  assert.ok(headers.includes("Health") && headers.includes("Connection") && !headers.includes("Last check"), headers.join(","));
  await waitFor(page, async () => /Unhealthy|Degraded/.test(await page.$eval("table.rtable", (t) => t.innerText)), "health in the list");
  const nas = await page.evaluate((id) => [...document.querySelectorAll("table.rtable tbody tr")].find((tr) => tr.querySelector(`a[href="/hosts/${id}"]`))
    ?.querySelector('td[data-label="Health"]')?.innerText.trim(), ids["nas-01"]);
  assert.match(nas, /(Unhealthy|Degraded) · \d+ issues?/, `nas-01's Health: ${nas}`);
  assert.ok(!/^(online|offline|not checked yet)$/.test(nas), "Health is never the last check's reachability");
  assert.deepEqual(page.errors, []);
});

test("every page has a title, and moving inside DROOL is announced", async () => {
  // The heading, and the view picked in the page's own navigation.
  await go(page, "/monitoring");
  await waitFor(page, async () => (await page.title()) === "Fleet health – Monitoring – DROOL", "the Monitoring title");
  const web = ids["web-1"];
  await go(page, `/hosts/${web}`);
  await waitFor(page, async () => (await page.title()) === "web-1 – DROOL", "the host's title");
  // By keyboard, from the workspace's own navigation: focus stays on the
  // link, the title and the announcement name the section and the host.
  await page.focus('[data-testid="host-sections"] a[href$="/monitoring"]');
  await page.keyboard.press("Enter");
  await waitFor(page, async () => (await page.title()) === "Monitoring – web-1 – DROOL", "the section's title");
  await waitFor(page, async () => (await page.$eval('[data-testid="page-announcer"]', (d) => d.textContent)) === "Monitoring – web-1", "announced");
  const a = await active(page);
  assert.equal(a.name, "Monitoring", "focus stays on the section link");
  // A page whose views are tabs: its heading.
  await go(page, "/firewall");
  await waitFor(page, async () => (await page.title()).endsWith("Firewall – DROOL"), "the Firewall title");
  assert.deepEqual(page.errors, []);
});

test("tab rows and menus work from the keyboard", async () => {
  await go(page, "/firewall");
  const tab = '[role="tablist"][aria-label="Firewall sections"] [role="tab"]';
  await waitFor(page, async () => (await page.$$(tab)).length === 5, "the sections");
  // One Tab stop for the row; the arrows move and show.
  assert.deepEqual(await page.$$eval(tab, (ts) => ts.map((t) => t.tabIndex)), [0, -1, -1, -1, -1]);
  await page.focus(`${tab}[aria-selected="true"]`);
  await page.keyboard.press("ArrowRight");
  await waitFor(page, async () => page.url().includes("tab=designer"), "→ shows Policy");
  assert.equal((await active(page)).name, "Policy");
  await page.keyboard.press("End");
  await waitFor(page, async () => page.url().includes("tab=settings"), "End shows Settings");
  const panel = await page.$eval('[role="tabpanel"]', (p) => document.getElementById(p.getAttribute("aria-labelledby"))?.textContent);
  assert.equal(panel, "Settings", "the panel is named by its tab");

  // A menu is named by its button.
  await go(page, `/hosts/${ids["web-1"]}`);
  await page.focus('[data-testid="host-actions"]');
  await page.keyboard.press("Enter");
  await page.waitForSelector('[role="menu"]');
  const name = await page.$eval('[role="menu"]', (m) => document.getElementById(m.getAttribute("aria-labelledby"))?.textContent.trim());
  assert.equal(name, "Actions");
  assert.equal((await active(page)).role, "menuitem");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => (await active(page)).testid === "host-actions", "focus back on Actions");
  assert.deepEqual(page.errors, []);
});

test("focus follows an inspector's step and comes back from a popover", async () => {
  await go(page, "/network");
  await page.waitForSelector('[data-testid="flow-table"] tbody tr[data-flow-id] button', { timeout: 20000 });
  const row = await page.$('[data-testid="flow-table"] tbody tr[data-flow-id] button');
  const opener = await row.evaluate((b) => b.getAttribute("aria-label"));
  await row.focus();
  await page.keyboard.press("Enter");
  await page.waitForSelector('[data-testid="flow-inspector"]');
  await waitFor(page, async () => (await active(page)).inInspector, "focus in the inspector");
  // A step (Classify) replaces the content: focus stays inside, and Back
  // (Escape) returns it to the button that opened the step.
  const step = await page.evaluate(() => {
    const b = [...document.querySelectorAll("[data-inspector] button")].find((x) => /^Classify/.test(x.innerText.trim()));
    b?.focus();
    return b ? (b.getAttribute("aria-label") || b.innerText).trim() : null;
  });
  assert.ok(step, "a Classify step");
  await page.keyboard.press("Enter");
  await waitText(page, "Back to the connection");
  await waitFor(page, async () => (await active(page)).inInspector, "focus inside the step");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => (await active(page)).name === step, `focus back on ${step}`);
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[data-testid="flow-inspector"]')), "closed");
  assert.equal((await active(page)).name, opener, "focus back on the connection's row");

  // The security indicator's popover: Escape from inside returns to its button.
  if (await page.$('[data-testid="security-status"]')) {
    await page.focus('[data-testid="security-status"]');
    await page.keyboard.press("Enter");
    await page.waitForSelector('[role="region"][aria-label="Security status"]');
    await page.evaluate(() => document.querySelector('[role="region"][aria-label="Security status"] a')?.focus());
    await page.keyboard.press("Escape");
    await waitFor(page, async () => (await active(page)).testid === "security-status", "focus back on the indicator");
  }
  assert.deepEqual(page.errors, []);
});

test("at 1024 px tables that don't fit become cards: nothing behind a sideways scroll, no Tab stop out of sight", async () => {
  const p = await session(browser, { width: 1024, height: 900 });
  try {
    const web = ids["web-1"];
    for (const path of ["/hosts", "/monitoring", "/network", "/audit", "/alerts?view=all", `/hosts/${web}/files?path=/etc`, `/hosts/${web}/monitoring?tab=services`, "/jobs/schedules"]) {
      await go(p, path);
      await sleep(1200);
      const r = await p.evaluate(() => ({
        page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        sideways: [...document.querySelectorAll("main *")].filter((e) => {
          const ox = getComputedStyle(e).overflowX;
          return (ox === "auto" || ox === "scroll") && e.clientWidth > 0 && e.scrollWidth > e.clientWidth + 2 && e.querySelector("table");
        }).map((e) => `${e.clientWidth}/${e.scrollWidth}: ${(e.innerText || "").slice(0, 40).replace(/\s+/g, " ")}`),
        // A control inside a box hidden for the cards (the table header) is a
        // Tab stop nobody can see. A hidden radio or checkbox is seen through
        // its label.
        unseen: [...document.querySelectorAll("main :is(a[href], button, input, select, textarea, [tabindex]:not([tabindex='-1']))")].filter((el) => {
          if (el.disabled || !el.checkVisibility()) return false;
          for (let e = el.type === "checkbox" || el.type === "radio" ? el.closest("label") ?? el : el; e && e.tagName !== "MAIN"; e = e.parentElement) {
            if (getComputedStyle(e).clipPath === "inset(50%)") return true;
          }
          return false;
        }).map((el) => el.getAttribute("aria-label") || el.innerText.trim()),
      }));
      assert.ok(r.page <= 1, `${path}: the page scrolls sideways`);
      assert.deepEqual(r.sideways, [], `${path}: a table scrolls sideways at 1024 px`);
      assert.deepEqual(r.unseen, [], `${path}: a Tab stop nobody can see`);
    }
    // Monitoring's Select all stays in sight, named, above its cards.
    await go(p, "/monitoring");
    await p.waitForSelector('input[aria-label="Select all shown hosts"]');
    await sleep(800);
    const all = await p.$eval('input[aria-label="Select all shown hosts"]', (e) => {
      const r = e.closest("label").getBoundingClientRect();
      return { cards: !!e.closest("table.rtable[data-cards]"), w: Math.round(r.width), h: Math.round(r.height), text: e.closest("label").innerText.trim() };
    });
    if (all.cards) assert.ok(all.w > 20 && all.h > 10 && all.text === "Select all shown", JSON.stringify(all));
    // The files list is cards here, each with its actions.
    await go(p, `/hosts/${web}/files?path=/etc`);
    await waitFor(p, async () => (await p.$$("table.rtable[data-cards] tbody tr")).length > 0, "file cards");
    assert.ok(await p.$('table.rtable[data-cards] button[aria-label^="More for"]'), "a card's ⋯ menu");
    await shot(p, "a11y-1024-files");
    // The same table at 1440 px stays a table.
    await p.setViewport({ width: 1440, height: 900 });
    await waitFor(p, async () => (await p.$$("table.rtable:not([data-cards]) tbody tr")).length > 0, "a table again at 1440 px");
    assert.deepEqual(p.errors, []);
  } finally {
    await p.close();
  }
});

test("touch screens: the controls a finger taps are at least 44 px", async () => {
  const p = await session(browser, { width: 390, height: 844, mobile: true });
  try {
    const web = ids["web-1"];
    await go(p, `/hosts/${web}`);
    const size = (sel) => p.$eval(sel, (e) => { const r = e.getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; });
    for (const sel of ['button[aria-label="Open navigation"]', '[data-testid="search-button"]', '[data-testid="host-actions"]', '[data-testid="host-section-select"] select']) {
      const [w, h] = await size(sel);
      assert.ok(w >= 44 && h >= 44, `${sel}: ${w}×${h}`);
    }
    const [, h] = await size('[data-testid="host-header"] button.bg-accent');
    assert.ok(h >= 44, `Run command: ${h} px tall`);
    // A dialog's close button too.
    await click(p, '[data-testid="host-actions"]', "Actions");
    await click(p, '[role="menuitem"]', "Start maintenance");
    await p.waitForSelector('[role="dialog"]');
    const [cw, ch] = await size('[role="dialog"] button[aria-label="Close dialog"]');
    assert.ok(cw >= 44 && ch >= 44, `close: ${cw}×${ch}`);
    await shot(p, "a11y-touch-dialog");
    await p.keyboard.press("Escape");
    assert.deepEqual(p.errors, []);
  } finally {
    await p.close();
  }
});

test("touch screens: a link-styled button among controls, icon-only buttons in cards, checkboxes through their label", async (t) => {
  const p = await session(browser, { width: 390, height: 844, mobile: true });
  // A checkbox is tapped through its label.
  const sizes = (sel) => p.$$eval(sel, (els) => els.map((e) => {
    const r = (e.type === "checkbox" ? e.closest("label") ?? e : e).getBoundingClientRect();
    return { name: e.getAttribute("aria-label") || e.innerText.trim(), w: Math.round(r.width), h: Math.round(r.height) };
  }));
  const cur = await api(p, "GET", "/firewall/document");
  let restore;
  try {
    await go(p, "/monitoring");
    await p.waitForSelector('button[aria-controls="fleet-more-filters"]');
    const [more] = await sizes('button[aria-controls="fleet-more-filters"]');
    assert.ok(more.h >= 44, `More filters: ${more.h} px tall`);
    await waitFor(p, async () => (await p.$$('main input[type="checkbox"][aria-label^="Select "]')).length > 0, "the fleet's checkboxes");
    for (const c of await sizes('main input[type="checkbox"][aria-label^="Select "]')) assert.ok(c.w >= 44 && c.h >= 44, `${c.name}: ${c.w}×${c.h}`);

    // The rules list is cards on a phone; the move arrows keep 44 × 44 there.
    if ((cur.body.document.policy ?? []).length === 0) {
      const r = await api(p, "PUT", "/firewall/document", { yaml: "policy:\n  - name: e2e-touch\n    from: internet\n    to: web-1\n    allow: [https]\n", revision: cur.body.revision });
      assert.equal(r.status, 200, JSON.stringify(r.body));
      restore = r.body.revision;
    }
    await go(p, "/firewall?tab=designer&view=rules");
    await p.waitForSelector('table.rtable[data-cards] button[aria-label^="Move "]');
    for (const b of await sizes('table.rtable[data-cards] button[aria-label^="Move "]')) assert.ok(b.w >= 44 && b.h >= 44, `${b.name}: ${b.w}×${b.h}`);

    // The firewall's hosts: a checkbox for each host in enforce mode.
    await go(p, "/firewall");
    const boxes = await sizes('main input[type="checkbox"][aria-label^="Select "]');
    if (boxes.length === 0) t.diagnostic("no host in enforce mode: the firewall's checkboxes weren't measured");
    for (const c of boxes) assert.ok(c.w >= 44 && c.h >= 44, `${c.name}: ${c.w}×${c.h}`);
    assert.deepEqual(p.errors, []);
  } finally {
    if (restore) await api(p, "PUT", "/firewall/document", { yaml: cur.body.yaml, revision: restore });
    await p.close();
  }
});

test("Can A reach B? says it is a simulation", async () => {
  await go(page, "/firewall?tab=simulate");
  const t = await page.$eval('[data-testid="simulate-explainer"]', (p) => p.textContent.replace(/\s+/g, " "));
  assert.match(t, /A simulation: DROOL works out the answer from the firewall policy it wants for these hosts\. It sends nothing to the hosts, tries no connection, and changes no policy\./);
  // From web-1 to a documentation address.
  await page.select("main select", ids["web-1"]);
  const to = await page.$('main input[placeholder="203.0.113.7"]');
  await to.type("203.0.113.7");
  await click(page, "main button", "Ask");
  await waitFor(page, async () => /\(simulated\)/.test(await text(page)), "the answer marked simulated", 20000);
  assert.match(await text(page), /(YES|NO|PARTLY)\s.*the policy (allows|blocks)|PARTLY/s);
  assert.deepEqual(page.errors, []);
});
