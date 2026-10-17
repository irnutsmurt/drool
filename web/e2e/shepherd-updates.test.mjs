// Shepherds → Updates (ARCHITECTURE-ALPHA §42): what a person sees first is
// how updates stand and the two settings they normally need (automatic
// updates, the channel); rollout tuning, an exact version and custom
// releases with their keys wait under Advanced settings. The browser suite's
// controller is a development build without the DROOL release key, so the
// page must say official releases can't work here — never that updates are
// working. A viewer reads the same status and changes nothing. Needs
// test/integration TestBrowser's environment.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let admin;
const run = Date.now().toString(36).slice(-5);
const viewer = { user: `e2e-upd-viewer-${run}`, password: "viewer-password-long" };
let viewerId;
let saved;

before(async () => {
  browser = await launch();
  admin = await session(browser);
  saved = (await api(admin, "GET", "/agents/updates/policy")).body;
  const u = await api(admin, "POST", "/users", { username: viewer.user, password: viewer.password, role: "viewer" });
  assert.equal(u.status, 201, JSON.stringify(u.body));
  viewerId = u.body.id;
});
after(async () => {
  if (admin && saved) await api(admin, "PUT", "/agents/updates/policy", saved).catch(() => {});
  if (admin && viewerId) await api(admin, "DELETE", `/users/${viewerId}`).catch(() => {});
  await browser?.close();
});

const open = async (page, path = "/agents?tab=updates") => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await waitText(page, "Shepherd updates");
  await sleep(300);
};
const policy = async () => (await api(admin, "GET", "/agents/updates/policy")).body;

test("the update page: status first, two settings, the rest under Advanced", async () => {
  // The default a new installation has, unless someone saved another.
  await api(admin, "PUT", "/agents/updates/policy", { policy: "automatic", channel: "stable", version: "", canary: 1, batch_size: 5, max_failures: 1, health_window_s: 120 });
  await open(admin);
  const t = await text(admin);
  assert.match(t, /Automatic updates/);
  assert.match(t, /Update channel/);
  assert.match(t, /Last checked/);
  // Honest: this build can't verify official releases, and says so where the
  // status is, and where the last check would be.
  assert.match(t, /no DROOL release key built in/);
  assert.match(t, /Not checked: This DROOL build has no DROOL release key/);
  // Nothing of the expert workflow on the first screen.
  for (const word of ["Release-signing", "Upload release", "Canary", "Health window", "Exactly this version", "manifest"]) {
    assert.ok(!t.includes(word), `"${word}" on the first screen`);
  }
  const auto = await admin.$('[data-testid="updates-overview"] input[type=checkbox]');
  assert.ok(await auto.evaluate((x) => x.checked), "automatic updates on by default");
  const options = await admin.$$eval('select[aria-label="Update channel"] option', (os) => os.map((o) => [o.value, o.textContent]));
  assert.deepEqual(options, [["stable", "Stable (recommended)"], ["prerelease", "Release candidates"]], "only the channels the backend supports");
  await shot(admin, "updates-first-screen");

  // Turning automatic updates off and on saves at once: no confirmation for
  // a setting. From the keyboard, the focus stays on the box while it saves,
  // and the box shows the choice: saving is held back here to see that.
  const onBox = () => document.activeElement?.matches('[data-testid="updates-overview"] input[type=checkbox]') ?? false;
  const slowSave = (r) => {
    if (r.method() === "PUT" && r.url().endsWith("/agents/updates/policy")) sleep(800).then(() => r.continue());
    else r.continue();
  };
  await admin.setRequestInterception(true);
  admin.on("request", slowSave);
  try {
    await auto.focus();
    await admin.keyboard.press("Space");
    await sleep(400);
    assert.ok(await admin.evaluate(onBox), "the focus stays on the box while it saves");
    assert.equal(await auto.evaluate((x) => x.checked), false, "the box shows the choice being saved");
    await waitFor(admin, async () => (await policy()).policy === "manual", "automatic updates off");
    await waitText(admin, "Shepherds update only when someone starts an update");
    await admin.keyboard.press("Space");
    await waitFor(admin, async () => (await policy()).policy === "automatic", "automatic updates back on");
    await waitText(admin, "On (recommended)");
    assert.ok(await admin.evaluate(onBox), "the focus is still on the box");
  } finally {
    admin.off("request", slowSave);
    await admin.setRequestInterception(false);
  }
  await admin.select('select[aria-label="Update channel"]', "prerelease");
  await waitFor(admin, async () => (await policy()).channel === "prerelease", "the release candidate channel");
  await admin.select('select[aria-label="Update channel"]', "stable");
  await waitFor(admin, async () => (await policy()).channel === "stable", "back to stable");

  // Advanced settings: one keyboard step away, in the address.
  await tabTo(admin, () => document.activeElement?.matches("button[aria-expanded]") && document.activeElement.textContent.includes("Advanced settings"),
    "the Advanced settings button");
  assert.equal(await admin.evaluate(() => document.activeElement.getAttribute("aria-expanded")), "false");
  await admin.keyboard.press("Enter");
  await waitText(admin, "How updates roll out");
  assert.ok(admin.url().includes("advanced=1"), admin.url());
  const adv = await text(admin);
  for (const want of ["How updates roll out", "Hosts first (canary)", "Exactly this version", "Custom releases", "Your release-signing keys",
    "Official releases", "DROOL project keys built in"]) {
    assert.ok(adv.includes(want), `Advanced shows ${want}`);
  }
  assert.match(adv, /none in this build/);
  await shot(admin, "updates-advanced");
  // Saving the rollout settings keeps the rest of the policy.
  // A number input: triple-click doesn't select it; End and Backspace clear it.
  const canary = await admin.$('xpath///label[.//span[text()="Hosts first (canary)"]]//input');
  await canary.click();
  await admin.keyboard.press("End");
  await admin.keyboard.press("Backspace");
  await canary.type("2");
  await click(admin, "button", "Save rollout settings");
  await waitFor(admin, async () => { const p = await policy(); return p.canary === 2 && p.policy === "automatic" && p.channel === "stable"; }, "the canary saved");
  assert.deepEqual(admin.errors, []);
});

test("a viewer sees how updates stand and changes nothing", async () => {
  const page = await session(browser, { as: viewer });
  try {
    await open(page);
    const t = await text(page);
    assert.match(t, /Read-only:/);
    assert.match(t, /Automatic updates/);
    assert.equal(await page.$('[data-testid="updates-overview"] input[type=checkbox]'), null, "no automatic-updates control");
    assert.equal(await page.$('select[aria-label="Update channel"]'), null, "no channel control");
    assert.ok(!t.includes("Check now"), "no Check now");
    await open(page, "/agents?tab=updates&advanced=1");
    const adv = await text(page);
    assert.ok(adv.includes("Custom releases") && !adv.includes("Upload release") && !adv.includes("Save rollout settings"), "Advanced read-only");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("installing a Shepherd needs no key; your own key is optional, under Advanced", async () => {
  const hosts = (await api(admin, "GET", "/hosts")).body.items;
  const h = hosts.find((x) => x.management?.ssh_configured && x.management?.preferred !== "agent" && !x.jump_only && x.pinned_key);
  assert.ok(h, "an SSH host without a Shepherd");
  await admin.goto(base + "/hosts/" + h.id + "/management", { waitUntil: "networkidle0" });
  await click(admin, "main button", "Install Shepherd (DROOL agent)");
  await waitText(admin, "takes official DROOL updates by itself");
  const dialog = await admin.$('[data-testid="install-shepherd"]');
  // checkVisibility: a closed <details> keeps its content laid out (offsetParent is set) but hidden.
  const visible = await dialog.evaluate((d) => [...d.querySelectorAll("input")].filter((i) => i.checkVisibility() && i.type !== "radio").length);
  assert.equal(visible, 0, "no key field until Advanced is opened");
  await click(admin, '[data-testid="install-shepherd"] summary', "Advanced: also trust your own release-signing key");
  await waitText(admin, "Your release-signing public key");
  await click(admin, '[role="dialog"] button', "Cancel");
  assert.deepEqual(admin.errors, []);
});
