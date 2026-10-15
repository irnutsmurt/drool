// Alerts, maintenance and notifications (M12): the Alerts views and an
// alert's details, acknowledging from the keyboard, maintenance from a
// host page, the short notification setup with a test, advanced routing,
// and a failing destination. Notifications go only to a local fake
// receiver started here (never a real service). Needs test/integration
// TestBrowser's environment (fixture hosts with health issues).
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { api, base, click, field, fill, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;
// The fake receiver: records every request; answers with `status`.
const receiver = { hits: [], status: 204 };
const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    receiver.hits.push({ headers: req.headers, body: body ? JSON.parse(body) : null });
    res.writeHead(receiver.status);
    res.end();
  });
});
let hookURL;

before(async () => {
  browser = await launch();
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  hookURL = `http://127.0.0.1:${server.address().port}/drool-hook`;
});
after(async () => {
  await browser?.close();
  server.close();
});
beforeEach(async () => {
  page?.close().catch(() => {});
  page = await session(browser);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  page.ids = Object.fromEntries(hosts.map((h) => [h.name, h.id]));
  // Monitoring's fixture issues open alerts shortly after the hosts exist.
  await waitFor(page, async () => (await api(page, "GET", `/alerts?view=open&severity=critical&host_id=${page.ids["nas-01"]}`)).body?.items?.length > 0,
    "nas-01's fixture alerts", 120000);
});

const go = async (path) => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await sleep(400);
};

test("alerts: active list with words and icons, nav count, filters, keyboard tabs", async () => {
  await go("/alerts");
  await waitText(page, "Critical");
  const t = await text(page);
  assert.match(t, /nas-01/);
  assert.match(t, /\d+ open: \d+ critical, \d+ warning/);
  assert.match(t, /No destination|Notified|Sending/);
  // The navigation shows the count, not by colour alone.
  const nav = await page.$eval('nav a[href="/alerts"]', (a) => a.innerHTML);
  assert.match(nav, /aria-label="\d+ unacknowledged alert/);
  // Severity filter (server side).
  const sel = await page.$("select");
  await sel.select("critical");
  await waitFor(page, async () => page.url().includes("severity=critical"), "severity in url");
  await waitFor(page, async () => !(await page.$$eval("tbody tr", (r) => r.map((x) => x.innerText).join("\n"))).includes("Warning"), "only critical");
  await shot(page, "alerts-active-critical");
  // The tabs are a keyboard tablist.
  await page.focus('[role="tab"][aria-selected="true"]');
  await page.keyboard.press("ArrowRight");
  await waitFor(page, async () => page.url().includes("view=acknowledged"), "arrow to Acknowledged");
  await page.keyboard.press("ArrowRight");
  await waitFor(page, async () => page.url().includes("view=resolved"), "arrow to history");
  await waitText(page, "From");
  assert.deepEqual(page.errors, []);
});

test("alert details: timeline, source issue, acknowledge with a note from the keyboard", async () => {
  const open = (await api(page, "GET", `/alerts?view=active&host_id=${page.ids["nas-01"]}&severity=critical`)).body.items;
  assert.ok(open.length > 0, "nas-01 has a critical alert");
  const a = open[0];
  await go(`/alerts/${a.id}`);
  await waitText(page, "Timeline");
  let t = await text(page);
  assert.match(t, /Alert opened \(Critical\)/);
  assert.match(t, /Source: health issue still active/);
  assert.match(t, /resolves on its own when monitoring sees the condition recover/);
  await tabTo(page, () => document.activeElement?.tagName === "TEXTAREA", "acknowledgement note");
  await page.keyboard.type("Replacing failed disk this evening");
  await page.keyboard.press("Tab");
  assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Acknowledge");
  await page.keyboard.press("Enter");
  await waitText(page, "Acknowledged by");
  t = await text(page);
  assert.match(t, /Replacing failed disk this evening/);
  assert.match(t, /✓ Acknowledged/);
  const after = (await api(page, "GET", `/alerts/${a.id}`)).body;
  assert.equal(after.alert.state, "acknowledged");
  assert.equal(after.issue.state, "active", "acknowledging never changes the health issue");
  await shot(page, "alert-detail-acknowledged");
  assert.deepEqual(page.errors, []);
});

test("maintenance from a host page: banner, suppressed alerts, end", async () => {
  await go(`/hosts/${page.ids["nas-01"]}`);
  await click(page, "button", "Start maintenance");
  await waitText(page, "Notifications are paused for these hosts");
  await fill(page, "Reason (recommended)", "Replacing storage");
  await click(page, '[role="dialog"] label', "1 hour");
  await shot(page, "maintenance-dialog");
  await click(page, '[role="dialog"] button[type="submit"]', "Start maintenance");
  await waitText(page, "In maintenance until");
  await waitText(page, "Suppressed by maintenance");
  let t = await text(page);
  assert.match(t, /Replacing storage/);
  assert.match(t, /monitoring and alerts continue/);
  await shot(page, "host-in-maintenance");
  // Health issues stay visible.
  await go(`/hosts/${page.ids["nas-01"]}/monitoring`);
  t = await text(page);
  assert.match(t, /In maintenance until/);
  assert.match(t, /Active health issues|issue/);
  // The Maintenance tab lists it; End now.
  await go("/alerts?view=maintenance");
  await waitText(page, "Replacing storage");
  assert.match(await text(page), /nas-01 · 1 host/);
  await click(page, "button", "End now");
  await waitText(page, "No maintenance in progress.");
  await go(`/hosts/${page.ids["nas-01"]}`);
  assert.doesNotMatch(await text(page), /In maintenance until/);
  assert.deepEqual(page.errors, []);
});

test("notifications: empty state, add a webhook, test it, save; advanced routing", async () => {
  await go("/settings/notifications");
  await waitText(page, "No destinations configured");
  await click(page, "button", "Add destination");
  // The kind cards: pick by the card's title (Discord's hint mentions webhooks too).
  await page.evaluate(() => [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.firstElementChild?.textContent === "Webhook").click());
  await fill(page, "Name", "Home Assistant");
  await fill(page, "URL", hookURL);
  await click(page, '[role="dialog"] button', "Send test");
  await waitText(page, "Test delivered");
  assert.equal(receiver.hits.length, 1);
  assert.equal(receiver.hits[0].body.test, true, "a test, not an alert");
  assert.equal(receiver.hits[0].body.schema, "drool.notification.v1");
  assert.match(await text(page), /Notifications to this URL are sent unencrypted/);
  await shot(page, "destination-dialog-tested");
  await click(page, '[role="dialog"] button[type="submit"]', "Save");
  await waitText(page, "Notify for: Recommended (Warnings + Critical, with recoveries)");
  let t = await text(page);
  assert.ok(!t.includes("drool-hook"), "the URL (a secret) is never shown");
  assert.match(t, /URL: http:\/\/127\.0\.0\.1:\d+\/…•••••••/);
  // Advanced: the default route lists it; add a Critical-only route.
  await click(page, "button", "Advanced");
  await waitText(page, "Routing");
  await waitText(page, "Warning and Critical → Home Assistant");
  await click(page, "button", "Add route");
  await fill(page, "Name", "Critical only");
  await click(page, '[role="dialog"] label', "Critical");
  await click(page, '[role="dialog"] label', "Home Assistant");
  await click(page, '[role="dialog"] button[type="submit"]', "Save");
  await waitText(page, "Critical → Home Assistant");
  await shot(page, "notifications-advanced");
  // The links in notifications point here (set from the browser).
  const s = (await api(page, "GET", "/notifications/settings")).body.settings;
  assert.equal(s.link_base_url, new URL(base).origin);
  assert.deepEqual(page.errors, []);
});

test("notifications: a Telegram destination: setup steps, a write-only token, a safe summary", async () => {
  // A made-up token. Nothing is sent from the browser tests (no Send test,
  // no route to it): sending is covered against a fake Bot API in the API
  // tests.
  const token = "7012345678:example-only-not-a-real-bot-token-222222";
  const secretPart = token.split(":")[1];
  await go("/settings/notifications");
  await click(page, "button", "Add destination");
  await page.evaluate(() => [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.firstElementChild?.textContent === "Telegram").click());
  await waitFor(page, async () => !!(await page.$('[data-testid="telegram-steps"]')), "setup steps");
  assert.match(await page.$eval('[data-testid="telegram-steps"]', (e) => e.innerText), /@BotFather[\s\S]*\/newbot[\s\S]*chat ID[\s\S]*don.t share it/);
  await fill(page, "Name", "Ops Telegram");
  await fill(page, "Bot token", token);
  await fill(page, "Chat ID", "-1001234567890");
  await fill(page, "Topic ID (optional)", "7");
  await (await field(page, "Notify for")).select("custom");
  assert.equal(await (await field(page, "Bot token")).evaluate((x) => x.type), "password", "the token field is masked");
  await shot(page, "destination-telegram");
  await click(page, '[role="dialog"] button[type="submit"]', "Save");
  await waitText(page, "Chat -1001234567890, topic 7 · bot token •••••••");
  assert.ok(!(await text(page)).includes(secretPart), "the token is never shown");
  const d = (await api(page, "GET", "/notifications/destinations")).body.destinations.find((x) => x.name === "Ops Telegram");
  assert.ok(d.secrets_set.bot_token && !d.config.url_display && !JSON.stringify(d).includes(secretPart), JSON.stringify(d));
  // Editing: the token is kept unless replaced, and never filled in.
  await page.evaluate(() => [...document.querySelectorAll("li")].find((l) => l.innerText.includes("Ops Telegram"))?.querySelectorAll("button").forEach((b) => b.textContent === "Edit" && b.click()));
  await waitFor(page, async () => !!(await page.$('[data-testid="telegram-steps"]')), "edit dialog");
  const tokenField = await field(page, "Bot token");
  assert.equal(await tokenField.evaluate((x) => x.value), "");
  assert.match(await tokenField.evaluate((x) => x.placeholder), /unchanged/);
  await page.keyboard.press("Escape");
  await api(page, "DELETE", `/notifications/destinations/${d.id}`);
  assert.deepEqual(page.errors, []);
});

test("a failing destination is visible; alert state stays accurate", async () => {
  receiver.status = 401;
  const before = receiver.hits.length;
  // A host leaves maintenance with an active alert: one opening notification after the grace period.
  const any = (await api(page, "GET", "/alerts?view=active")).body.items;
  assert.ok(any.length > 0, "an active alert");
  const vm = any[0].host_id;
  const open = (await api(page, "GET", `/alerts?view=active&host_id=${vm}`)).body.items;
  const m = (await api(page, "POST", "/maintenance", { host_ids: [vm], indefinite: true, reason: "test" })).body;
  await api(page, "POST", `/maintenance/${m.id}/end`);
  await waitFor(page, async () => receiver.hits.length > before, "opening after maintenance", 30000);
  assert.ok(receiver.hits[receiver.hits.length - 1].headers["idempotency-key"], "delivery id sent for deduplication");
  await go("/settings/notifications");
  await waitText(page, "Delivery failing");
  assert.match(await text(page), /Last failure: .* the receiver refused the credentials \(HTTP 401\)/);
  await go(`/alerts?view=active&host_id=${vm}`);
  await waitFor(page, async () => /Delivery failed|Partly delivered/.test(await text(page)), "delivery failure shown");
  await shot(page, "alerts-delivery-failed");
  const a = (await api(page, "GET", `/alerts/${open[0].id}`)).body.alert;
  assert.equal(a.state, "active", "a delivery failure never resolves an alert");
  receiver.status = 204;
  assert.deepEqual(page.errors, []);
});

test("the Overview and the Monitoring page show alert counts and recent events", async () => {
  // This file's alerting events are in the timeline...
  const titles = (await api(page, "GET", "/events?limit=200")).body.events.map((e) => e.title);
  for (const [re, what] of [[/^Maintenance (started|ended) by /, "maintenance"], [/^Notifications to .* are failing/, "the failing destination"],
    [/^Critical( \(escalated\))?: nas-01 /, "nas-01's critical alert"]]) {
    assert.ok(titles.some((s) => re.test(s)), `${what} in the timeline`);
  }
  // ...and the Overview shows the newest events as they are. The rest of the
  // suite keeps adding events (the hosts of Shepherds other tests stopped go
  // stale), so the card is compared with the timeline once both agree, not
  // with a list fixed in advance.
  const shown = () => page.evaluate(() => {
    const h = [...document.querySelectorAll("section h2")].find((e) => e.textContent === "Recent events");
    return h ? [...h.closest("section").querySelectorAll("li")].map((li) => li.children[1]?.textContent ?? "") : [];
  });
  let card = [], newest = [];
  await waitFor(page, async () => {
    await go("/");
    card = await shown();
    newest = (await api(page, "GET", "/events?limit=8")).body.events.map((e) => e.title);
    return card.length > 0 && JSON.stringify(card) === JSON.stringify(newest);
  }, "the Overview's recent events to match the timeline", 60000);
  let t = await text(page);
  assert.match(t, /\d+ critical · \d+ warning/);
  await go("/monitoring");
  await waitText(page, "Alerts:");
  t = await text(page);
  assert.match(t, /\d+ critical/);
  assert.match(t, /Warning and Critical health issues become alerts/);
  assert.deepEqual(page.errors, []);
});
