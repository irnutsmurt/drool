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

/** A logged-in page that records page errors (the admin unless another
 * user is given; a phone-sized viewport with mobile: true). */
export async function session(browser, { width = 1440, height = 1000, mobile = false, as } = {}) {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  await page.setViewport(mobile ? { width, height, isMobile: true, hasTouch: true } : { width, height });
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push("pageerror: " + e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !/status of (401|403|404|409|422)/.test(m.text())) page.errors.push("console: " + m.text());
  });
  await page.goto(base + "/", { waitUntil: "networkidle0" });
  if ((await text(page)).includes("Sign in")) {
    await fill(page, "Username", as?.user ?? user);
    await fill(page, "Password", as?.password ?? password);
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

/** Runs in the page: returns texts below the required contrast. */
export function lowContrast() {
  const parse = (c) => {
    const m = c.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    const [r, g, b, a = 1] = m[1].split(/[ ,/]+/).filter(Boolean).map(Number);
    return { r, g, b, a };
  };
  const lum = ({ r, g, b }) => {
    const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  };
  const over = (top, bottom) => ({ r: top.r * top.a + bottom.r * (1 - top.a), g: top.g * top.a + bottom.g * (1 - top.a), b: top.b * top.a + bottom.b * (1 - top.a), a: 1 });
  const background = (el) => {
    const layers = [];
    for (let e = el; e; e = e.parentElement) {
      const c = parse(getComputedStyle(e).backgroundColor);
      if (c && c.a > 0) {
        layers.push(c);
        if (c.a >= 1) break;
      }
    }
    let bg = { r: 255, g: 255, b: 255, a: 1 };
    const root = parse(getComputedStyle(document.body).backgroundColor);
    if (root) bg = root;
    for (const l of layers.reverse()) bg = over(l, bg);
    return bg;
  };
  const out = [];
  const seen = new Set();
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (!el || seen.has(el) || !n.textContent.trim()) continue;
    seen.add(el);
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0 || cs.visibility === "hidden" || el.closest("[aria-hidden='true'], .sr-only, svg, option")) continue;
    if (el.closest("button:disabled, [disabled], [aria-disabled='true']")) continue;
    let op = 1;
    for (let e = el; e; e = e.parentElement) op *= Number(getComputedStyle(e).opacity);
    if (op < 0.95) continue; // deliberately dimmed (disabled rules, members)
    const fg = parse(cs.color);
    if (!fg) continue;
    const bg = background(el);
    const L1 = lum(over(fg, bg));
    const L2 = lum(bg);
    const ratio = (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05);
    const size = parseFloat(cs.fontSize);
    const large = size >= 24 || (size >= 18.66 && Number(cs.fontWeight) >= 700);
    const need = large ? 3 : 4.5;
    if (ratio < need) out.push(`${ratio.toFixed(2)} < ${need}: "${n.textContent.trim().slice(0, 50)}" (${cs.color} on rgb(${Math.round(bg.r)},${Math.round(bg.g)},${Math.round(bg.b)}))`);
  }
  return out;
}

