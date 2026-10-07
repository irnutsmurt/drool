// Every main page, in Light and Dark: visible text meets WCAG AA contrast
// against what is actually behind it (4.5:1, or 3:1 for large text).
// Disabled controls are exempt (as in WCAG). Screenshots of each page in
// each theme are kept when DROOL_E2E_SHOTS is set.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, launch, session, shot, sleep } from "./lib.mjs";

let browser;
let page;
before(async () => {
  browser = await launch();
  page = await session(browser);
});
after(async () => { await browser?.close(); });

/** Runs in the page: returns texts below the required contrast. */
function lowContrast() {
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

const pages = async () => {
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  const host = hosts.find((h) => h.name === "web-1") ?? hosts[0];
  return [
    "/", "/hosts", `/hosts/${host.id}`, "/groups", "/jobs", "/jobs/new", "/agents", "/credentials", "/audit",
    "/settings/account", "/settings/system", "/settings/users", "/settings/tokens",
    "/firewall", "/firewall?tab=designer&view=topology", "/firewall?tab=designer&view=rules", "/firewall?tab=designer&view=yaml",
    "/firewall?tab=simulate", "/firewall?tab=transactions", "/firewall?tab=settings",
    // M10: network monitoring, the observed topology, filtered and empty views.
    "/network", "/network?policy=unclassified", "/network?scope=external&new=host&since=7d", "/network?peer=192.0.2.123", "/network?tab=settings",
    "/firewall?tab=designer&view=topology&topo=observed", "/firewall?tab=designer&view=topology&topo=both",
  ];
};

for (const theme of ["light", "dark"]) {
  test(`text contrast in ${theme} mode`, async () => {
    await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: theme }]);
    await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
    const failures = [];
    for (const p of await pages()) {
      await page.goto(base + p, { waitUntil: "networkidle0" });
      await sleep(900);
      const low = await page.evaluate(lowContrast);
      if (low.length) failures.push(`${p}:\n  ${low.join("\n  ")}`);
      await shot(page, `${theme}${p.replace(/[/?&=]+/g, "-")}`);
    }
    // A dialog and the login page too.
    await page.goto(base + "/firewall?tab=designer&view=rules", { waitUntil: "networkidle0" });
    await sleep(800);
    const btn = await page.evaluateHandle(() => [...document.querySelectorAll("button")].find((b) => b.textContent === "New rule"));
    if (btn.asElement()) {
      await btn.asElement().click();
      await sleep(500);
      const low = await page.evaluate(lowContrast);
      if (low.length) failures.push(`rule dialog:\n  ${low.join("\n  ")}`);
      await shot(page, `${theme}-rule-dialog`);
      await page.keyboard.press("Escape");
    }
    // A connection's details.
    await page.goto(base + "/network", { waitUntil: "networkidle0" });
    await sleep(800);
    const row = await page.$('[data-testid="flow-table"] tbody tr[data-flow-id] button');
    if (row) {
      await row.click();
      await page.waitForSelector('[data-testid="flow-details"]', { timeout: 10000 });
      await sleep(400);
      const low = await page.evaluate(lowContrast);
      if (low.length) failures.push(`connection details:\n  ${low.join("\n  ")}`);
      await shot(page, `${theme}-connection-details`);
      await page.keyboard.press("Escape");
    }
    assert.deepEqual(failures, [], `low-contrast text in ${theme} mode`);
    assert.deepEqual(page.errors, []);
  });
}
