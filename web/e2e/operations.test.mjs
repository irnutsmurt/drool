// Everyday operations where the information is (Phase 1 usability,
// ARCHITECTURE-ALPHA §46.2): Install updates is one review — each host's
// dry run, what follows, the recommended rollout for several hosts — whose
// button starts the ordinary system update run and whose progress shows in
// place; a service alert offers what fixes it (start, restart or stop the
// unit), confirmed and followed in the alert's inspector or page. The run
// API and the alert are stubbed here (the engine, the package dry run and
// the typed service operation have their own tests): nothing runs on a host.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, lowContrast, session, shot, sleep, waitFor, waitText } from "./lib.mjs";

let browser;
let admin;
let ids = {};

before(async () => {
  browser = await launch();
  admin = await session(browser);
  for (const h of (await api(admin, "GET", "/hosts")).body.items) ids[h.name] = h.id;
});
after(async () => browser?.close());

const inner = (page, sel) => page.$eval(sel, (e) => e.innerText).catch(() => "");
const simHost = (name, extra = {}) => ({ host_id: ids[name], host_name: name, package_manager: "apt", extra_removals: [],
  plan: { install: [{ name: "linux-image-6.1.0-54-amd64", to: "6.1.190-1" }], upgrade: [{ name: "python3.11", from: "3.11.2-6", to: "3.11.2-6+deb12u7" },
    { name: "ca-certificates", from: "20230311", to: "20230311+deb12u1" }], remove: [], downgrade: [] }, ...extra });

/** Answers the dry run and the run API on a page; records what was sent.
 * Once the run has ended, the hosts list says DROOL checked them again
 * after it (rechecked: after that many more requests for it). */
async function stubRuns(page, { sim, rechecked = 2 }) {
  const s = { calls: [], polls: 0, hostLists: 0 };
  const realHosts = (await api(page, "GET", "/hosts")).body;
  let finishedAt = null;
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const path = new URL(r.url()).pathname;
    const m = r.method();
    const json = (body, status = 200) => r.respond({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path.startsWith("/api/v1/") && m !== "GET") s.calls.push(`${m} ${path}`);
    if (m === "POST" && path === "/api/v1/jobs/simulate") {
      s.simulate = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ hosts: sim() });
    }
    if (m === "POST" && path === "/api/v1/jobs") {
      s.job = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ id: "job_ops_e2e", name: "System update", status: "pending" }, 201);
    }
    const hosts = () => (s.job?.targets.hosts ?? []).map((id) => ({ host_id: id, host_name: Object.keys(ids).find((n) => ids[n] === id), state: s.polls < 3 ? "running" : "succeeded",
      transport: "ssh", transport_note: "", current_step: 0, attempt: 1, exit_code: 0, error_class: "", error_message: "", error_hint: "", started_at: null, finished_at: finishedAt, wave: 0 }));
    if (m === "GET" && path === "/api/v1/hosts" && finishedAt) {
      // Before the recheck: the counts from before the run; after it, checked since.
      const after = ++s.hostLists > rechecked;
      return json({ ...realHosts, items: realHosts.items.map((h) => (s.job?.targets.hosts ?? []).includes(h.id)
        ? { ...h, health: { ...(h.health ?? {}), updates_available: after ? 0 : 6, security_updates: after ? 0 : 6,
          updates_checked_at: after ? new Date(Date.parse(finishedAt) + 2000).toISOString() : new Date(Date.parse(finishedAt) - 600000).toISOString() } } : h) });
    }
    if (m === "GET" && path === "/api/v1/jobs/job_ops_e2e") {
      s.polls++;
      const done = s.polls >= 3;
      if (done && !finishedAt) finishedAt = new Date().toISOString();
      return json({ id: "job_ops_e2e", name: "System update", status: done ? "completed" : "running", host_count: hosts().length,
        counts: done ? { succeeded: hosts().length } : { running: hosts().length } });
    }
    if (m === "GET" && path === "/api/v1/jobs/job_ops_e2e/hosts") return json({ items: hosts() });
    return r.continue();
  });
  return s;
}

test("Install updates from a host's header: one review with the dry run, the options, then progress in place", async () => {
  const page = await session(browser);
  try {
    const s = await stubRuns(page, { sim: () => [simHost("web-1")] });
    await page.goto(`${base}/hosts/${ids["web-1"]}`, { waitUntil: "networkidle0" });
    await click(page, '[data-testid="host-header"] button', "Install updates…");
    await page.waitForSelector('[data-testid="install-updates"]');
    // The review opens on the safe choice and says nothing has changed.
    assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim()), "Cancel");
    await waitFor(page, async () => /3 package changes on 1 of 1 host\./.test(await inner(page, '[data-testid="install-updates"]')), "the dry run");
    const review = await inner(page, '[data-testid="install-updates"]');
    assert.match(review, /Install updates on web-1\?/);
    assert.match(review, /This is the review: nothing changes until you choose Install updates on web-1\./);
    assert.match(review, /web-1\s*apt\s*2 to upgrade, 1 new/);
    assert.match(review, /No host reboots\. Where an update needs one, the host says so afterwards/);
    assert.ok(!review.includes("Rollout"), "a rollout for one host");
    assert.ok(!/canary/i.test(review), "a rollout warning for one host");
    assert.deepEqual(s.simulate.targets, { hosts: [ids["web-1"]] });
    assert.equal(s.simulate.step.type, "system_update");
    assert.ok(!s.calls.includes("POST /api/v1/jobs"), "nothing started before the confirmation");
    await shot(page, "ops-install-updates-review");
    // An option chosen here is what runs.
    await click(page, '[data-testid="install-updates-options"] summary', "Options");
    await page.select('[data-testid="install-updates-options"] select', "if_required");
    await waitText(page, "Hosts that need it reboot afterwards");
    assert.match(await inner(page, '[data-testid="install-updates-options"] summary'), /Options: Upgrade, refresh first, reboot if required/);
    await click(page, '[data-testid="install-updates"] button', "Install updates on web-1");
    await page.waitForSelector('[data-testid="install-updates-progress"]');
    assert.deepEqual(s.job.steps, [{ type: "system_update", mode: "upgrade", refresh: true, reboot: "if_required", allow_removals: false, timeout_s: 3600 }]);
    assert.deepEqual(s.job.targets, { hosts: [ids["web-1"]] });
    assert.equal(s.job.strategy.canary_count, undefined, "one host has no canary");
    // Progress, then the outcome in words, without leaving the host.
    await waitFor(page, async () => (await inner(page, '[data-testid="install-updates-summary"]')) === "web-1 is updated.", "the outcome", 20000);
    assert.match(await inner(page, '[data-testid="install-updates"] h2'), /^Updates installed on web-1$/, "the title still says installing");
    assert.match(await inner(page, '[data-testid="install-updates-host"]'), /web-1\s*Updated/);
    assert.ok(!/running|succeeded|completed/.test(await inner(page, '[data-testid="install-updates-progress"]')), "engine states in the words");
    // The count is the run's outcome only once DROOL checked the host again.
    assert.match(await inner(page, '[data-testid="pending-after"]'), /^Checking its pending updates again…$/);
    await waitFor(page, async () => /^No pending updates left \(checked /.test(await inner(page, '[data-testid="pending-after"]')), "the recheck", 20000);
    assert.ok(new URL(page.url()).pathname.endsWith(`/hosts/${ids["web-1"]}`), "the page changed");
    assert.ok(await page.$('[data-testid="install-updates"] a[href="/jobs/job_ops_e2e"]'), "View the run");
    await shot(page, "ops-install-updates-done");
    await click(page, '[data-testid="install-updates"] button', "Done");
    await waitFor(page, async () => !(await page.$('[data-testid="install-updates"]')), "closed");
    assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim()), "Install updates…", "focus back on the button");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("Hosts: several selected, Install updates… with the recommended rollout; removals need permission", async () => {
  const page = await session(browser);
  try {
    const s = await stubRuns(page, { sim: () => [simHost("web-1"), simHost("server-a", { extra_removals: [{ name: "old-tool", from: "1.0" }],
      plan: { install: [], upgrade: [{ name: "libc6", from: "2.36-9", to: "2.36-9+deb12u9" }], remove: [{ name: "old-tool", from: "1.0" }], downgrade: [] } })] });
    await page.goto(`${base}/hosts`, { waitUntil: "networkidle0" });
    for (const n of ["web-1", "server-a"]) {
      await page.evaluate((n) => [...document.querySelectorAll("main tr, main [data-row]")].find((r) => r.innerText.includes(n))?.querySelector('input[type="checkbox"]')?.click(), n);
    }
    await waitText(page, "2 selected");
    await click(page, "main button", "Install updates…");
    await page.waitForSelector('[data-testid="install-updates"]');
    await waitFor(page, async () => /server-a would remove other packages/.test(await inner(page, '[data-testid="install-updates"]')), "the removal");
    const review = await inner(page, '[data-testid="install-updates"]');
    assert.match(review, /Install updates on 2 hosts\?/);
    assert.match(review, /Without your permission, the run stops there before changing anything\./);
    assert.match(await inner(page, '[data-testid="install-updates-rollout"]'), /Rollout \(recommended\): 1 canary host first, then batches of 5, up to 5 at a time, pause after 2 failures\./);
    assert.match(review, /open it in the run composer/);
    await click(page, '[data-testid="install-updates"] label', "Allow these removals");
    await click(page, '[data-testid="install-updates"] button', "Install updates on 2 hosts");
    await page.waitForSelector('[data-testid="install-updates-progress"]');
    assert.equal(s.job.steps[0].allow_removals, true, "the removals allowed");
    assert.deepEqual(s.job.strategy, { concurrency: 5, failure_threshold: 2, canary_count: 1, batch_size: 5 });
    assert.deepEqual([...s.job.targets.hosts].sort(), [ids["web-1"], ids["server-a"]].sort());
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

/** A failed-service alert for nas-01's backup.service (the fixture shows the
 * unit failed), served in place of the real alerts. */
async function stubServiceAlert(page) {
  // Every field the API returns (openapi Alert), so the list and the page
  // render as they would for a real one.
  const now = Date.now();
  const alert = { id: "alr_ops_e2e", host_id: ids["nas-01"], host_name: "nas-01", issue_id: "iss_ops_e2e", check: "service_failed", subject: "backup.service",
    category: "services", severity: "critical", peak_severity: "critical", state: "active", title: "backup.service failed", detail: "backup.service failed (exit-code)",
    value_text: "failed", observed_at: now - 600000, issue_started_at: now - 600000, opened_at: now - 540000, duration_ms: 540000, suppressed: false,
    ever_suppressed: false, notification: { status: "none", delivered: 0, pending: 0, failed: 0, cancelled: 0, retrying: false } };
  const list = { items: [], next_cursor: "" };
  const detail = { timeline: [{ ts: now - 540000, kind: "opened", text: "Opened: backup.service failed" }], deliveries: [], occurrences: [] };
  const s = { calls: [], polls: 0 };
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const u = new URL(r.url());
    const m = r.method();
    const json = (body, status = 200) => r.respond({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (u.pathname.startsWith("/api/v1/") && m !== "GET") s.calls.push(`${m} ${u.pathname}`);
    if (m === "GET" && u.pathname === "/api/v1/alerts") return json({ ...list, items: [alert], next_cursor: "" });
    if (m === "GET" && u.pathname === "/api/v1/alerts/alr_ops_e2e") return json({ ...detail, alert });
    if (m === "POST" && u.pathname === "/api/v1/jobs") {
      s.job = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ id: "job_svc_e2e", name: s.job.name, status: "pending" }, 201);
    }
    if (m === "GET" && u.pathname === "/api/v1/jobs/job_svc_e2e") return json({ id: "job_svc_e2e", status: ++s.polls < 2 ? "running" : "completed", counts: {}, host_count: 1 });
    if (m === "GET" && u.pathname === `/api/v1/jobs/job_svc_e2e/hosts/${ids["nas-01"]}`) {
      return json({ host_id: ids["nas-01"], host_name: "nas-01", state: "succeeded", steps: [{ index: 0, result: { after: { active: "active", sub: "running", unit_file: "static" } } }] });
    }
    if (m === "POST" && u.pathname.endsWith("/monitoring/refresh")) return json({});
    return r.continue();
  });
  return s;
}

test("a failed-service alert offers Start backup.service…, confirmed and followed in its inspector", async () => {
  const page = await session(browser);
  try {
    // A fresh controller loads the monitoring fixture shortly after start.
    await waitFor(admin, async () => ((await api(admin, "GET", `/hosts/${ids["nas-01"]}/monitoring/services?filter=all&all=true`)).body?.units ?? [])
      .some((u) => u.name === "backup.service"), "nas-01's services (the monitoring fixture)", 120000);
    const s = await stubServiceAlert(page);
    await page.goto(`${base}/alerts`, { waitUntil: "networkidle0" });
    await page.evaluate(() => [...document.querySelectorAll("main button")].find((b) => b.innerText.includes("backup.service failed") || b.getAttribute("aria-label")?.includes("backup.service"))?.click());
    await page.waitForSelector('[data-testid="alert-inspector"]');
    await waitFor(page, async () => (await inner(page, '[data-testid="alert-actions"]')).includes("Start backup.service…"), "the fix offered", 20000);
    await click(page, '[data-testid="alert-actions"] button', "Start backup.service…");
    // A step inside the inspector (never a dialog on top), on the confirmation.
    await waitFor(page, async () => (await inner(page, '[data-testid="alert-inspector"]')).includes("Start backup.service on nas-01?"), "the confirmation");
    assert.equal(await page.$('[role="dialog"][data-testid="service-action"]'), null);
    assert.ok(!s.calls.includes("POST /api/v1/jobs"), "nothing ran before the confirmation");
    await shot(page, "ops-alert-service-fix");
    await click(page, '[data-testid="alert-inspector"] [data-testid="service-action"] button', "Start");
    await waitFor(page, async () => /Start: done\. DROOL checked on nas-01: backup\.service is now active \(running\)/.test(await inner(page, '[data-testid="alert-inspector"]')), "done", 20000);
    assert.deepEqual(s.job.steps, [{ type: "service", name: "backup.service", state: "running" }]);
    assert.deepEqual(s.job.targets, { hosts: [ids["nas-01"]] });
    // The alert's own page offers the same, as its primary action.
    await page.goto(`${base}/alerts/alr_ops_e2e`, { waitUntil: "networkidle0" });
    await waitFor(page, async () => !!(await page.$('[data-testid="alert-service-fix"]')), "the page's action", 20000);
    assert.equal((await inner(page, '[data-testid="alert-service-fix"]')).trim(), "Start backup.service…");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("layout: the Install updates review fits at 1440, 1024 and 390 px, in both themes", async () => {
  for (const [w, h, mobile] of [[1440, 1000, false], [1024, 800, false], [390, 844, true]]) {
    for (const theme of ["light", "dark"]) {
      const page = await session(browser, { width: w, height: h, mobile });
      try {
        await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
        await stubRuns(page, { sim: () => [simHost("web-1")] });
        await page.goto(`${base}/hosts/${ids["web-1"]}`, { waitUntil: "networkidle0" });
        if (mobile) {
          await click(page, '[data-testid="host-actions"]', "Actions");
          await click(page, '[role="menuitem"]', "Install updates…");
        } else {
          await click(page, '[data-testid="host-header"] button', "Install updates…");
        }
        await waitFor(page, async () => /3 package changes/.test(await inner(page, '[data-testid="install-updates"]')), "the dry run");
        await sleep(300);
        const wide = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth,
          [...document.querySelectorAll('[data-testid="install-updates"] *')].filter((e) => e.scrollWidth > e.clientWidth + 2 && getComputedStyle(e).overflowX !== "visible" && e.clientWidth > 1).length]);
        assert.deepEqual(wide, [0, 0], `${w} ${theme}: sideways`);
        assert.deepEqual(await page.evaluate(lowContrast), [], `${w} ${theme}: contrast`);
        if (w === 390) await shot(page, `ops-install-updates-${w}-${theme}`);
      } finally {
        await page.browserContext().close();
      }
    }
  }
});
