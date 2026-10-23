// Phase 1's new UI at the Design Standard's widths and themes (§6, §9):
// the firewall rule builder with its search open, the review of a host
// that needs nftables with the installation's confirmation over it, the
// Install updates review and its outcome, a service alert's fix in the
// alert's panel, and the Shepherd update states (Updates, the Shepherds
// list, a host's card). At 1440, 1280, 1024 and 390 px (a touch phone), in
// light and dark: nothing scrolls sideways or is cut off, text reaches its
// contrast, touch targets are 44 px on the phone, focus is visible and
// goes where it should. Firewall state, runs, the alert and the update
// status are stubbed (each has its own tests): nothing changes on a host.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, lowContrast, session, shot, sleep, waitFor } from "./lib.mjs";

let browser;
let admin;
const ids = {};
let realPlan;
let realList;
let realStatus;

const widths = [[1440, 1000, false], [1280, 900, false], [1024, 800, false], [390, 844, true]];
const themes = ["light", "dark"];
const nftReason = "nftables (nft) is not installed: DROOL can install it (Install nftables…, under Firewall environment)";

before(async () => {
  browser = await launch();
  admin = await session(browser);
  for (const h of (await api(admin, "GET", "/hosts")).body.items) ids[h.name] = h.id;
  realPlan = (await api(admin, "GET", `/firewall/hosts/${ids["web-1"]}`)).body;
  realList = (await api(admin, "GET", "/firewall/hosts")).body;
  realStatus = (await api(admin, "GET", "/agents/updates/status")).body;
  // A fresh controller loads the monitoring fixture shortly after it starts.
  await waitFor(admin, async () => ((await api(admin, "GET", `/hosts/${ids["nas-01"]}/monitoring/services?filter=all&all=true`)).body?.units ?? [])
    .some((u) => u.name === "backup.service"), "nas-01's services (the monitoring fixture)", 120000);
});
after(async () => browser?.close());

const inner = (page, sel) => page.$eval(sel, (e) => e.innerText).catch(() => "");

/** Layout, contrast, touch and clipping inside scope (a selector). */
async function check(page, scope, what, mobile) {
  const r = await page.evaluate((scope, mobile) => {
    const root = document.querySelector(scope) ?? document.body;
    const visible = (e) => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden" && !e.closest(".sr-only");
    const sideways = document.documentElement.scrollWidth - innerWidth;
    // Cut off: content wider than its box, where the box doesn't scroll.
    const clipped = [root, ...root.querySelectorAll("*")].filter((e) => visible(e) && e.clientWidth > 1 && e.scrollWidth > e.clientWidth + 2
      && !["auto", "scroll"].includes(getComputedStyle(e).overflowX) && getComputedStyle(e).overflowX !== "visible" && !e.closest("pre, code"))
      .map((e) => `${e.tagName}.${String(e.className).slice(0, 40)} ${e.scrollWidth}/${e.clientWidth}`);
    // Outside the window (to the right): nothing to see or press there,
    // except in a row that scrolls sideways (local navigation on a phone, §6).
    const scrolls = (e) => { for (let a = e.parentElement; a; a = a.parentElement) {
      if (["auto", "scroll"].includes(getComputedStyle(a).overflowX) && a.scrollWidth > a.clientWidth) return true; } return false; };
    const outside = [...root.querySelectorAll("button, input, select, a, [role=option]")].filter(visible)
      .filter((e) => { const b = e.getBoundingClientRect(); return (b.right > innerWidth + 1 || b.left < -1) && !scrolls(e); })
      .map((e) => (e.innerText || e.getAttribute("aria-label") || e.tagName).slice(0, 40));
    // Touch: what a finger taps is 44 px tall (icon-only buttons wide too);
    // links inside sentences are exempt.
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

/** The focused element shows it (an outline or a ring). Buttons show it
 * for the keyboard (:focus-visible), not after a click or tap. */
const focusShown = (page) => page.evaluate(() => {
  const e = document.activeElement;
  if (!e || e === document.body) return "nothing focused";
  const s = getComputedStyle(e);
  return s.outlineStyle !== "none" && parseFloat(s.outlineWidth) > 0 || s.boxShadow !== "none" ? "" : `no focus indicator on ${e.tagName} ${e.innerText?.slice(0, 30)}`;
});
const press = async (page, handle, mobile) => {
  const el = handle.asElement ? handle.asElement() : handle;
  if (!el) throw new Error("nothing to press");
  return mobile ? el.tap() : el.click();
};

/** Stubs web-1's firewall state (needing only nftables), the dry run and
 * the runs, the alert, and the Shepherd update status. */
async function stubAll(page) {
  const s = { polls: 0, finishedAt: null };
  const plan = { ...realPlan, transport: "ssh", pending_transaction: undefined, config: { ...realPlan.config, mode: "monitor" },
    applied: { ...realPlan.applied, enforced: false, drift: "" }, environment: { ...(realPlan.environment ?? {}), nft: "", nft_version: "" },
    eligibility: { supported: false, reasons: [nftReason], detected: [] }, environment_checked_at: new Date().toISOString(),
    summary: { ...realPlan.summary, mode: "monitor", supported: false, needs_nftables: true, reason: nftReason, transport: "ssh", status: "not_enforced" } };
  const sim = (id, name) => ({ host_id: id, host_name: name, package_manager: "apt", extra_removals: [],
    plan: { install: [{ name: "nftables", to: "1.0.6-2+deb12u2" }, { name: "libnftables1", to: "1.0.6-2+deb12u2" }],
      upgrade: [{ name: "python3.11", from: "3.11.2-6", to: "3.11.2-6+deb12u7" }], remove: [], downgrade: [] } });
  const now = Date.now();
  const alert = { id: "alr_p1_ui", host_id: ids["nas-01"], host_name: "nas-01", issue_id: "iss_p1_ui", check: "service_failed", subject: "backup.service",
    category: "services", severity: "critical", peak_severity: "critical", state: "active", title: "backup.service failed", detail: "backup.service failed (exit-code)",
    value_text: "failed", observed_at: now - 600000, issue_started_at: now - 600000, opened_at: now - 540000, duration_ms: 540000, suppressed: false,
    ever_suppressed: false, notification: { status: "none", delivered: 0, pending: 0, failed: 0, cancelled: 0, retrying: false } };
  const status = { ...realStatus, state: "unknown", verified: false, target: "",
    official: { ...realStatus.official, state: "development", detail: "This DROOL (v1.4.0-12-gabcdef0) is a development build, not an official release: there are no official Shepherd releases for it." },
    summary: "1 Shepherd is older than this DROOL (v1.4.0-12-gabcdef0), and whether a release can update it isn't known: this DROOL can't check official Shepherd releases.",
    counts: { total: 2, current: 1, behind: 0, updating: 0, waiting: 0, blocked: 0, unavailable: 0, unknown: 1, offline: 0 }, blocked: [], problems: [], active: [], history: [],
    shepherds: [{ host_id: ids["web-1"], host_name: "web-1", version: "v1.3.0", state: "unknown", reason: "this DROOL can't check official Shepherd releases", online: true, verified: false },
      { host_id: ids["server-a"], host_name: "server-a", version: "v1.4.0-12-gabcdef0", state: "current", online: true, verified: false }] };
  const identity = (h, u) => ({ id: `agt_${h}`, host_id: ids[h], status: "active", key_fingerprint: "SHA256:e2e", enrolled_at: new Date().toISOString(),
    cert_not_after: new Date(now + 864e5 * 30).toISOString(), cert_renew_after: new Date(now + 864e5 * 20).toISOString(), recovered_at: null, revoked_at: null,
    agent_version: u.version, protocol: 1, protocol_min: 1, protocol_max: 1, compatible: true, compat_reason: "", hostname: h, os: "linux", arch: "amd64",
    ceiling: {}, effective: {}, root_equivalent: [], manifest_version: 1, epoch_reported: "", epoch_matches: true, last_seen_at: new Date().toISOString(),
    last_address: "10.0.0.1", connected: true, update_state: "", update_version: "", update_detail: "", update_at: null,
    compatibility: { status: "compatible", summary: "works", missing: [], needed: [] }, release_trust: null, update: u });
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const u = new URL(r.url());
    const m = r.method();
    const p = u.pathname;
    const json = (body, code = 200) => r.respond({ status: code, contentType: "application/json", body: JSON.stringify(body) });
    if (m === "GET" && p === `/api/v1/firewall/hosts/${ids["web-1"]}`) return json(plan);
    if (m === "GET" && p === "/api/v1/firewall/hosts") return json({ ...realList, items: realList.items.map((h) => (h.host_id === ids["web-1"] ? plan.summary : h)) });
    if (m === "POST" && p === "/api/v1/jobs/simulate") {
      const b = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ hosts: (b.targets.hosts ?? []).map((id) => sim(id, Object.keys(ids).find((n) => ids[n] === id))) });
    }
    if (m === "POST" && p === "/api/v1/jobs") {
      s.job = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      s.polls = 0;
      s.finishedAt = null;
      return json({ id: "job_p1_ui", name: "Run", status: "pending" }, 201);
    }
    const jobHosts = () => (s.job?.targets.hosts ?? []).map((id) => ({ host_id: id, host_name: Object.keys(ids).find((n) => ids[n] === id),
      state: s.polls < 2 ? "running" : "succeeded", transport: "ssh", transport_note: "", current_step: 0, attempt: 1, exit_code: 0, error_class: "",
      error_message: "", error_hint: "", started_at: null, finished_at: s.finishedAt, wave: 0 }));
    if (m === "GET" && p === "/api/v1/jobs/job_p1_ui") {
      s.polls++;
      if (s.polls >= 2 && !s.finishedAt) s.finishedAt = new Date().toISOString();
      return json({ id: "job_p1_ui", name: "Run", status: s.polls >= 2 ? "completed" : "running", host_count: jobHosts().length,
        counts: s.polls >= 2 ? { succeeded: jobHosts().length } : { running: jobHosts().length } });
    }
    if (m === "GET" && p === "/api/v1/jobs/job_p1_ui/hosts") return json({ items: jobHosts() });
    if (m === "GET" && p.startsWith("/api/v1/jobs/job_p1_ui/hosts/")) {
      return json({ ...jobHosts()[0], steps: [{ index: 0, result: { after: { active: "active", sub: "running", unit_file: "static" } } }] });
    }
    if (m === "POST" && p === `/api/v1/firewall/hosts/${ids["web-1"]}/detect`) return json({});
    if (m === "GET" && p === "/api/v1/alerts") return json({ items: [alert], next_cursor: "" });
    if (m === "GET" && p === "/api/v1/alerts/alr_p1_ui") return json({ alert, timeline: [{ ts: now - 540000, kind: "opened", text: "Opened" }], deliveries: [], occurrences: [] });
    if (m === "POST" && p.endsWith("/monitoring/refresh")) return json({});
    if (m === "GET" && p === "/api/v1/agents/updates/status") return json(status);
    if (m === "GET" && p === "/api/v1/agents") return json({ items: status.shepherds.map((x) => identity(x.host_name, x)) });
    if (m === "GET" && p === `/api/v1/hosts/${ids["web-1"]}/agent`) {
      return json({ management: { preferred: "agent", fallback: "ssh", ssh_configured: true, active: "agent", agent_state: "connected", blocked: false },
        identity: identity("web-1", status.shepherds[0]), sessions: [], state_versions: [], requested_caps: null, ssh_bypasses_ceiling: false });
    }
    return r.continue();
  });
  return s;
}

for (const [w, h, mobile] of widths) {
  for (const theme of themes) {
    test(`Phase 1 UI at ${w} px, ${theme}`, async () => {
      const page = await session(browser, { width: w, height: h, mobile });
      const tag = `${w}-${theme}`;
      try {
        await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
        await stubAll(page);

        // The rule builder, its search open, then a choice made.
        await page.goto(`${base}/firewall?tab=designer&view=rules&rule=new&to=${encodeURIComponent("host:web-1")}`, { waitUntil: "networkidle0" });
        await page.waitForSelector('[data-testid="rule-dialog"]');
        const src = await page.$('[data-testid="rule-source"] input[role="combobox"]');
        await press(page, src, mobile);
        await page.type('[data-testid="rule-source"] input[role="combobox"]', "inter");
        await page.waitForSelector('[data-testid="rule-source"] [role="listbox"]');
        assert.equal(await focusShown(page), "", `${tag}: the search's focus`);
        await check(page, '[data-testid="rule-dialog"]', `${tag} rule builder, search open`, mobile);
        await shot(page, `p1-rule-builder-search-${tag}`);
        await page.keyboard.press("Enter");
        await press(page, await page.$('[data-testid="rule-service"] input[role="combobox"]'), mobile);
        await page.type('[data-testid="rule-service"] input[role="combobox"]', "ssh");
        await sleep(300);
        await page.keyboard.press("Enter");
        await page.$eval('[data-testid="rule-advanced"] summary', (e) => e.click());
        await waitFor(page, async () => /Allows ssh \(TCP 22\) from Internal networks to web-1\./.test(await inner(page, '[data-testid="rule-summary"]')), "the rule in words");
        await check(page, '[data-testid="rule-dialog"]', `${tag} rule builder, filled`, mobile);
        await shot(page, `p1-rule-builder-${tag}`);
        await page.keyboard.press("Escape");

        // The review of a host that needs nftables, and the installation's
        // confirmation over it.
        await page.goto(`${base}/hosts/${ids["web-1"]}/firewall`, { waitUntil: "networkidle0" });
        await page.waitForSelector('[data-testid="fw-policy-status"]');
        await click(page, '[data-testid="fw-policy-status"] button', "Review and apply policy…");
        await page.waitForSelector('[data-testid="review-needs-nftables"]');
        await check(page, '[data-testid="fw-apply-dialog"]', `${tag} review with nftables`, mobile);
        await shot(page, `p1-review-nftables-${tag}`);
        // From the keyboard on a desktop (where the focus ring shows: §6), by
        // touch on the phone.
        const install = await page.$('[data-testid="review-needs-nftables"] button');
        if (mobile) await install.tap();
        else {
          await install.focus();
          await page.keyboard.press("Enter");
        }
        await waitFor(page, async () => /libnftables1/.test(await inner(page, '[data-testid="nft-install-dialog"]')), "the dry run over the review");
        assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim()), "Cancel", `${tag}: the confirmation starts on Cancel`);
        if (!mobile) assert.equal(await focusShown(page), "", `${tag}: Cancel's focus`);
        await check(page, '[data-testid="nft-install-dialog"]', `${tag} nftables over the review`, mobile);
        await shot(page, `p1-nft-over-review-${tag}`);
        await page.keyboard.press("Escape");
        await waitFor(page, async () => !(await page.$('[data-testid="nft-install-dialog"]')), "the confirmation closed");
        assert.ok(await page.$('[data-testid="fw-apply-dialog"]'), `${tag}: Escape closed the review too`);
        assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim()), "Install nftables…", `${tag}: focus back on Install nftables…`);
        await page.keyboard.press("Escape");

        // Install updates: the review, then the outcome.
        await page.goto(`${base}/hosts/${ids["web-1"]}`, { waitUntil: "networkidle0" });
        if (mobile) {
          await click(page, '[data-testid="host-actions"]', "Actions");
          await press(page, await page.evaluateHandle(() => [...document.querySelectorAll('[role="menuitem"]')].find((x) => x.textContent.startsWith("Install updates"))), mobile);
        } else {
          await click(page, '[data-testid="host-header"] button', "Install updates…");
        }
        await waitFor(page, async () => /package changes on 1 of 1 host/.test(await inner(page, '[data-testid="install-updates"]')), "the dry run");
        assert.ok(!/canary/i.test(await inner(page, '[data-testid="install-updates"]')), `${tag}: a rollout warning for one host`);
        await page.$eval('[data-testid="install-updates-options"] summary', (e) => e.click());
        await check(page, '[data-testid="install-updates"]', `${tag} Install updates review`, mobile);
        await shot(page, `p1-install-updates-review-${tag}`);
        await click(page, '[data-testid="install-updates"] button', "Install updates on web-1");
        await waitFor(page, async () => (await inner(page, '[data-testid="install-updates"] h2')) === "Updates installed on web-1", "the outcome");
        await check(page, '[data-testid="install-updates"]', `${tag} Install updates outcome`, mobile);
        await shot(page, `p1-install-updates-done-${tag}`);
        await page.keyboard.press("Escape");

        // A service alert's fix, in the alert's panel.
        await page.goto(`${base}/alerts`, { waitUntil: "networkidle0" });
        await waitFor(page, async () => !!(await page.$('button[aria-label^="Actions for backup.service failed"]')), "the alert's row");
        await press(page, await page.$('button[aria-label^="Actions for backup.service failed"]'), mobile);
        await waitFor(page, async () => (await inner(page, '[data-testid="alert-actions"]')).includes("Start backup.service…"), "the fix offered", 30000);
        await check(page, '[data-testid="alert-inspector"]', `${tag} alert panel with the fix`, mobile);
        const fix = await page.evaluateHandle(() => [...document.querySelectorAll('[data-testid="alert-actions"] button')].find((b) => b.innerText.startsWith("Start backup.service…")));
        await press(page, fix, mobile);
        await waitFor(page, async () => (await inner(page, '[data-testid="alert-inspector"]')).includes("Start backup.service on nas-01?"), "the step");
        assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim()), "Start", `${tag}: focus in the step, on its action`);
        await check(page, '[data-testid="alert-inspector"]', `${tag} the fix's step`, mobile);
        await shot(page, `p1-alert-fix-${tag}`);
        await page.keyboard.press("Escape");
        await waitFor(page, async () => (await inner(page, '[data-testid="alert-actions"]')).includes("Start backup.service…"), "back to the choices");
        assert.match(await page.evaluate(() => document.activeElement?.innerText ?? ""), /^Start backup\.service…/, `${tag}: focus back on the choice`);
        await page.keyboard.press("Escape");

        // Shepherd update states.
        await page.goto(`${base}/agents?tab=updates`, { waitUntil: "networkidle0" });
        await page.waitForSelector('[data-testid="updates-overview"]');
        await check(page, "main", `${tag} Shepherds → Updates`, mobile);
        await shot(page, `p1-shepherd-updates-${tag}`);
        await page.goto(`${base}/agents`, { waitUntil: "networkidle0" });
        await waitFor(page, async () => (await page.$$('[data-testid="shepherd-update-state"]')).length === 2, "the update states");
        await check(page, "main", `${tag} Shepherds list`, mobile);
        await shot(page, `p1-shepherds-${tag}`);
        await page.goto(`${base}/hosts/${ids["web-1"]}/management`, { waitUntil: "networkidle0" });
        await waitFor(page, async () => /Not checked/.test(await inner(page, '[data-testid="host-shepherd"]')), "the host's Shepherd card");
        await check(page, '[data-testid="host-shepherd"]', `${tag} host's Shepherd card`, mobile);
        assert.deepEqual(page.errors, []);
      } finally {
        await page.browserContext().close();
      }
    });
  }
}
