// Light / Dark / System: applied without a reload, persisted, and System
// follows the OS/browser preference live.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { base, launch, session, shot, sleep, waitFor } from "./lib.mjs";

let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });

const bg = (page) => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
const LIGHT = "rgb(246, 247, 249)";
const DARK = "rgb(17, 20, 24)";

async function choose(page, label) {
  // The header switcher: native radios, operated by clicking the label.
  const radio = await page.$(`[data-testid="theme-switcher"] input[value="${label.toLowerCase()}"]`);
  await radio.evaluate((r) => r.closest("label").click());
}

test("light, dark and system apply immediately and persist", async () => {
  const page = await session(browser);
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "light" }]);
  await page.goto(base + "/", { waitUntil: "networkidle0" });

  await choose(page, "Dark");
  await waitFor(page, async () => (await bg(page)) === DARK, "dark background");
  assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), "dark");
  await shot(page, "theme-dark");
  await page.reload({ waitUntil: "networkidle0" });
  assert.equal(await bg(page), DARK, "dark persists across a reload");
  assert.equal(await page.evaluate(() => localStorage.getItem("drool.theme")), "dark");

  await choose(page, "Light");
  await waitFor(page, async () => (await bg(page)) === LIGHT, "light background");
  await page.reload({ waitUntil: "networkidle0" });
  assert.equal(await bg(page), LIGHT, "light persists, even though…");
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "dark" }]);
  await sleep(200);
  assert.equal(await bg(page), LIGHT, "…an explicit choice ignores the OS setting");

  await choose(page, "System");
  await waitFor(page, async () => (await bg(page)) === DARK, "system → dark (OS is dark)");
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "light" }]);
  await waitFor(page, async () => (await bg(page)) === LIGHT, "system follows the OS live, without a reload");
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "dark" }]);
  await waitFor(page, async () => (await bg(page)) === DARK, "and back");
  await page.reload({ waitUntil: "networkidle0" });
  assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), "system");
  assert.equal(await bg(page), DARK);
  assert.deepEqual(page.errors, []);
  await page.close();
});

test("the account page offers the same choice, keyboard-operable", async () => {
  const page = await session(browser);
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "light" }]);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  await page.goto(base + "/settings/account", { waitUntil: "networkidle0" });
  const radios = await page.$$('fieldset:not(:has([data-testid="theme-switcher"].text-xs)) input[name="theme"]');
  assert.equal(radios.length, 3, "Light, Dark, System radios on the account page");
  // Arrow keys move between radios in a group (native behaviour).
  await radios[0].focus();
  await page.keyboard.press("ArrowRight");
  await waitFor(page, async () => (await bg(page)) === DARK, "arrow key selects Dark");
  await shot(page, "theme-account-dark");
  // The header switcher reflects it (same state, no reload).
  assert.equal(await page.$eval('[data-testid="theme-switcher"] input[value="dark"]', (r) => r.checked), true);
  // No flash on load: the theme is set before the app's scripts run.
  await page.reload({ waitUntil: "domcontentloaded" });
  assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), "dark");
  assert.deepEqual(page.errors, []);
  await page.close();
});

test("the login page follows the theme too", async () => {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "dark" }]);
  await page.goto(base + "/", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await page.evaluate(() => document.body.innerText)).includes("Sign in"), "login page");
  assert.equal(await bg(page), DARK, "system default follows the OS before anyone signs in");
  await shot(page, "theme-login-dark");
  await ctx.close();
});
