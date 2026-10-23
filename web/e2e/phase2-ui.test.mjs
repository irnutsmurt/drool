// Phase 2 (ARCHITECTURE-ALPHA §47) at the Design Standard's widths and
// themes (§6, §9): the Overview and a host's Overview (Needs attention from
// the server's verdicts), the Hosts list, a Pack's page with its Actions
// menu and Add hosts, the fleet's Health issues, Health checks (routine and
// Advanced), an alert's "Adjust when this alerts…", the host's Danger zone,
// and Users with Change role…. At 1440, 1280, 1024 and 390 px (a touch
// phone), in light and dark: nothing scrolls sideways or is cut off, text
// reaches its contrast, touch targets are 44 px on the phone, and focus is
// visible and returns where it should. And Users: a role changes only in
// Change role…, never from a stray key. Needs test/integration TestBrowser's
// environment (the monitoring fixture: nas-01's /srv/media alert).
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, fill, launch, lowContrast, session, shot, sleep, waitFor } from "./lib.mjs";

const adminPassword = process.env.DROOL_E2E_PASSWORD ?? "a-long-admin-password";
const widths = [[1440, 1000, false], [1280, 900, false], [1024, 800, false], [390, 844, true]];
const themes = ["light", "dark"];

let browser;
let admin;
const ids = {};
let pack;
let user;

before(async () => {
  browser = await launch();
  admin = await session(browser);
  for (const h of (await api(admin, "GET", "/hosts")).body.items) ids[h.name] = h.id;
  await waitFor(admin, async () => ((await api(admin, "GET", `/alerts?view=active&host_id=${ids["nas-01"]}`)).body?.items ?? [])
    .some((a) => a.check === "filesystem" && a.subject === "/srv/media"), "nas-01's /srv/media alert (the monitoring fixture)", 120000);
  const run = Date.now().toString(36);
  pack = (await api(admin, "POST", "/groups", { name: `e2e-p2-${run}`, description: "Phase 2 layout" })).body;
  await api(admin, "POST", `/groups/${pack.id}/members`, { host_ids: [ids["nas-01"], ids["pi-sd"]] });
  user = (await api(admin, "POST", "/users", { username: `e2e-role-${run}`, password: "first-password-123", role: "viewer" })).body;
});
after(async () => {
  if (pack) await api(admin, "DELETE", `/groups/${pack.id}`).catch(() => {});
  if (user) await api(admin, "DELETE", `/users/${user.id}`).catch(() => {});
  await browser?.close();
});

const inner = (page, sel) => page.$eval(sel, (e) => e.innerText).catch(() => "");
const mediaAlert = async () => ((await api(admin, "GET", `/alerts?view=active&host_id=${ids["nas-01"]}`)).body?.items ?? [])
  .find((a) => a.check === "filesystem" && a.subject === "/srv/media");

/** Layout, contrast, touch and clipping inside scope (a selector), as in
 * Phase 1's layout test. */
async function check(page, scope, what, mobile) {
  const r = await page.evaluate((scope, mobile) => {
    const root = document.querySelector(scope) ?? document.body;
    const visible = (e) => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden" && !e.closest(".sr-only");
    const sideways = document.documentElement.scrollWidth - innerWidth;
    const clipped = [root, ...root.querySelectorAll("*")].filter((e) => visible(e) && e.clientWidth > 1 && e.scrollWidth > e.clientWidth + 2
      && !["auto", "scroll"].includes(getComputedStyle(e).overflowX) && getComputedStyle(e).overflowX !== "visible" && !e.closest("pre, code"))
      .map((e) => `${e.tagName}.${String(e.className).slice(0, 40)} ${e.scrollWidth}/${e.clientWidth}`);
    const scrolls = (e) => { for (let a = e.parentElement; a; a = a.parentElement) {
      if (["auto", "scroll"].includes(getComputedStyle(a).overflowX) && a.scrollWidth > a.clientWidth) return true; } return false; };
    const outside = [...root.querySelectorAll("button, input, select, a, [role=option]")].filter(visible)
      .filter((e) => { const b = e.getBoundingClientRect(); return (b.right > innerWidth + 1 || b.left < -1) && !scrolls(e); })
      .map((e) => (e.innerText || e.getAttribute("aria-label") || e.tagName).slice(0, 40));
    const small = !mobile ? [] : [...root.querySelectorAll("button, input:not([type=checkbox]):not([type=radio]), select, summary, [role=option], label:has(> input[type=radio]), label:has(> input[type=checkbox])")]
      .filter(visible).filter((e) => !e.closest("p") || e.tagName !== "A")
      .filter((e) => { const b = e.getBoundingClientRect(); const iconOnly = e.tagName === "BUTTON" && e.innerText.trim().length <= 1; return b.height < 43.5 || (iconOnly && b.width < 43.5); })
      .map((e) => `${(e.innerText || e.getAttribute("aria-label") || e.tagName).trim().slice(0, 30)} ${Math.round(e.getBoundingClientRect().width)}×${Math.round(e.getBoundingClientRect().height)}`);
    return { sideways, clipped, outside, small };
  }, scope, mobile);
  assert.ok(r.sideways <= 0, `${what}: the page scrolls sideways by ${r.sideways} px`);
  assert.deepEqual(r.clipped, [], `${what}: cut off`);
  assert.deepEqual(r.outside, [], `${what}: controls outside the window`);
  assert.deepEqual(r.small, [], `${what}: touch targets under 44 px`);
  assert.deepEqual(await page.evaluate(lowContrast), [], `${what}: contrast`);
}

const focusShown = (page) => page.evaluate(() => {
  const e = document.activeElement;
  if (!e || e === document.body) return "nothing focused";
  const s = getComputedStyle(e);
  return s.outlineStyle !== "none" && parseFloat(s.outlineWidth) > 0 || s.boxShadow !== "none" ? "" : `no focus indicator on ${e.tagName} ${e.innerText?.slice(0, 30)}`;
});
const active = (page) => page.evaluate(() => (document.activeElement?.innerText || document.activeElement?.getAttribute("aria-label") || "").trim());

/** Opens a control by keyboard on a desktop (where focus shows), by touch on the phone. */
async function open(page, el, mobile) {
  if (mobile) return el.tap();
  await el.focus();
  await page.keyboard.press("Enter");
}

/** Waits for what the step shows; confirms who you are if DROOL asks. */
async function reauthIfAsked(page, done, what) {
  await waitFor(page, async () => !!(await page.$('[data-testid="reauth-dialog"]')) || (await done()), what);
  if (await page.$('[data-testid="reauth-dialog"]')) {
    await fill(page, "Password", adminPassword);
    await click(page, '[data-testid="reauth-dialog"] button', "Confirm");
    await waitFor(page, done, what);
  }
}

test("Users: a role changes only in Change role…, with what each role may do; your own row has nothing to break", async () => {
  const page = await session(browser);
  try {
    await page.goto(`${base}/settings/users`, { waitUntil: "networkidle0" });
    await waitFor(page, async () => (await inner(page, "main")).includes(user.username), "the user's row");
    // No select in the table: a key press can't change anyone's role.
    assert.equal(await page.$$eval("main table select", (s) => s.length), 0, "no role select in the list");
    // Your own row: a link to your account, no Change role or More.
    const own = await page.$$eval("main tr", (rows) => rows.find((r) => r.innerText.includes("(you)"))?.innerText ?? "");
    assert.match(own, /Your account/);
    assert.doesNotMatch(own, /Change role|More|Delete|Disable/);
    // The role, by name, and what it may do under the form.
    assert.match(await inner(page, '[data-testid="role-hint"]'), /^Operator: Day-to-day work/);

    await click(page, "main button", `Change role of ${user.username}`);
    await page.waitForSelector('[data-testid="change-role"]');
    const d = await inner(page, '[data-testid="change-role"]');
    for (const r of ["Administrator", "Operator", "Auditor", "Viewer (now)"]) assert.ok(d.includes(r), `the role ${r}`);
    assert.match(d, /applies at once, to the sessions and API tokens/);
    const submit = () => page.$eval('[data-testid="change-role"] button[type="submit"]', (b) => [b.textContent, b.disabled]);
    assert.deepEqual(await submit(), ["Change role", true], "nothing to save before a choice");
    // Arrow keys move the choice; nothing is saved until the button.
    await page.focus('[data-testid="change-role"] input[type="radio"]:checked');
    await page.keyboard.press("ArrowUp");
    assert.deepEqual(await submit(), [`Make ${user.username} an auditor`, false]);
    assert.equal((await api(page, "GET", "/users")).body.items.find((u) => u.id === user.id).role, "viewer", "nothing saved by a key");
    await page.keyboard.press("ArrowUp");
    assert.deepEqual(await submit(), [`Make ${user.username} an operator`, false]);
    await shot(page, "p2-change-role");
    await click(page, '[data-testid="change-role"] button[type="submit"]', `Make ${user.username} an operator`);
    await reauthIfAsked(page, async () => !(await page.$('[data-testid="change-role"]')), "the role saved");
    await waitFor(page, async () => (await api(page, "GET", "/users")).body.items.find((u) => u.id === user.id).role === "operator", "operator");

    // Disable is confirmed, and says what it does.
    await click(page, "main button", `More for ${user.username}`);
    await click(page, '[role="menuitem"]', "Disable…");
    await page.waitForSelector('[data-testid="confirm-dialog"]');
    assert.match(await inner(page, '[data-testid="confirm-dialog"]'), /signed out at once and can.t sign in or use their API tokens/);
    await page.keyboard.press("Escape");
    await waitFor(page, async () => !(await page.$('[data-testid="confirm-dialog"]')), "cancelled");
    assert.equal((await api(page, "GET", "/users")).body.items.find((u) => u.id === user.id).disabled, false, "cancelled: still enabled");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

for (const [w, h, mobile] of widths) {
  for (const theme of themes) {
    test(`Phase 2 UI at ${w} px, ${theme}`, async () => {
      const page = await session(browser, { width: w, height: h, mobile });
      const tag = `${w}-${theme}`;
      const go = async (path, ready) => {
        await page.goto(base + path, { waitUntil: "networkidle0" });
        if (ready) await waitFor(page, ready, `${tag} ${path}`, 30000);
      };
      try {
        await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);

        // The Overview: Needs attention first, then Updates and reboots.
        await go("/", async () => !!(await page.$('[data-testid="fleet-attention"]')));
        assert.match(await inner(page, '[data-testid="fleet-attention"]'), /nas-01: \/srv\/media is over 95% full/);
        await check(page, "main", `${tag} Overview`, mobile);
        await shot(page, `p2-overview-${tag}`);

        // A host's Overview: its alerts once, under Needs attention.
        await go(`/hosts/${ids["nas-01"]}`, async () => (await inner(page, '[data-testid="host-attention"]')).includes("/srv/media"));
        await check(page, "main", `${tag} host Overview`, mobile);
        await shot(page, `p2-host-overview-${tag}`);

        // The Hosts list, honest about what isn't known.
        await go("/hosts", async () => (await inner(page, "main")).includes("nas-01"));
        await check(page, "main", `${tag} Hosts`, mobile);

        // The host's Danger zone: Remove host with the Shepherd's rare actions.
        await go(`/hosts/${ids["web-1"]}/management`, async () => (await inner(page, "main")).includes("Danger zone"));
        await check(page, "main", `${tag} host Management`, mobile);

        // A Pack: hosts and health first, Actions with Delete last.
        await go(`/groups/${pack.id}`, async () => !!(await page.$('[data-testid="pack-settings"]')));
        await check(page, "main", `${tag} Pack`, mobile);
        await shot(page, `p2-pack-${tag}`);
        const actions = await page.$('[data-testid="pack-actions"]');
        await open(page, actions, mobile);
        await page.waitForSelector('[role="menu"]');
        const items = await page.$$eval('[role="menuitem"]', (xs) => xs.map((x) => x.innerText.split("\n")[0]));
        assert.equal(items.at(-1), "Delete Pack…", `${tag}: Delete last: ${items}`);
        await check(page, '[role="menu"]', `${tag} Pack actions`, mobile);
        await page.keyboard.press("Escape");
        await waitFor(page, async () => !(await page.$('[role="menu"]')), "the menu closed");
        assert.equal(await active(page), "Actions", `${tag}: focus back on Actions`);
        if (!mobile) assert.equal(await focusShown(page), "", `${tag}: Actions' focus`);
        const add = await page.evaluateHandle(() => [...document.querySelectorAll("main button")].find((b) => b.innerText.trim() === "Add hosts…"));
        await open(page, add, mobile);
        await waitFor(page, async () => !!(await page.$('[data-testid="pack-join-summary"]')), "what joining means");
        await check(page, '[role="dialog"]', `${tag} Add hosts`, mobile);
        await shot(page, `p2-add-hosts-${tag}`);
        await page.keyboard.press("Escape");
        await waitFor(page, async () => !(await page.$('[role="dialog"]')), "Add hosts closed");
        assert.equal(await active(page), "Add hosts…", `${tag}: focus back on Add hosts…`);
        await go("/groups", async () => (await inner(page, "main")).includes(pack.name));
        await check(page, "main", `${tag} Packs`, mobile);

        // Monitoring: health issues first; Health checks, routine then Advanced.
        await go("/monitoring", async () => !!(await page.$('[data-testid="fleet-issues"]')));
        await check(page, "main", `${tag} Monitoring`, mobile);
        await go("/monitoring/settings", async () => (await inner(page, "main")).includes("Set for Packs, hosts and devices"));
        await check(page, "main", `${tag} Health checks`, mobile);
        await shot(page, `p2-health-checks-${tag}`);
        await go("/monitoring/settings?advanced=1", async () => (await inner(page, "main")).includes("Save collection settings"));
        await check(page, "main", `${tag} Health checks, Advanced`, mobile);

        // From an alert to the check behind it, in its panel.
        const a = await mediaAlert();
        await go("/alerts", async () => (await inner(page, "main")).includes(a.title));
        const act = await page.evaluateHandle((l) => [...document.querySelectorAll("main button")].find((b) => b.getAttribute("aria-label") === l), `Actions for ${a.title} on nas-01`);
        await open(page, act, mobile);
        await waitFor(page, async () => (await inner(page, '[data-testid="alert-actions"]')).includes("Adjust when this alerts…"), "the choice", 30000);
        await click(page, '[data-testid="alert-actions"] button', "Adjust when this alerts…");
        await page.waitForSelector('[data-testid="alert-rule"]');
        await check(page, '[data-testid="alert-inspector"]', `${tag} Adjust when this alerts`, mobile);
        await shot(page, `p2-alert-rule-${tag}`);

        // Users, and Change role….
        await go("/settings/users", async () => (await inner(page, "main")).includes(user.username));
        await check(page, "main", `${tag} Users`, mobile);
        const change = await page.evaluateHandle((l) => [...document.querySelectorAll("main button")].find((b) => b.getAttribute("aria-label") === l), `Change role of ${user.username}`);
        await open(page, change, mobile);
        await page.waitForSelector('[data-testid="change-role"]');
        await check(page, '[role="dialog"]', `${tag} Change role`, mobile);
        await page.keyboard.press("Escape");
        await waitFor(page, async () => !(await page.$('[data-testid="change-role"]')), "Change role closed");
        assert.equal(await active(page), "Change role…", `${tag}: focus back on Change role…`);
        if (!mobile) assert.equal(await focusShown(page), "", `${tag}: Change role's focus`);
        await sleep(100);
        assert.deepEqual(page.errors, []);
      } finally {
        await page.browserContext().close();
      }
    });
  }
}
