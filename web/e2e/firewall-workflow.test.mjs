// Firewall workflow (correction 2026-10-19): a host's Firewall page shows
// the saved policy as the compiler resolves it for that host, says whether
// it is in force, lists the host's observed connections (with "Create policy
// from observed connection…"), and offers one next action — Review and
// apply…, which says when it switches the host to Enforce and changes
// nothing until confirmed. Saving in the Designer applies nothing and
// changes no mode; it lists the hosts it affected, with Review and apply….
//
// Needs test/integration TestBrowser's environment (web-1 and its
// observations, including TCP 6053 from 192.168.3.132). Its containers
// can't enforce (no nftables, no systemd): what needs an enforceable host
// stubs that host's firewall state and the apply call (the transactions
// themselves have their own tests: internal/api TestFirewallSaveReviewApply).
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, lowContrast, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

const doc = `services:
  esphome:
    protocol: tcp
    port: 6053
policy:
  - name: ha-to-esphome
    from: 192.168.3.132
    to: web-1
    allow: [esphome]
`;

let browser;
let admin;
let id;
let saved; // the document and web-1's settings before, restored after
let mode;
const run = Date.now().toString(36).slice(-5);
const viewer = { user: `e2e-fw-viewer-${run}`, password: "viewer-password-long" };
let viewerId;

const putDoc = async (page, yaml) => {
  const cur = await api(page, "GET", "/firewall/document");
  const r = await api(page, "PUT", "/firewall/document", { yaml, revision: cur.body.revision });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  return r.body;
};

before(async () => {
  browser = await launch();
  admin = await session(browser);
  id = (await api(admin, "GET", "/hosts")).body.items.find((h) => h.name === "web-1").id;
  saved = (await api(admin, "GET", "/firewall/document")).body;
  mode = (await api(admin, "GET", `/firewall/hosts/${id}`)).body.config;
  await putDoc(admin, doc);
  assert.equal((await api(admin, "PUT", `/firewall/hosts/${id}`, { mode: "monitor" })).status, 200);
  const u = await api(admin, "POST", "/users", { username: viewer.user, password: viewer.password, role: "viewer" });
  assert.equal(u.status, 201, JSON.stringify(u.body));
  viewerId = u.body.id;
  // The observations file is ingested shortly after the hosts exist.
  await waitFor(admin, async () => ((await api(admin, "GET", `/network/flows?host=${id}&port=6053&limit=1`)).body?.items ?? []).length > 0, "web-1's observations", 60000);
});
after(async () => {
  if (admin && saved) await putDoc(admin, saved.yaml).catch(() => {});
  if (admin && mode) await api(admin, "PUT", `/firewall/hosts/${id}`, mode).catch(() => {});
  if (admin && viewerId) await api(admin, "DELETE", `/users/${viewerId}`).catch(() => {});
  await browser?.close();
});

const firewallPage = async (page) => {
  await page.goto(`${base}/hosts/${id}/firewall`, { waitUntil: "networkidle0" });
  await waitFor(page, async () => !!(await page.$('[data-testid="fw-policy-status"]')), "the policy status");
  await sleep(300);
};
const inner = (page, sel) => page.$eval(sel, (e) => e.innerText).catch(() => "");
/** Firewall requests that change something: none may happen unasked. */
function record(page) {
  const calls = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/v1/") && r.method() !== "GET") calls.push(`${r.method()} ${u.pathname}`);
  });
  return () => calls.filter((c) => /^PUT \/api\/v1\/firewall\/hosts\/|^POST \/api\/v1\/firewall\/apply/.test(c));
}

/** Serves web-1's firewall state from plan() (and the hosts list from
 * list(), when given), and answers an apply without starting one. */
async function stub(page, { plan, list }) {
  const s = { calls: [] };
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const path = new URL(r.url()).pathname;
    const m = r.method();
    const json = (body, status = 200) => r.respond({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path.startsWith("/api/v1/") && m !== "GET") s.calls.push(`${m} ${path}`);
    if (m === "GET" && plan && path === `/api/v1/firewall/hosts/${id}`) return json(plan());
    if (m === "GET" && list && path === "/api/v1/firewall/hosts") return json(list());
    if (m === "POST" && path === "/api/v1/firewall/apply") {
      s.apply = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ id: "job_fw_e2e", name: "Apply firewall policy", status: "pending" }, 201);
    }
    if (m === "PUT" && path.startsWith("/api/v1/firewall/hosts/")) return json({ title: "not in this test", status: 409 }, 409);
    return r.continue();
  });
  return s;
}
/** web-1 as an enforceable host (plain nftables) in the given state. */
const enforceable = (p, extra = {}) => ({
  ...p, transport: "ssh", pending_transaction: undefined,
  environment: { ...(p.environment ?? {}), nft: "/usr/sbin/nft", nft_version: "nftables v1.0.9" },
  eligibility: { supported: true, reasons: [], detected: ["nftables v1.0.9"] }, environment_checked_at: new Date().toISOString(), ...extra,
});

test("the host's Firewall page: the saved rule as resolved for the host, not enforced, and its observed connections", async () => {
  const changed = record(admin);
  await firewallPage(admin);
  const plan = (await api(admin, "GET", `/firewall/hosts/${id}`)).body;
  const status = await inner(admin, '[data-testid="fw-policy-status"]');
  assert.match(status, /Policy saved — not yet enforced\./);
  assert.match(status, /Saved policy\s*1 rule for this host: ha-to-esphome/);
  assert.match(status, /Applied on the host\s*Nothing of DROOL's: DROOL doesn't filter this host's traffic\./);
  assert.match(status, /Observed traffic\s*\d+ connections seen/);
  // The containers can't enforce: said, and nothing offers to apply.
  if (plan.eligibility && !plan.eligibility.supported) {
    assert.match(status, /Applying isn't possible right now: DROOL can't enforce on this host/);
    assert.ok(!status.includes("Review and apply"), status);
  }
  // Never presented as enforced.
  const resolved = await inner(admin, '[data-testid="fw-resolved"]');
  assert.match(await text(admin), /Policy preview/);
  assert.match(resolved, /Nothing of it is enforced now\./);
  assert.ok(!/In force: revision|Applied policy/.test(await text(admin)), "nothing reads as in force");
  // The summary is the compiler's: every line of its Explain, by direction,
  // with the named service, the peer and the rule; the defaults as stated.
  for (const dir of ["in", "out"]) {
    const heading = dir === "in" ? "Inbound: connections to web-1" : "Outbound: connections from web-1";
    const rows = await admin.$$eval(`section[aria-label="${heading}"] tbody tr`, (trs) => trs.map((t) => t.innerText.replace(/\s+/g, " ")));
    const want = plan.explain.filter((r) => r.direction === dir && r.reason.kind !== "base" && r.reason.kind !== "default");
    assert.equal(rows.filter((r) => !r.includes("No rule for")).length, want.length, `${dir}: ${rows.join(" | ")}`);
    for (const w of want) {
      const svc = (w.services ?? []).length ? `${w.services.join(", ")} · ${w.service}` : w.service;
      assert.ok(rows.some((r) => r.includes(svc) && r.includes(w.peer) && (w.reason.kind !== "policy" || r.includes(`rule ${w.reason.policy_name}`))),
        `${dir} ${svc} ${w.peer}: ${rows.join(" | ")}`);
    }
    const def = plan.explain.find((r) => r.direction === dir && r.reason.kind === "default");
    const line = await inner(admin, `[data-testid="fw-default-${dir}"]`);
    assert.match(line, def.verdict === "drop" ? (dir === "in" ? /Everything not allowed below is dropped/ : /Restricted/) : /allowed unless a rule below denies it/, line);
  }
  assert.match(resolved, /esphome · TCP 6053[\s\S]*192\.168\.3\.132\/32[\s\S]*rule\s+ha-to-esphome/);

  // Observed connections are on the page, judged by the saved policy and
  // marked as not enforced; observation is not proof.
  const tab = await inner(admin, '[data-testid="fw-observed-tab"]');
  assert.match(tab, /Seeing a connection isn't proof that the firewall allowed it/);
  assert.match(tab, /Firewall: .*does not filter this host/);
  // Inbound only: the connection is on the first page whatever else the
  // other tests made web-1 observe.
  await click(admin, '[data-testid="fw-observed-tab"] button[aria-pressed]', "Inbound");
  const espRow = () => admin.evaluate(() => [...document.querySelectorAll('[data-testid="fw-observed-tab"] [data-flow-id]')]
    .find((r) => r.innerText.includes("192.168.3.132"))?.innerText ?? "");
  await waitFor(admin, async () => (await espRow()) !== "", "the connection from 192.168.3.132");
  const row = await espRow();
  assert.match(row, /Covered by policy/);
  assert.match(row, /not enforced/);
  assert.ok(!/✕\s*Denied/.test(await inner(admin, '[data-testid="fw-observed-tab"]')), "nothing reads as denied where DROOL doesn't filter");
  await shot(admin, "fw-host-preview");
  // Its details, beside the list, from the keyboard; Escape returns to the
  // row. The row is looked up afresh: a full-page screenshot narrows the
  // window for a moment, and the list switches to its phone layout and back
  // (rebuilding the rows) — as it would for a person turning a tablet.
  let opened = false;
  for (let i = 0; i < 5 && !opened; i++) {
    await admin.evaluate(() => [...document.querySelectorAll('[data-testid="fw-observed-tab"] [data-flow-id]')]
      .find((r) => r.innerText.includes("192.168.3.132"))?.querySelector("button")?.focus());
    await admin.keyboard.press("Enter");
    for (let j = 0; j < 15 && !opened; j++) {
      opened = !!(await admin.$('[data-testid="flow-details"]'));
      if (!opened) await sleep(200);
    }
  }
  assert.ok(opened, "the connection's details opened");
  const d = await inner(admin, '[data-testid="flow-details"]');
  for (const re of [/Direction\s*↘ Inbound/, /Source\s*192\.168\.3\.132/, /Destination\s*web-1/, /Protocol\s*TCP/, /Port\s*6053/, /Process\s*esphome/, /First seen/, /Last seen/,
    /Allowed by policy|Would be allowed by policy “ha-to-esphome”|Covered by policy/]) assert.match(d, re);
  await shot(admin, "fw-host-observed-details");
  await admin.keyboard.press("Escape");
  await waitFor(admin, async () => (await admin.$('[data-testid="flow-details"]')) === null, "details closed");
  assert.ok(await admin.evaluate(() => !!document.activeElement?.closest("[data-flow-id]")?.innerText.includes("192.168.3.132")), "focus back on the row");
  assert.deepEqual(changed(), [], "looking changed nothing");
  assert.deepEqual(admin.errors, []);
});

test("Monitor: a policy drafted from an observed connection, saved — nothing applied, no mode changed — then reviewed before anything changes", async () => {
  const page = await session(browser);
  try {
    const changed = record(page);
    const txs = (await api(page, "GET", `/firewall/transactions?host_id=${id}`)).body.items.length;
    await firewallPage(page);
    await click(page, '[data-testid="fw-observed-tab"] button[aria-pressed]', "Not in policy");
    await waitFor(page, async () => (await page.$$('[data-testid="fw-observed-tab"] [data-flow-id]')).length > 0, "connections not in the policy");
    const flow = (await api(page, "GET", `/network/flows?host=${id}&policy=unclassified&direction=in&limit=50`)).body.items.find((f) => f.port === 443) ??
      (await api(page, "GET", `/network/flows?host=${id}&policy=unclassified&limit=1`)).body.items[0];
    await page.evaluate((fid) => document.querySelector(`[data-testid="fw-observed-tab"] [data-flow-id="${fid}"] button`)?.click(), flow.id);
    await waitFor(page, async () => !!(await page.$('[data-testid="flow-details"]')), "details");
    await click(page, '[data-testid="flow-inspector"] button', "Create policy from observed connection");
    await waitText(page, "was added from an observed connection", 20000);
    await waitFor(page, async () => (await inner(page, '[data-testid="designer-status"]')).includes("Unsaved changes"), "a draft");
    assert.equal((await api(page, "GET", `/firewall/hosts/${id}`)).body.config.mode, "monitor", "drafting changed the mode");
    await sleep(500);
    await click(page, "button", "Save policy");
    await waitFor(page, async () => !!(await page.$('[data-testid="after-save"]')), "the saved panel", 20000);
    const panel = await inner(page, '[data-testid="after-save"]');
    assert.match(panel, /Saved\. Nothing has changed on any host yet\./);
    assert.match(panel, /web-1\s*Policy saved — not yet enforced \(mode Monitor: applying switches it to Enforce\)\./);
    await shot(page, "fw-designer-saved");
    // Saving applied nothing and changed no mode.
    const after = (await api(page, "GET", `/firewall/hosts/${id}`)).body;
    assert.equal(after.config.mode, "monitor");
    assert.equal(after.applied.enforced, false);
    assert.equal((await api(page, "GET", `/firewall/transactions?host_id=${id}`)).body.items.length, txs, "a transaction started");
    assert.deepEqual(changed(), [], "a mode change or an apply was requested");
    if (after.eligibility && !after.eligibility.supported) {
      // Here: no Review and apply…, and why.
      assert.match(panel, /DROOL can't enforce on this host/);
      assert.ok(!panel.includes("Review and apply"), panel);
    }

    // On an enforceable host: guided from the save to the review.
    const list = (await api(page, "GET", "/firewall/hosts")).body;
    const s = await stub(page, {
      plan: () => enforceable(after),
      list: () => ({ ...list, items: list.items.map((h) => (h.host_id === id ? { ...h, supported: true, reason: "", transport: "ssh" } : h)) }),
    });
    await waitFor(page, async () => (await inner(page, '[data-testid="after-save"]')).includes("Review and apply to web-1…"), "Review and apply", 15000);
    await click(page, '[data-testid="after-save"] button', "Review and apply to web-1…");
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Could affect connectivity"), "the review");
    const review = await inner(page, '[data-testid="fw-apply-dialog"]');
    assert.match(review, /Review and apply the firewall policy to web-1/);
    assert.match(review, /This is the review: nothing changes until you choose Switch to Enforce and apply\./);
    assert.match(review, /Mode:\s*Monitor → Enforce\s*This apply switches the host to Enforce/);
    assert.match(review, /DROOL starts filtering this host's traffic with the saved policy's rules: ha-to-esphome/);
    assert.match(review, /DROOL's management path stays open/);
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Cancel", "Cancel has the focus");
    await shot(page, "fw-review-from-designer");
    assert.deepEqual(s.calls, [], "the review requested a change");
    await click(page, '[data-testid="fw-apply-dialog"] button', "Switch to Enforce and apply");
    await waitFor(page, async () => !!(await page.$('[data-testid="apply-started"]')), "started");
    assert.deepEqual(s.apply.host_ids, [id]);
    assert.equal(s.apply.set_enforce, true, "the switch to Enforce is part of the request");
    assert.deepEqual(s.apply.expect_rules_hashes, { [id]: after.rendered.rules_hash }, "what was reviewed is what is applied");
    assert.match(await inner(page, '[data-testid="apply-started"]'), /web-1 switches to Enforce when its checks have passed/);
    assert.ok(!s.calls.some((c) => c.startsWith("PUT /api/v1/firewall/hosts")), "the mode was set separately");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("an enforceable host: one next action, Enforce explained, applied and saved policy kept apart", async () => {
  const page = await session(browser);
  try {
    await putDoc(page, doc);
    const real = (await api(page, "GET", `/firewall/hosts/${id}`)).body;
    const esp = real.explain.find((r) => r.reason.policy_name === "ha-to-esphome");
    const older = real.explain.filter((r) => r !== esp);
    let state = enforceable(real, { config: { ...real.config, mode: "monitor" } });
    const s = await stub(page, { plan: () => state });
    await firewallPage(page);
    // Not enforced: the next step, on its own.
    let status = await inner(page, '[data-testid="fw-policy-status"]');
    assert.match(status, /Policy saved — not yet enforced\./);
    const primary = await page.$$eval('[data-testid="fw-policy-status"] button', (bs) => bs.filter((b) => b.className.includes("bg-accent")).map((b) => b.textContent));
    assert.deepEqual(primary, ["Review and apply policy…"], "one primary action");
    // Choosing Enforce applies nothing and says what does.
    await page.select('main select:has(option[value="guided"])', "enforce");
    await waitFor(page, async () => !!(await page.$('[data-testid="fw-enforce-next"]')), "the explanation");
    assert.match(await inner(page, '[data-testid="fw-enforce-next"]'), /Selecting Enforce doesn't change anything on the host by itself: DROOL's rules take effect when the policy is applied/);
    assert.ok((await text(page)).includes("Save mode only"));
    await shot(page, "fw-host-select-enforce");
    assert.deepEqual(s.calls, [], "selecting Enforce requested a change");
    // From the keyboard: the status card's Review and apply…, Cancel, back.
    await page.select('main select:has(option[value="guided"])', "monitor");
    // Just above the status card: the section's own links.
    await page.evaluate(() => document.querySelector('main a[href="/firewall?tab=designer"]')?.focus());
    await tabTo(page, () => document.activeElement?.tagName === "BUTTON" && document.activeElement.textContent === "Review and apply policy…"
      && !!document.activeElement.closest('[data-testid="fw-policy-status"]'), "Review and apply policy…");
    await page.keyboard.press("Enter");
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Monitor → Enforce"), "the review");
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Cancel");
    const low = await page.evaluate(lowContrast);
    assert.deepEqual(low, [], "contrast in the review");
    await shot(page, "fw-host-review");
    await page.keyboard.press("Escape");
    await waitFor(page, async () => (await page.$('[data-testid="fw-apply-dialog"]')) === null, "closed");
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Review and apply policy…", "focus back on the button");
    assert.deepEqual(s.calls, [], "cancelled: nothing requested");
    // The mode changes underneath (an apply switched it): the form follows,
    // and never offers the old mode for saving.
    state = { ...state, config: { ...state.config, mode: "enforce" } };
    await waitFor(page, async () => (await page.$eval('main select:has(option[value="guided"])', (x) => x.value)) === "enforce", "the form showing Enforce", 15000);
    assert.equal(await page.$('[data-testid="fw-mode-keeps"]'), null);
    assert.ok(!(await page.$$eval("main button", (bs) => bs.map((b) => b.textContent))).includes("Save"), "a stale mode offered for saving");
    state = { ...state, config: { ...state.config, mode: "monitor" } };

    // In force, with saved changes not applied: both, marked.
    state = enforceable(real, {
      config: { ...real.config, mode: "enforce" }, applied_explain: older, changes_added: [`allow inbound ${esp.service} from ${esp.peer} (policy ha-to-esphome)`], changes_removed: [],
      applied: { ...real.applied, enforced: true, revision: 3, rules_changed: true, sets_pending: false, drift: "", status_at: new Date().toISOString() },
    });
    await firewallPage(page);
    status = await inner(page, '[data-testid="fw-policy-status"]');
    assert.match(status, /Saved changes not applied yet/);
    assert.match(status, /Applied on the host\s*Revision 3, which differs from the saved policy\./);
    assert.match(await text(page), /Saved policy — not applied yet/);
    let resolved = await inner(page, '[data-testid="fw-resolved"]');
    assert.match(resolved, /esphome · TCP 6053[\s\S]*↻\s*not applied yet/);
    await shot(page, "fw-host-changes-pending");
    await click(page, 'button[aria-pressed]', "In force now (revision 3)");
    await waitText(page, "What web-1 runs now: revision 3");
    resolved = await inner(page, '[data-testid="fw-resolved"]');
    assert.ok(!resolved.includes("esphome"), "the unapplied rule shown as in force");
    assert.match(await text(page), /Applied policy/);
    await click(page, '[data-testid="fw-policy-status"] button', "Review and apply changes…");
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("What changes"), "the review");
    const review = await inner(page, '[data-testid="fw-apply-dialog"]');
    assert.match(review, /Mode:\s*Enforce \(already set\)/);
    assert.match(review, /\+ allow inbound TCP 6053 from 192\.168\.3\.132\/32/);
    assert.match(review, /nothing changes until you choose Apply policy/);
    await page.keyboard.press("Escape");

    // In force and the same as saved; then changed outside DROOL: unknown.
    state = enforceable(real, { config: { ...real.config, mode: "enforce" }, applied_explain: real.explain, changes_added: [], changes_removed: [],
      applied: { ...real.applied, enforced: true, revision: 3, rules_changed: false, sets_pending: false, drift: "", status_at: new Date().toISOString() } });
    await firewallPage(page);
    status = await inner(page, '[data-testid="fw-policy-status"]');
    assert.match(status, /In force: revision 3/);
    assert.match(status, /Revision 3, the same as the saved policy/);
    assert.ok(status.includes("Apply again…") && !status.includes("Review and apply"), status);
    state = { ...state, applied: { ...state.applied, drift: "changed" } };
    await firewallPage(page);
    status = await inner(page, '[data-testid="fw-policy-status"]');
    assert.match(status, /Changed outside DROOL: what this host enforces isn't known/);
    assert.match(status, /Unknown: revision 3 was committed, then DROOL's rules were changed outside DROOL/);
    assert.match(await text(page), /Applied policy \(not verified\)/);
    assert.deepEqual(s.calls, [], "nothing requested");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("a viewer: the policy and its state, no actions, and why observed connections aren't shown", async () => {
  const page = await session(browser, { as: viewer });
  try {
    await firewallPage(page);
    const t = await text(page);
    assert.match(t, /Read-only:/);
    assert.match(t, /Policy saved — not yet enforced\.|Saved changes not applied yet|In force: revision/);
    assert.match(await inner(page, '[data-testid="fw-observed"]'), /Not shown: viewing observed connections needs the network\.read permission/);
    assert.match(await inner(page, '[data-testid="fw-observed-denied"]'), /needs the network\.read permission/);
    assert.ok(!/Review and apply|Apply again|Remove DROOL policy|Save mode only/.test(t), "an action for a viewer");
    assert.match(await inner(page, '[data-testid="fw-resolved"]'), /esphome · TCP 6053/);
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("layout: desktop and phone, both themes, no sideways scroll, readable", async () => {
  const real = (await api(admin, "GET", `/firewall/hosts/${id}`)).body;
  const esp = real.explain.find((r) => r.reason.policy_name === "ha-to-esphome");
  const state = enforceable(real, {
    config: { ...real.config, mode: "enforce" }, applied_explain: real.explain.filter((r) => r !== esp), changes_added: [], changes_removed: [],
    applied: { ...real.applied, enforced: true, revision: 3, rules_changed: true, sets_pending: false, drift: "", status_at: new Date().toISOString() },
  });
  for (const [w, h, mobile] of [[1440, 1000, false], [1280, 900, false], [1024, 800, false], [390, 844, true]]) {
    for (const theme of ["light", "dark"]) {
      const page = await session(browser, { width: w, height: h, mobile });
      try {
        await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
        await stub(page, { plan: () => state });
        await firewallPage(page);
        await waitFor(page, async () => (await page.$$('[data-testid="fw-observed-tab"] [data-flow-id]')).length > 0, "observed connections");
        // Only the tab row may scroll sideways on a phone (DESIGN-STANDARD §6).
        const wide = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth,
          [...document.querySelectorAll("main .overflow-x-auto")].filter((e) => e.scrollWidth > e.clientWidth + 2 && e.getAttribute("role") !== "tablist").length]);
        assert.deepEqual(wide, [0, 0], `${w} ${theme}: sideways`);
        const low = await page.evaluate(lowContrast);
        assert.deepEqual(low, [], `${w} ${theme}: contrast`);
        if (w === 1440 || w === 390) await shot(page, `fw-host-${w}-${theme}`);
        assert.deepEqual(page.errors, []);
      } finally {
        await page.browserContext().close();
      }
    }
  }
});
