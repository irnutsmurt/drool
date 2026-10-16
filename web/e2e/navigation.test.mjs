// M15 navigation and shell: the direct-destination sidebar with its
// headings and themed names (Packs = groups, Shepherds = DROOL agents),
// the navigation search (button, Ctrl+K, "/", aliases, keyboard, focus),
// what a read-only role sees (hidden destinations, 403 on direct links,
// read-only pages), the narrow-screen shell (menu, card tables, dialogs,
// no sideways scrolling) in both themes, cross-links between related
// pages, and honest loading states. Needs test/integration TestBrowser's
// environment (web-1, jump-a, server-a…c; fixture hosts nas-01, gpu-01).
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let admin;
const run = Date.now().toString(36).slice(-5);
const viewer = { user: `e2e-viewer-${run}`, password: "viewer-password-long" };
let viewerId;
let ids = {};

before(async () => {
  browser = await launch();
  admin = await session(browser);
  ids = Object.fromEntries((await api(admin, "GET", "/hosts")).body.items.map((h) => [h.name, h.id]));
  const u = await api(admin, "POST", "/users", { username: viewer.user, password: viewer.password, role: "viewer" });
  assert.equal(u.status, 201, JSON.stringify(u.body));
  viewerId = u.body.id;
});
after(async () => {
  if (admin && viewerId) await api(admin, "DELETE", `/users/${viewerId}`).catch(() => {});
  await browser?.close();
});

const go = async (page, path) => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await sleep(400);
};
/** The sidebar: [heading, [labels]] per section (labels without hints or counts). */
const sidebar = (page) => page.$$eval('nav[aria-label="Main"] > div', (secs) => secs.map((s) => [
  s.querySelector("div[id^=nav-]")?.textContent ?? "",
  [...s.querySelectorAll("a")].map((a) => a.querySelector("span")?.textContent.trim() ?? ""),
]));
const paletteOpen = (page) => page.$('[data-testid="command-palette"]').then(Boolean);
async function ctrlK(page) {
  await page.keyboard.down("Control");
  await page.keyboard.press("k");
  await page.keyboard.up("Control");
}
/** Opens the search, types `q`, and returns the titles offered (in order). */
async function search(page, q) {
  if (!(await paletteOpen(page))) await ctrlK(page);
  await waitFor(page, () => paletteOpen(page), "search open");
  const input = await page.$('[data-testid="command-palette"] input[role="combobox"]');
  // Clear it (a triple click doesn't select an input's text in puppeteer).
  await input.focus();
  await page.keyboard.down("Control");
  await page.keyboard.press("a");
  await page.keyboard.up("Control");
  await page.keyboard.press("Backspace");
  if (q) await page.keyboard.type(q);
  await sleep(250);
  // Hosts, services, alerts and runs come from the server as typing pauses.
  await waitFor(page, async () => (await page.$eval('[data-testid="command-palette"]', (d) => d.getAttribute("data-searching"))) === "false", "search settled");
  return page.$$eval('[role="listbox"] [role="option"]', (os) => os.map((o) => o.getAttribute("data-to")));
}

test("sidebar: direct destinations under headings; themed names explain themselves", async () => {
  const page = admin;
  await go(page, "/");
  // CSS uppercases the headings; the text stays readable for screen readers.
  assert.deepEqual(await sidebar(page), [
    ["", ["Overview"]],
    ["Fleet", ["Hosts", "Packs", "Shepherds"]],
    ["Observe", ["Monitoring", "Alerts", "Network Activity"]],
    ["Automate", ["Runs", "Schedules", "Workflows", "Desired State"]],
    ["Protect", ["Firewall", "Credentials", "Audit Log"]],
    // One destination, set apart: no "Settings" heading over a "Settings" link.
    ["", ["Settings"]],
  ]);
  // Headings are labels, not links.
  assert.equal(await page.$('nav[aria-label="Main"] div[id^=nav-] a'), null);
  // The conventional term is visible next to the themed one (no hover
  // needed) and the description is announced and shown on keyboard focus.
  const packs = await page.$('nav[aria-label="Main"] a[href="/groups"]');
  assert.match(await packs.evaluate((a) => a.innerText), /Packs\s*Groups/);
  const desc = await packs.evaluate((a) => document.getElementById(a.getAttribute("aria-describedby"))?.textContent);
  assert.equal(desc, "Groups of hosts used for targeting and inherited configuration.");
  await tabTo(page, () => document.activeElement?.getAttribute("href") === "/agents", "Shepherds link");
  await waitFor(page, async () => /Shepherds: DROOL agents installed on managed hosts\./.test(await page.$eval('[data-testid="nav-tip"]', (t) => t.textContent).catch(() => "")), "tooltip on focus");
  await shot(page, "nav-tooltip-focus");
  await page.keyboard.press("Enter");
  // The page loads on demand; until it has, the previous one stays (and stays marked).
  await waitFor(page, async () => page.url().endsWith("/agents") && (await page.$eval("main h1", (h) => h.textContent).catch(() => "")) === "Shepherds", "Shepherds page");
  assert.match(await page.$eval("main", (m) => m.innerText), /DROOL agents installed on managed hosts/);
  await waitFor(page, async () => (await page.$eval('nav[aria-label="Main"] a[aria-current="page"]', (a) => a.getAttribute("href"))) === "/agents", "Shepherds marked in the sidebar");
  await go(page, "/groups");
  assert.match(await page.$eval("main", (m) => m.innerText), /Groups of hosts used for targeting and inherited configuration/);
  // Nested pages mark the right destination.
  for (const [path, marked] of [[`/hosts/${ids["web-1"]}/firewall`, "/hosts"], ["/jobs/new", "/jobs"], ["/jobs/workflows/new", "/jobs/workflows"], ["/monitoring/settings", "/monitoring"], ["/settings/tokens", "/settings"]]) {
    await go(page, path);
    assert.deepEqual(await page.$$eval('nav[aria-label="Main"] a[aria-current="page"]', (as) => as.map((a) => a.getAttribute("href"))), [marked], path);
  }
  assert.deepEqual(page.errors, []);
});

test("navigation search: button, Ctrl+K and '/', aliases, keyboard, Escape returns focus", async () => {
  const page = admin;
  await go(page, `/hosts/${ids["web-1"]}`);
  // The visible control.
  await page.click('[data-testid="search-button"]');
  await waitFor(page, () => paletteOpen(page), "opened by the button");
  const input = await page.$('[data-testid="command-palette"] input');
  assert.equal(await input.evaluate((i) => i === document.activeElement), true, "focus in the search field");
  assert.equal(await input.evaluate((i) => i.getAttribute("role")), "combobox");
  assert.equal(await page.$eval('[data-testid="command-palette"]', (d) => d.getAttribute("aria-modal")), "true");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await paletteOpen(page)), "closed");
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("data-testid")), "search-button", "focus returns to the button");
  // "/" opens it — but not while typing in a field.
  await page.keyboard.press("/");
  await waitFor(page, () => paletteOpen(page), "opened by /");
  await page.keyboard.press("Escape");
  await go(page, "/hosts");
  await page.focus('input[placeholder^="Search name"]');
  await page.keyboard.type("/web");
  assert.equal(await paletteOpen(page), false, "'/' types into a field");
  assert.equal(await page.$eval('input[placeholder^="Search name"]', (i) => i.value), "/web");
  await page.$eval('input[placeholder^="Search name"]', (i) => i.blur());
  // Conventional words find themed destinations (the top result).
  const cases = [
    ["dashboard", "/"], ["server", "/hosts"], ["group", "/groups"], ["agent", "/agents"], ["metrics", "/monitoring"],
    ["threshold", "/monitoring/settings"], ["downtime", "/alerts?view=maintenance"], ["traffic", "/network"], ["job", "/jobs"],
    ["cron", "/jobs/schedules"], ["workflow", "/jobs/workflows"], ["drift", "/desired"], ["can A reach B", "/firewall?tab=simulate"],
    ["ssh key", "/credentials"], ["api key", "/settings/tokens"], ["discord", "/settings/notifications"], ["who changed", "/audit"],
  ];
  for (const [q, to] of cases) {
    const got = await search(page, q);
    assert.equal(got[0], to, `"${q}" → ${got.join(", ")}`);
  }
  await shot(page, "palette-who-changed");
  // Arrow keys move the selection (announced through aria-activedescendant); Enter opens.
  const offered = await search(page, "settings");
  assert.ok(offered.length > 2, "several results");
  const active = () => page.$eval('[data-testid="command-palette"] input', (i) => document.getElementById(i.getAttribute("aria-activedescendant"))?.getAttribute("data-to"));
  assert.equal(await active(), offered[0]);
  await page.keyboard.press("ArrowDown");
  assert.equal(await active(), offered[1]);
  assert.equal(await page.$eval('[role="option"][aria-selected="true"]', (o) => o.getAttribute("data-to")), offered[1]);
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => page.url().endsWith(offered[0]), "opened " + offered[0]);
  assert.equal(await paletteOpen(page), false);
  // Deep links land where the thing is.
  await search(page, "reachability");
  await page.keyboard.press("Enter");
  await waitText(page, "Can A reach B?");
  assert.match(page.url(), /tab=simulate/);
  // Nothing matches: said so.
  await search(page, "zzzz-nothing");
  assert.match(await page.$eval('[data-testid="command-palette"]', (d) => d.innerText), /Nothing matches/);
  await page.keyboard.press("Escape");
  assert.deepEqual(page.errors, []);
});

test("search finds hosts, services, alerts and runs, grouped, and opens where they are", async () => {
  const page = admin;
  const web = (await api(page, "GET", `/hosts/${ids["web-1"]}`)).body;
  const run = await api(page, "POST", "/jobs", { name: `e2e search ${Date.now().toString(36)}`, steps: [{ type: "command", command: "true", read_only: true }],
    targets: { hosts: [web.id] } });
  assert.equal(run.status, 201, JSON.stringify(run.body));
  await go(page, "/");
  const groups = () => page.$$eval('[data-testid="command-palette"] [role="group"]', (gs) => gs.map((g) => g.firstElementChild?.textContent));
  // A host by name: the host first, its workspace on Enter.
  let offered = await search(page, "web-1");
  assert.equal(offered[0], `/hosts/${web.id}`);
  assert.equal((await groups())[0], "Hosts");
  // …and by its address, saying why it matched.
  offered = await search(page, web.address);
  assert.ok(offered.includes(`/hosts/${web.id}`), `address ${web.address}: ${offered}`);
  assert.match(await page.$eval('[data-testid="command-palette"]', (d) => d.innerText), new RegExp(`matches address ${web.address.replaceAll(".", "\\.")}`));
  // A service, with its host, opens that host's Services filtered to it.
  offered = await search(page, "jellyfin");
  const svc = offered.find((t) => t.includes("/monitoring?tab=services&unit=jellyfin.service"));
  assert.ok(svc, `jellyfin.service: ${offered}`);
  assert.match(await page.$eval('[data-testid="command-palette"]', (d) => d.innerText), /jellyfin\.service\s*— nas-01/);
  // Open alerts by words of their title and category.
  offered = await search(page, "disk full");
  assert.ok(offered.some((t) => t.startsWith("/alerts/alr_")), `disk full: ${offered}`);
  assert.ok((await groups()).includes("Active alerts"));
  // Runs by name.
  offered = await search(page, run.body.name);
  assert.ok(offered.includes(`/jobs/${run.body.id}`), `run: ${offered}`);
  // Pages still win when the words name one ("cron" is Schedules).
  offered = await search(page, "cron");
  assert.equal(offered[0], "/jobs/schedules");
  // Open the service result: the host's Services, found and highlighted.
  await search(page, "jellyfin");
  const i = (await page.$$eval('[role="listbox"] [role="option"]', (os) => os.map((o) => o.getAttribute("data-to")))).indexOf(svc);
  for (let k = 0; k < i; k++) await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => page.url().includes("tab=services") && page.url().includes("unit=jellyfin.service"), "the service's host");
  await waitFor(page, async () => (await page.$eval('input[aria-label="Find a service"]', (x) => x.value).catch(() => "")) === "jellyfin.service", "found");
  const rows = await page.$$eval("table tbody tr", (r) => r.map((x) => x.innerText));
  assert.equal(rows.length, 1, `only that service: ${rows}`);
  await shot(page, "search-service");
  assert.deepEqual(page.errors, []);
});

test("a read-only role: no destinations it can't view, a clear 403 on direct links, read-only pages", async () => {
  const page = await session(browser, { as: viewer });
  try {
    await go(page, "/");
    const items = (await sidebar(page)).flatMap(([, i]) => i);
    for (const hidden of ["Credentials", "Audit Log", "Network Activity"]) assert.ok(!items.includes(hidden), `${hidden} hidden: ${items}`);
    for (const shown of ["Hosts", "Packs", "Monitoring", "Runs", "Firewall", "Settings"]) assert.ok(items.includes(shown), `${shown} shown`);
    // Search never offers what the role can't open.
    assert.ok(!(await search(page, "ssh key")).includes("/credentials"));
    assert.deepEqual(await search(page, "who changed"), []);
    assert.ok(!(await search(page, "notification")).includes("/settings/notifications"));
    assert.ok(!(await search(page, "add user")).includes("/settings/users"));
    assert.equal((await search(page, "api token"))[0], "/settings/tokens", "everyone has their own API tokens");
    await page.keyboard.press("Escape");
    // A typed or linked address says why.
    for (const p of ["/credentials", "/audit", "/network", "/settings/users", "/jobs/new"]) {
      await go(page, p);
      const t = await text(page);
      assert.match(t, /Not permitted/, p);
      assert.match(t, /Your role \(viewer\) can't view this page/, p);
    }
    await shot(page, "viewer-403");
    // Viewing without changing: actions are absent, settings read-only and say so.
    await go(page, `/hosts/${ids["web-1"]}`);
    await waitText(page, "Health");
    const buttons = await page.$$eval("main button", (bs) => bs.map((b) => b.textContent.trim()));
    for (const b of ["Run command", "Edit host details", "Reboot…", "Install updates", "Remove host…", "Actions"]) assert.ok(!buttons.includes(b), `no ${b}: ${buttons}`);
    // The workspace offers a viewer only what it may open (no Files, no Network).
    const sections = await page.$$eval('[data-testid="host-sections"] a', (as) => as.map((a) => a.textContent.trim()));
    assert.deepEqual(sections, ["Overview", "Monitoring", "Firewall", "Desired State", "Management", "Activity"]);
    // Credentials the role can't list are named as such, not by internal ID.
    await go(page, `/hosts/${ids["web-1"]}/management`);
    await waitText(page, "Credential");
    const host = await text(page);
    assert.match(host, /a stored credential — your role can't see its name/);
    assert.ok(!/\bsec_[0-9a-z]{20,}/.test(host), "no internal credential IDs");
    await go(page, "/monitoring/settings");
    await waitText(page, "Read-only: you can see these settings");
    assert.equal(await page.$$eval('main input[type="number"]', (xs) => xs.every((x) => x.disabled)), true);
    const settingsButtons = await page.$$eval("main button", (bs) => bs.map((b) => b.textContent.trim()));
    assert.ok(settingsButtons.includes("Show checks") && !settingsButtons.includes("Customize"), `the checks toggle offers no customizing: ${settingsButtons}`);
    await go(page, "/firewall?tab=designer&view=rules");
    await waitText(page, "only administrators can change the firewall policy");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.close();
  }
});

for (const theme of ["light", "dark"]) {
  test(`narrow screen (390×844, ${theme}): menu, card tables, dialogs, no sideways scrolling`, async () => {
    const page = await session(browser, { width: 390, height: 844, mobile: true });
    await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: theme }]);
    await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
    try {
      await go(page, "/");
      // The sidebar becomes a menu; content gets the full width.
      assert.equal(await page.$eval("aside", (a) => getComputedStyle(a).display), "none");
      const main = await page.$eval("main", (m) => m.getBoundingClientRect().width);
      assert.ok(main >= 389, `main is full width (${main})`);
      const toggle = 'button[aria-label="Open navigation"]';
      assert.equal(await page.$eval(toggle, (b) => b.getAttribute("aria-expanded")), "false");
      await page.click(toggle);
      await page.waitForSelector("#mobile-nav");
      assert.equal(await page.$eval("#mobile-nav", (d) => d.getAttribute("aria-modal")), "true");
      assert.equal(await page.evaluate(() => !!document.activeElement?.closest("#mobile-nav")), true, "focus moves into the menu");
      // Tab stays inside the open menu.
      for (let i = 0; i < 25; i++) await page.keyboard.press("Tab");
      assert.equal(await page.evaluate(() => !!document.activeElement?.closest("#mobile-nav")), true, "focus trapped");
      assert.match(await page.$eval("#mobile-nav", (d) => d.innerText), /Packs\s*Groups/);
      await shot(page, `narrow-menu-${theme}`);
      await page.keyboard.press("Escape");
      await waitFor(page, async () => !(await page.$("#mobile-nav")), "Escape closes");
      assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), "Open navigation", "focus returns to the menu button");
      // Choosing a destination navigates and closes the menu.
      await page.click(toggle);
      await page.waitForSelector("#mobile-nav");
      await click(page, "#mobile-nav a", "Hosts");
      await waitFor(page, async () => page.url().endsWith("/hosts") && !(await page.$("#mobile-nav")), "navigated, menu closed");
      // Header controls fit without overlapping.
      const header = await page.$$eval("header button, header a", (els) => els.filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect()).map((r) => [r.left, r.right]));
      header.sort((a, b) => a[0] - b[0]);
      for (let i = 1; i < header.length; i++) assert.ok(header[i][0] >= header[i - 1][1] - 1, `header controls overlap: ${JSON.stringify(header)}`);
      assert.ok(header.every(([, r]) => r <= 390), "header fits");
      // Tables become cards: the header row is hidden, each cell labelled.
      await waitFor(page, async () => (await page.$$("table.rtable tbody tr")).length > 1, "host rows");
      const card = await page.$eval("table.rtable tbody tr td[data-label='Address']", (td) => [getComputedStyle(td, "::before").content, getComputedStyle(td.closest("table").querySelector("thead")).position]);
      assert.deepEqual(card, ['"Address"', "absolute"]);
      // No page scrolls sideways. (The fixture hosts' monitoring data
      // arrives shortly after the hosts exist; their tabs need it.)
      await waitFor(page, async () => (await api(page, "GET", "/monitoring/summary")).body?.counts?.storage > 0, "monitoring fixture", 90000);
      for (const p of ["/", "/hosts", `/hosts/${ids["web-1"]}`, `/hosts/${ids["nas-01"]}/monitoring`,
        ...["metrics", "services", "storage", "hardware"].map((t) => `/hosts/${ids["nas-01"]}/monitoring?tab=${t}`), "/monitoring", "/alerts", "/network", "/jobs", "/jobs/new",
        "/desired", "/firewall", "/groups", "/agents", "/settings/notifications", "/monitoring/settings"]) {
        await go(page, p);
        // A host's monitoring tab is measured with its tables drawn, not while loading.
        if (/tab=(metrics|services|hardware)/.test(p)) await waitFor(page, async () => !!(await page.$("#monpanel table tbody tr")), `${p} tables`);
        const w = await page.evaluate(() => {
          const wide = [...document.querySelectorAll("main *")].filter((el) => el.getBoundingClientRect().right > window.innerWidth + 1 && !el.closest(".overflow-x-auto"))
            .slice(0, 3).map((el) => `${el.tagName.toLowerCase()} "${(el.textContent || "").trim().slice(0, 50)}" right=${Math.round(el.getBoundingClientRect().right)}`);
          return [document.documentElement.scrollWidth, document.querySelector("main").scrollWidth, window.innerWidth, wide.join("; ")];
        });
        assert.ok(w[0] <= w[2] + 1 && w[1] <= w[2] + 1, `${p} scrolls sideways: ${w}`);
      }
      // Dialogs fit and scroll inside.
      await go(page, `/hosts/${ids["nas-01"]}`);
      await click(page, "button", "Start maintenance");
      await page.waitForSelector('[role="dialog"]');
      const box = await page.$eval('[role="dialog"]', (d) => { const r = d.getBoundingClientRect(); return [r.left, r.right, r.top, r.bottom, getComputedStyle(d).overflowY]; });
      assert.ok(box[0] >= 0 && box[1] <= 390 && box[2] >= 0 && box[3] <= 844, `dialog inside the viewport: ${box}`);
      assert.equal(box[4], "auto");
      await shot(page, `narrow-dialog-${theme}`);
      await page.keyboard.press("Escape");
      // Firewall works on a phone: the Rules view opens, Topology is explained.
      await go(page, "/firewall?tab=designer");
      await waitFor(page, async () => (await page.$$eval('[role="group"][aria-label="View the policy as"] button[aria-pressed="true"]', (b) => b.map((x) => x.textContent)))[0] === "Rules", "Rules by default");
      await click(page, '[role="group"][aria-label="View the policy as"] button', "Topology");
      await waitText(page, "works best on a larger screen");
      // The search works here too, from the header's icon button.
      await page.click('[data-testid="search-button"]');
      await waitFor(page, () => paletteOpen(page), "search on a phone");
      const sb = await page.$eval('[data-testid="command-palette"]', (d) => { const r = d.getBoundingClientRect(); return [r.left, r.right]; });
      assert.ok(sb[0] >= 0 && sb[1] <= 390, `search fits: ${sb}`);
      await page.keyboard.press("Escape");
      assert.deepEqual(page.errors, []);
    } finally {
      await page.close();
    }
  });
}

test("related pages link to each other", async () => {
  const page = admin;
  const id = ids["web-1"];
  // A host summarizes each area and links to its section of the workspace.
  await go(page, `/hosts/${id}`);
  await waitText(page, "Health now");
  const links = await page.$$eval("main a", (as) => as.map((a) => a.getAttribute("href")));
  for (const want of [`/hosts/${id}/monitoring`, `/hosts/${id}/desired`, `/hosts/${id}/firewall`, `/hosts/${id}/network`, `/hosts/${id}/activity`,
    `/hosts/${id}/files`, `/hosts/${id}/management`]) {
    assert.ok(links.includes(want), `host links to ${want}`);
  }
  // Runs for one host.
  await go(page, `/jobs?host=${id}`);
  assert.match(await text(page), /Runs that included web-1/);
  // Alerts → notification settings; Monitoring → its health settings.
  await go(page, "/alerts");
  assert.ok(await page.$('main a[href="/settings/notifications"]'), "Alerts link to notification settings");
  await go(page, "/monitoring");
  await click(page, "nav[aria-label='Monitoring'] a", "Health checks and settings");
  await waitFor(page, async () => page.url().endsWith("/monitoring/settings"), "monitoring settings");
  await waitText(page, "Using the Recommended profile");
  // An alert links to its host and the host's monitoring.
  const open = (await api(page, "GET", `/alerts?view=open&host_id=${ids["nas-01"]}&limit=1`)).body.items;
  if (open.length) {
    await go(page, `/alerts/${open[0].id}`);
    await waitText(page, "Timeline");
    for (const want of [`/hosts/${ids["nas-01"]}`, `/hosts/${ids["nas-01"]}/monitoring`]) assert.ok(await page.$(`main a[href="${want}"]`), `alert links to ${want}`);
  }
  // Network Activity ↔ Firewall.
  await go(page, "/network");
  assert.ok(await page.$('main a[href="/firewall?tab=designer"]'), "Network Activity → Firewall policy");
  await go(page, "/firewall");
  assert.ok(await page.$('main a[href="/network"]'), "Firewall → observed traffic");
  await waitFor(page, async () => !!(await page.$(`main a[href="/hosts/${id}/firewall"]`)), "Firewall hosts → a host's firewall page");
  assert.ok(await page.$(`main a[href="/hosts/${id}/network"]`), "Firewall hosts → a host's traffic");
  await go(page, `/hosts/${id}/firewall`);
  assert.ok(await page.$(`main a[href="/hosts/${id}/network"]`), "host firewall → host traffic");
  await go(page, `/hosts/${id}/network`);
  assert.ok(await page.$(`main a[href="/hosts/${id}/firewall"]`), "host traffic → host firewall");
  assert.ok(await page.$(`main a[href="/network?host=${id}"]`), "host traffic → fleet Network Activity");
  // The old monitoring-settings address still works.
  await go(page, "/settings/monitoring");
  await waitFor(page, async () => page.url().endsWith("/monitoring/settings"), "redirect");
  assert.deepEqual(page.errors, []);
});

test("loading is never shown as empty or healthy", async () => {
  const page = await session(browser);
  try {
    // Hold the answers back: the pages must say they're loading, not
    // "0 hosts" or "nothing needs attention".
    await page.setRequestInterception(true);
    let hold = true;
    const held = [];
    page.on("request", (r) => {
      if (hold && /\/api\/v1\/(hosts|hosts\/[^/?]+\/facts|alerts\/summary|desired\/summary|firewall\/hosts|jobs)(\?|$)/.test(r.url())) held.push(r);
      else r.continue();
    });
    await page.goto(base + "/", { waitUntil: "domcontentloaded" });
    await waitText(page, "Loading…");
    await sleep(800);
    let t = await text(page);
    assert.doesNotMatch(t, /Nothing needs attention|0 hosts|No open alerts|Nothing running/);
    await shot(page, "loading-overview");
    hold = false;
    held.splice(0).forEach((r) => r.continue());
    await waitText(page, "Needs attention");
    t = await text(page);
    assert.match(t, /\d+ hosts?/);
    // A host's attention list waits for its sources too, and its
    // inventory summary isn't "0 filesystems" before the inventory is in.
    hold = true;
    await page.goto(base + `/hosts/${ids["web-1"]}`, { waitUntil: "domcontentloaded" });
    await sleep(1500);
    t = await text(page);
    assert.doesNotMatch(t, /Nothing needs attention on this host|0 filesystems|0 addresses/);
    assert.match(t, /Filesystems, addresses and interfaces — loading…/);
    hold = false;
    held.splice(0).forEach((r) => r.continue());
    await waitFor(page, async () => /Needs attention|Nothing needs attention on this host/.test(await text(page)), "host attention after loading");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.close();
  }
});
