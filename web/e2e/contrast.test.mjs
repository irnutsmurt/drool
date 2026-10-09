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
after(async () => {
  await cleanupDesired().catch(() => {});
  await browser?.close();
});

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

const monitoringPages = (hosts) => {
  const id = (n) => hosts.find((h) => h.name === n)?.id;
  const out = ["/monitoring", "/monitoring?category=unhealthy", "/settings/monitoring"];
  if (id("nas-01")) {
    for (const tab of ["overview", "metrics", "services", "storage", "hardware", "health", "agent"]) out.push(`/hosts/${id("nas-01")}/monitoring?tab=${tab}`);
    out.push(`/hosts/${id("pi-sd")}/monitoring?tab=storage`, `/hosts/${id("gpu-01")}/monitoring?tab=hardware`, `/hosts/${id("gpu-01")}/monitoring?tab=health`, `/hosts/${id("old-vm")}/monitoring`);
  }
  return out;
};

const alertPages = async () => {
  const a = (await api(page, "GET", "/alerts?view=open&limit=1")).body?.items ?? [];
  return a.map((x) => `/alerts/${x.id}`);
};

// M13: schedules, workflows, recurring windows and rollouts (the
// automation tests ran first and left some of each).
const automationPages = async () => {
  const sch = (await api(page, "GET", "/schedules?kind=job")).body?.items ?? [];
  const win = (await api(page, "GET", "/schedules?kind=maintenance")).body?.items ?? [];
  const wfs = (await api(page, "GET", "/workflows")).body?.items ?? [];
  const jobs = (await api(page, "GET", "/jobs?limit=100")).body?.items ?? [];
  // Finished jobs only: an active job's page streams updates, so it never goes network-idle.
  const done = jobs.filter((j) => !["pending", "running", "paused"].includes(j.status));
  const picked = [done.find((j) => j.name === "Canary check"), done.find((j) => j.origin?.schedule_id), done.find((j) => j.name === "Check a service")];
  return [
    "/jobs/schedules", "/jobs/workflows", "/jobs/workflows/new", "/jobs/workflows/new?template=update_verify_services",
    "/jobs/schedules/new", "/jobs/schedules/new?kind=maintenance",
    ...sch.slice(0, 1).flatMap((s) => [`/jobs/schedules/${s.id}`, `/jobs/schedules/${s.id}/edit`]),
    ...win.slice(0, 1).map((s) => `/jobs/schedules/${s.id}`),
    ...wfs.slice(0, 1).map((w) => `/jobs/workflows/${w.id}`),
    ...picked.filter(Boolean).map((j) => `/jobs/${j.id}`),
  ];
};

// M14: desired state with a drift, an unknown and a conflict (its own
// baselines, archived afterwards).
let desiredFixture;
const desiredPages = async (hosts) => {
  const gpu = hosts.find((h) => h.name === "gpu-01");
  if (!gpu) return ["/desired", "/desired/baselines", "/desired/baselines/new", "/desired/history"];
  if (!desiredFixture) {
    const tag = Date.now().toString(36).slice(-4);
    const mk = async (name, assignments, resources) => (await api(page, "POST", "/desired/baselines", { name: `${name} ${tag}`, assignments, resources })).body;
    const g1 = (await api(page, "POST", "/groups", { name: `contrast-a-${tag}` })).body.id;
    const g2 = (await api(page, "POST", "/groups", { name: `contrast-b-${tag}` })).body.id;
    for (const g of [g1, g2]) await api(page, "PATCH", `/groups/${g}`, { host_ids: [gpu.id] });
    const b = await mk("Contrast", [{ scope: "host", host_id: gpu.id }], [
      { kind: "service", name: "ollama", state: "running", enabled: true },
      { kind: "service", name: "nvidia-persistenced", state: "stopped" },
      { kind: "package", name: "htop", state: "absent" },
    ]);
    await mk("Contrast A", [{ scope: "group", group_id: g1 }], [{ kind: "service", name: "ssh", state: "running" }]);
    await mk("Contrast B", [{ scope: "group", group_id: g2 }], [{ kind: "service", name: "ssh", state: "stopped" }]);
    await api(page, "POST", `/desired/hosts/${gpu.id}/refresh`);
    await sleep(3000);
    desiredFixture = { baseline: b.id, groups: [g1, g2], tag, host: gpu.id };
  }
  const f = desiredFixture;
  return ["/desired", "/desired?status=all", "/desired/baselines", `/desired/baselines/${f.baseline}`, `/desired/baselines/${f.baseline}/edit`,
    "/desired/baselines/new", "/desired/history", `/hosts/${f.host}/desired`];
};

async function cleanupDesired() {
  if (!desiredFixture || !page) return;
  for (const b of (await api(page, "GET", "/desired/baselines")).body?.items ?? []) {
    if (b.name.endsWith(desiredFixture.tag)) await api(page, "DELETE", `/desired/baselines/${b.id}`);
  }
  for (const g of desiredFixture.groups) await api(page, "DELETE", `/groups/${g}`);
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
    // M11: monitoring, the fleet and a host's tabs (fixture hosts), settings.
    ...monitoringPages(hosts),
    // M12: alerts, an alert's details, maintenance, notification settings.
    "/alerts", "/alerts?view=acknowledged", "/alerts?view=resolved", "/alerts?view=maintenance",
    ...(await alertPages()),
    "/settings/notifications", "/settings/notifications?advanced=1",
    ...(await automationPages()),
    ...(await desiredPages(hosts)),
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
    // M12.1: the fleet's health checks and a check's editor.
    await page.goto(base + "/settings/monitoring", { waitUntil: "networkidle0" });
    await sleep(600);
    const cust = await page.evaluateHandle(() => [...document.querySelectorAll("button")].find((b) => b.textContent === "Customize"));
    if (cust.asElement()) {
      await cust.asElement().click();
      await sleep(400);
      const t = await page.evaluateHandle(() => [...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === "Customize Temperature"));
      if (t.asElement()) {
        await t.asElement().click();
        await sleep(500);
        const low = await page.evaluate(lowContrast);
        if (low.length) failures.push(`health editor:\n  ${low.join("\n  ")}`);
        await shot(page, `${theme}-health-editor`);
        await page.keyboard.press("Escape");
      }
      const low = await page.evaluate(lowContrast);
      if (low.length) failures.push(`health checks:\n  ${low.join("\n  ")}`);
    }
    // M14: a conflict's details, Review difference and the restore plan.
    if (desiredFixture) {
      await page.goto(base + `/hosts/${desiredFixture.host}/desired`, { waitUntil: "networkidle0" });
      await sleep(800);
      // One resource's details are open at a time: the conflict, then a drift.
      let low;
      for (const [name, what] of [["ssh.service", "conflict"], ["nvidia-persistenced.service", "drift"]]) {
        const row = await page.evaluateHandle((n) => [...document.querySelectorAll('ul[aria-label="Managed resources"] > li > button')].find((b) => b.textContent.includes(n)), name);
        if (!row.asElement()) continue;
        await row.asElement().click();
        await sleep(600);
        low = await page.evaluate(lowContrast);
        if (low.length) failures.push(`desired ${what} details:\n  ${low.join("\n  ")}`);
        await shot(page, `${theme}-desired-${what}-details`);
      }
      const rv = await page.evaluateHandle(() => [...document.querySelectorAll("button")].find((b) => b.textContent === "Review difference"));
      if (rv.asElement()) {
        await rv.asElement().click();
        await sleep(1200);
        low = await page.evaluate(lowContrast);
        if (low.length) failures.push(`review difference:\n  ${low.join("\n  ")}`);
        await shot(page, `${theme}-desired-review`);
        await page.keyboard.press("Escape");
        await sleep(300);
      }
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
