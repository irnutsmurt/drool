// Browser test helpers (puppeteer-core + the system Chromium). The tests run
// against a live controller: test/integration TestBrowser starts one with
// containers and runs them; they can also be pointed at any test
// controller with DROOL_E2E_URL, DROOL_E2E_USER and DROOL_E2E_PASSWORD.
import puppeteer from "puppeteer-core";
import fs from "node:fs";

export const base = process.env.DROOL_E2E_URL ?? "https://127.0.0.1:18590";
const user = process.env.DROOL_E2E_USER ?? "alex";
const password = process.env.DROOL_E2E_PASSWORD ?? "a-long-admin-password";
const shots = process.env.DROOL_E2E_SHOTS; // directory for screenshots (optional)
const chrome = process.env.CHROME ?? "/usr/bin/chromium";

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function launch() {
  const browser = await puppeteer.launch({ executablePath: chrome, headless: true, args: ["--ignore-certificate-errors", "--no-sandbox"] });
  return browser;
}

/** A logged-in page that records page errors. */
export async function session(browser, { width = 1440, height = 1000 } = {}) {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  await page.setViewport({ width, height });
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push("pageerror: " + e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !/status of (401|403|404|409|422)/.test(m.text())) page.errors.push("console: " + m.text());
  });
  await page.goto(base + "/", { waitUntil: "networkidle0" });
  if ((await text(page)).includes("Sign in")) {
    await fill(page, "Username", user);
    await fill(page, "Password", password);
    await Promise.all([page.waitForNetworkIdle({ idleTime: 500 }).catch(() => {}), page.keyboard.press("Enter")]);
    await waitFor(page, async () => !(await text(page)).includes("Sign in to DROOL"), "login");
  }
  return page;
}

export const text = (page) => page.evaluate(() => document.body.innerText);

export async function waitFor(page, fn, what, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) {
      await shot(page, "timeout-" + what.replace(/\W+/g, "-"));
      throw new Error("timed out waiting for " + what);
    }
    await sleep(200);
  }
}

export const waitText = (page, s, ms) => waitFor(page, async () => (await text(page)).includes(s), JSON.stringify(s), ms);

/** The input/select/textarea of a <label> whose <span> reads `label`. */
export async function field(page, label) {
  const h = await page.evaluateHandle((l) => {
    for (const lab of document.querySelectorAll("label")) {
      const span = lab.querySelector("span");
      if (span && span.textContent.trim() === l) return lab.querySelector("input,select,textarea");
    }
    return document.querySelector(`[aria-label="${l}"]`);
  }, label);
  const el = h.asElement();
  if (!el) throw new Error("no field " + label);
  return el;
}

export async function fill(page, label, value) {
  const el = await field(page, label);
  await el.click({ clickCount: 3 });
  await el.evaluate((x) => { if ("value" in x) x.value = ""; });
  await el.type(value);
}

/** Clicks the first enabled element matching `selector` whose text includes `label`. */
export async function click(page, selector, label, ms = 10000) {
  const end = Date.now() + ms;
  for (;;) {
    for (const el of await page.$$(selector)) {
      const t = await el.evaluate((x) => (x.textContent || "") + " " + (x.getAttribute("aria-label") || ""));
      if (t.includes(label) && (await el.evaluate((x) => !x.disabled))) {
        await el.click();
        return el;
      }
    }
    if (Date.now() > end) throw new Error(`no clickable ${selector} "${label}"`);
    await sleep(200);
  }
}

/** Presses Tab until `pred(activeElement)` holds: proves a control is
 * reachable from the keyboard. */
export async function tabTo(page, pred, what, max = 60) {
  for (let i = 0; i < max; i++) {
    if (await page.evaluate(pred)) return;
    await page.keyboard.press("Tab");
  }
  throw new Error("not reachable with Tab: " + what);
}

let n = 0;
export async function shot(page, name) {
  if (!shots) return;
  fs.mkdirSync(shots, { recursive: true });
  n++;
  await page.screenshot({ path: `${shots}/${String(n).padStart(2, "0")}-${name}.png`, fullPage: true });
}

/** REST call with the page's session (CSRF header from the cookie flow). */
export async function api(page, method, path, body) {
  return page.evaluate(async (m, p, b) => {
    const me = await fetch("/api/v1/auth/me");
    const csrf = (await me.json()).csrf_token;
    const r = await fetch("/api/v1" + p, { method: m, headers: { "Content-Type": "application/json", "X-DROOL-CSRF": csrf }, body: b ? JSON.stringify(b) : undefined });
    const t = await r.text();
    return { status: r.status, body: t ? JSON.parse(t) : null };
  }, method, path, body);
}
