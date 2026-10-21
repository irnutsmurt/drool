// Firewall workflow (corrections 2026-10-19 and 2026-10-21): a host's
// Firewall page shows the saved policy as the compiler resolves it for that
// host, says whether it is in force, lists the host's observed connections
// (with "Create policy from observed connection…"), and offers one next
// action — Review and apply…, which changes nothing until confirmed. For a
// host not in Enforce mode the review asks which mode it should have, with
// "Keep current mode" chosen: only Enforce, chosen, applies the policy (and
// switches the host); Off, Monitor or Guided change only the mode. Saving in
// the Designer applies nothing and changes no mode; it lists the hosts it
// affected and asks whether to review and apply now — or "Save and review
// application…" goes straight to the review. A rule that lands on no host
// (its destination names addresses, not hosts) says so.
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
  await waitFor(page, async () => !!(await page.$('[data-testid="fw-policy-status"]')), "the policy status")
    .catch((e) => { throw new Error(`${e.message}; page errors: ${JSON.stringify(page.errors ?? [])}`); });
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

/** Serves web-1's firewall state from plan() (other hosts' from plans[id],
 * and the hosts list from list(), when given), and answers an apply without
 * starting one. A host's mode change is refused unless puts is set: then it
 * is recorded (s.puts) and answered from the host's stubbed state. */
async function stub(page, { plan, plans = {}, list, puts = false }) {
  const s = { calls: [], puts: [] };
  const planOf = (path) => {
    const hid = path.slice("/api/v1/firewall/hosts/".length);
    return hid === id && plan ? plan() : plans[hid]?.();
  };
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const path = new URL(r.url()).pathname;
    const m = r.method();
    const json = (body, status = 200) => r.respond({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path.startsWith("/api/v1/") && m !== "GET") s.calls.push(`${m} ${path}`);
    if (m === "GET" && path.startsWith("/api/v1/firewall/hosts/") && planOf(path)) return json(planOf(path));
    if (m === "GET" && list && path === "/api/v1/firewall/hosts") return json(list());
    if (m === "POST" && path === "/api/v1/firewall/apply") {
      s.apply = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ id: "job_fw_e2e", name: "Apply firewall policy", status: "pending" }, 201);
    }
    if (m === "PUT" && path.startsWith("/api/v1/firewall/hosts/")) {
      if (!puts || !planOf(path)) return json({ title: "not in this test", status: 409 }, 409);
      const body = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      s.puts.push({ host: path.slice("/api/v1/firewall/hosts/".length), ...body });
      return json({ ...planOf(path), config: { ...planOf(path).config, ...body } });
    }
    return r.continue();
  });
  return s;
}
const choose = (page, host, mode) => page.select(`section[aria-label="${host}: review"] [data-testid="review-mode-choice"]`, mode);
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
    assert.match(panel, /web-1\s*Policy saved — not yet enforced \(mode Monitor: the review asks whether to switch it to Enforce\)\./);
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
    assert.match(await inner(page, '[data-testid="after-save-ask"]'), /Would you like to review and apply these changes now\?/);
    await click(page, '[data-testid="after-save"] button', "Review and apply to web-1…");
    // The mode is asked, Keep current mode chosen: nothing would happen yet.
    await waitFor(page, async () => !!(await page.$('[data-testid="review-mode-choice"]')), "the review");
    let review = await inner(page, '[data-testid="fw-apply-dialog"]');
    assert.match(review, /Review and apply the firewall policy to web-1/);
    assert.match(review, /Mode:\s*Monitor\s*DROOL observes this host but doesn't enforce the policy on it\. Would you like to configure it\?/);
    assert.equal(await page.$eval('[data-testid="review-mode-choice"]', (x) => x.value), "keep", "Keep current mode is chosen");
    assert.deepEqual(await page.$$eval('[data-testid="review-mode-choice"] option', (os) => os.map((o) => o.textContent)), [
      "Keep current mode (Monitor)", "Off — No firewall monitoring or enforcement.", "Guided — Observe traffic and provide policy guidance.",
      "Enforce — Apply firewall rules with rollback protection."]);
    assert.match(review, /Nothing changes on web-1: the saved policy isn't applied while it is in Monitor mode\./);
    assert.ok(!review.includes("Could affect connectivity"), "a review of an apply that wouldn't happen");
    assert.equal(await page.$eval('[data-testid="fw-apply-dialog"] footer button.bg-accent, [data-testid="fw-apply-dialog"] button.bg-accent', (b) => b.disabled), true, "nothing to confirm yet");
    await shot(page, "fw-review-mode-prompt");
    // Enforce, chosen: the switch is part of the review.
    await choose(page, "web-1", "enforce");
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Could affect connectivity"), "the review of the apply");
    review = await inner(page, '[data-testid="fw-apply-dialog"]');
    assert.match(review, /This is the review: nothing changes until you choose Switch to Enforce and apply\./);
    assert.match(review, /Monitor → Enforce\s*This apply switches the host to Enforce/);
    assert.match(review, /DROOL starts filtering this host's traffic with the saved policy's rules: ha-to-esphome/);
    assert.match(review, /DROOL's management path stays open/);
    // Rollback is decided by DROOL's own connection, not by other access.
    assert.match(review, /Automatic rollback protects only DROOL's own connection: while it keeps working, the change stays even where it blocks other access, such as your own SSH/);
    await shot(page, "fw-review-from-designer");
    assert.deepEqual(s.calls, [], "the review requested a change");
    await click(page, '[data-testid="fw-apply-dialog"] button', "Switch to Enforce and apply");
    await waitFor(page, async () => !!(await page.$('[data-testid="apply-started"]')), "started");
    assert.deepEqual(s.apply.host_ids, [id]);
    assert.deepEqual(s.apply.set_enforce_hosts, [id], "the switch to Enforce is part of the request, for this host");
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
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Would you like to configure it?"), "the review");
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Cancel");
    assert.equal(await page.$eval('[data-testid="review-mode-choice"]', (x) => x.value), "keep", "Keep current mode is chosen, not Enforce");
    // The mode choice, from the keyboard (the dialog keeps the focus).
    await tabTo(page, () => document.activeElement?.getAttribute("data-testid") === "review-mode-choice", "the mode choice");
    assert.match(await page.evaluate(() => document.activeElement?.labels?.[0]?.textContent ?? ""), /^Mode for web-1/, "its name starts with the visible label");
    await choose(page, "web-1", "enforce");
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Monitor → Enforce"), "the switch reviewed");
    const low = await page.evaluate(lowContrast);
    assert.deepEqual(low, [], "contrast in the review");
    await shot(page, "fw-host-review");
    await page.keyboard.press("Escape");
    await waitFor(page, async () => (await page.$('[data-testid="fw-apply-dialog"]')) === null, "closed");
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Review and apply policy…", "focus back on the button");
    assert.deepEqual(s.calls, [], "cancelled: nothing requested");
    // Enforce chosen under Mode and environment: its review opens with
    // Enforce chosen, the switch reviewed.
    await page.select('main select:has(option[value="guided"])', "enforce");
    await click(page, '[data-testid="fw-enforce-next"] ~ div button', "Review and apply policy…");
    await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Monitor → Enforce"), "the review with Enforce chosen");
    assert.equal(await page.$eval('[data-testid="review-mode-choice"]', (x) => x.value), "enforce");
    await page.keyboard.press("Escape");
    await waitFor(page, async () => (await page.$('[data-testid="fw-apply-dialog"]')) === null, "closed");
    await page.select('main select:has(option[value="guided"])', "monitor");
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

async function setYAML(page, y) {
  const ta = await page.$('textarea[aria-label="Firewall policy as YAML"]');
  await ta.focus();
  await page.keyboard.down("Control");
  await page.keyboard.press("a");
  await page.keyboard.up("Control");
  await page.keyboard.press("Backspace");
  await page.keyboard.sendCharacter(y);
}
const designerStatus = (page, want) => waitFor(page, async () => (await inner(page, '[data-testid="designer-status"]')).includes(want), `the designer: ${want}`, 20000);
const options = (page, host) => page.$$eval(`section[aria-label="${host}: review"] [data-testid="review-mode-choice"] option`,
  (os) => os.map((o) => [o.textContent, o.disabled]));

test("Save and review application…: hosts in different modes, each one's mode chosen in the review, only Enforce applied", async () => {
  const page = await session(browser);
  try {
    const all = (await api(page, "GET", "/hosts")).body.items;
    const [a, b] = ["server-a", "server-b"].map((n) => all.find((h) => h.name === n).id);
    const doc2 = doc + `  - name: https-in
    from: internal
    to: [web-1, server-a, server-b]
    allow: [https]
`;
    // The plans the save produces, for enforceable hosts in three modes:
    // web-1 Off, server-a Monitor, server-b Enforce.
    await putDoc(page, doc2);
    const modes = { [id]: "off", [a]: "monitor", [b]: "enforce" };
    const plans = {};
    for (const h of [id, a, b]) {
      const real = (await api(page, "GET", `/firewall/hosts/${h}`)).body;
      const st = enforceable(real, { config: { ...real.config, mode: modes[h] } });
      plans[h] = () => st;
    }
    await putDoc(page, doc);
    const list = (await api(page, "GET", "/firewall/hosts")).body;
    const s = await stub(page, { plans, puts: true,
      list: () => ({ ...list, items: list.items.map((h) => (plans[h.host_id] ? { ...h, mode: modes[h.host_id], supported: true, reason: "", transport: "ssh" } : h)) }) });
    await page.goto(`${base}/firewall?tab=designer&view=yaml`, { waitUntil: "networkidle0" });
    await designerStatus(page, "Saved");
    await setYAML(page, doc2);
    await designerStatus(page, "Unsaved changes");
    await click(page, "button", "Save and review application…");
    await waitFor(page, async () => (await page.$$('[data-testid="host-review"]')).length === 3, "the review of the three hosts", 20000);
    const sec = (n) => inner(page, `section[aria-label="${n}: review"]`);
    const review = await inner(page, '[data-testid="fw-apply-dialog"]');
    assert.match(review, /Review and apply the firewall policy to 3 hosts/);
    assert.match(await sec("web-1"), /Mode:\s*Off\s*Firewall management isn't enabled on this host\. Would you like to configure it\?/);
    assert.match(await sec("server-a"), /Mode:\s*Monitor\s*DROOL observes this host but doesn't enforce the policy on it\. Would you like to configure it\?/);
    assert.match(await sec("server-b"), /Mode: Enforce \(already set\)\./);
    assert.equal(await page.$(`section[aria-label="server-b: review"] [data-testid="review-mode-choice"]`), null, "a mode asked of a host in Enforce");
    assert.deepEqual(await options(page, "web-1"), [["Keep current mode (Off)", false], ["Monitor — Observe traffic without enforcing rules.", false],
      ["Guided — Observe traffic and provide policy guidance.", false], ["Enforce — Apply firewall rules with rollback protection.", false]]);
    // Kept modes by default: only server-b, already in Enforce, is applied.
    for (const n of ["web-1", "server-a"]) assert.equal(await page.$eval(`section[aria-label="${n}: review"] [data-testid="review-mode-choice"]`, (x) => x.value), "keep");
    assert.match(review, /nothing changes until you choose Apply policy\./);
    assert.match(await sec("server-a"), /server-a: stays Monitor, not applied/);
    await shot(page, "fw-review-mixed-modes");
    await choose(page, "web-1", "monitor");
    await choose(page, "server-a", "enforce");
    await waitText(page, "Switch to Enforce and apply to 2 hosts and change web-1's mode to Monitor");
    assert.match(await sec("web-1"), /When you confirm, web-1's mode changes to Monitor\. Nothing is applied or enforced\./);
    assert.match(await sec("server-a"), /server-a: switches from Monitor to Enforce/);
    assert.deepEqual([s.puts, s.apply], [[], undefined], "the review requested a change");
    await click(page, '[data-testid="fw-apply-dialog"] button', "Switch to Enforce and apply to 2 hosts and change web-1's mode to Monitor");
    await waitFor(page, async () => !!(await page.$('[data-testid="apply-started"]')), "started");
    assert.deepEqual(s.puts.map((x) => [x.host, x.mode]), [[id, "monitor"]], "mode changes");
    assert.deepEqual(s.apply.host_ids, [a, b], "applied to");
    assert.deepEqual(s.apply.set_enforce_hosts, [a], "switched to Enforce");
    assert.deepEqual(Object.keys(s.apply.expect_rules_hashes).sort(), [a, b].sort());
    const done = await inner(page, '[data-testid="apply-started"]');
    assert.match(done, /Mode changed: web-1: Off → Monitor\. Nothing was applied to that host/);
    assert.match(done, /Started: applying the saved policy to server-a, server-b, one host at a time\. server-a switches to Enforce when its checks have passed/);
    assert.deepEqual(page.errors, []);
  } finally {
    await putDoc(admin, doc).catch(() => {});
    await page.browserContext().close();
  }
});

/** Chooses in one of the rule builder's pickers: type, Enter on the match. */
async function pick(page, testId, query, label) {
  const sel = `[data-testid="${testId}"]`;
  await page.click(`${sel} input[role="combobox"]`);
  await page.type(`${sel} input[role="combobox"]`, query);
  await waitFor(page, () => page.$eval(`${sel} [role="option"][aria-selected="true"]`, (o, l) => o.innerText.includes(l), label).catch(() => false), `option ${label}`);
  await page.keyboard.press("Enter");
  await waitFor(page, () => page.$eval(`${sel} ul`, (u, l) => u.innerText.includes(l), label).catch(() => false), `chosen ${label}`);
}
const nftReason = "nftables (nft) is not installed: DROOL can install it (Install nftables…, under Firewall environment)";
/** web-1 as a host whose only obstacle to enforcing is a missing nftables. */
const needsNft = (p) => ({
  ...p, transport: "ssh", pending_transaction: undefined,
  environment: { ...(p.environment ?? {}), nft: "", nft_version: "" },
  eligibility: { supported: false, reasons: [nftReason], detected: [] }, environment_checked_at: new Date().toISOString(),
  summary: { ...p.summary, supported: false, needs_nftables: true, reason: nftReason, transport: "ssh" },
});

test("a rule from the host's page: one search per field, Save and review…, and the review offers nftables", async () => {
  const page = await session(browser);
  try {
    await putDoc(page, doc);
    const changed = record(page);
    const before = (await api(page, "GET", `/firewall/hosts/${id}`)).body;
    const list = (await api(page, "GET", "/firewall/hosts")).body;
    await firewallPage(page);
    // New rule… opens the builder with this host as the destination.
    await click(page, '[data-testid="fw-new-rule-for-host"]', "New rule…");
    await waitFor(page, async () => !!(await page.$('[data-testid="rule-dialog"]')), "the rule builder", 20000);
    assert.match(new URL(page.url()).search, /tab=designer/);
    assert.match(await inner(page, '[data-testid="rule-destination"]'), /Host\s*web-1/);
    // Source and service: one search each, no kind, value and Add button.
    await pick(page, "rule-source", "internal", "Internal networks");
    await pick(page, "rule-service", "ssh", "ssh");
    const summary = await inner(page, '[data-testid="rule-summary"]');
    assert.match(summary, /Allows ssh \(TCP 22\) from Internal networks to web-1\./);
    assert.match(summary, /Changes the firewall of web-1\./);
    assert.ok(!(await page.$('[data-testid="rule-no-host"]')), "a rule on a host warned as on no host");
    await shot(page, "fw-rule-builder");
    // From here web-1 is stubbed as needing only nftables (the containers
    // have no systemd either); the save itself is real.
    await stub(page, { plan: () => needsNft(before),
      list: () => ({ ...list, items: list.items.map((h) => (h.host_id === id ? { ...h, supported: false, needs_nftables: true, reason: nftReason, transport: "ssh" } : h)) }) });
    await click(page, '[data-testid="rule-dialog"] button', "Save and review…");
    await waitFor(page, async () => !!(await page.$('[data-testid="review-needs-nftables"]')), "the review, with nftables offered", 20000);
    const saved = (await api(admin, "GET", "/firewall/document")).body.document.policy;
    assert.deepEqual(saved.at(-1), { name: "allow-ssh-internal-to-web-1", description: "", enabled: true, action: "allow", from: ["internal"], to: ["host:web-1"], services: ["ssh"] });
    const step = await inner(page, '[data-testid="review-needs-nftables"]');
    assert.match(step, /nftables isn't installed on web-1, so DROOL can't enforce there yet\. DROOL can install it with web-1's package manager/);
    assert.ok(await page.$('[data-testid="review-needs-nftables"] button'), "Install nftables… in the review");
    // Enforce waits for it; nothing is chosen for the person.
    assert.equal(await page.$eval('[data-testid="review-mode-choice"]', (x) => x.value), "keep");
    assert.deepEqual((await options(page, "web-1")).find(([t]) => t.startsWith("Enforce")), ["Enforce — Apply firewall rules with rollback protection. (not possible now)", true]);
    assert.match(await inner(page, '[data-testid="fw-apply-dialog"]'), /Enforce isn't possible now: nftables isn't installed on this host yet\./);
    await shot(page, "fw-review-needs-nftables");
    await click(page, '[data-testid="fw-apply-dialog"] button', "Cancel");
    // The panel after saving says the same, and offers the review again.
    await waitFor(page, async () => /nftables isn't installed there yet: the review offers to install it\./.test(await inner(page, '[data-testid="after-save"]')), "the saved panel");
    assert.match(await inner(page, '[data-testid="after-save"]'), /Review and apply to web-1…/);
    assert.deepEqual(changed(), [], "a mode change or an apply was requested");
    assert.deepEqual(page.errors, []);
  } finally {
    await putDoc(admin, doc).catch(() => {});
    await page.browserContext().close();
  }
});

test("Off: the review offers to configure the host — Guided changes only the mode; Enforce isn't offered where it would be refused", async () => {
  const page = await session(browser);
  try {
    await putDoc(page, doc);
    const real = (await api(page, "GET", `/firewall/hosts/${id}`)).body;
    let state = enforceable(real, { config: { ...real.config, mode: "off" } });
    const s = await stub(page, { plan: () => state, puts: true });
    await firewallPage(page);
    await click(page, '[data-testid="fw-policy-status"] button', "Review and apply policy…");
    await waitFor(page, async () => !!(await page.$('[data-testid="review-mode-choice"]')), "the review");
    assert.match(await inner(page, '[data-testid="fw-apply-dialog"]'), /Mode:\s*Off\s*Firewall management isn't enabled on this host\. Would you like to configure it\?/);
    await choose(page, "web-1", "guided");
    await waitText(page, "Change web-1's mode to Guided");
    const review = await inner(page, '[data-testid="fw-apply-dialog"]');
    assert.match(review, /When you confirm, web-1's mode changes to Guided\. Nothing is applied or enforced\./);
    assert.ok(!/Automatic rollback protection|Could affect connectivity/.test(review), "an apply described that won't happen");
    await click(page, '[data-testid="fw-apply-dialog"] button', "Change web-1's mode to Guided");
    await waitFor(page, async () => !!(await page.$('[data-testid="apply-started"]')), "done");
    assert.deepEqual(s.puts, [{ host: id, mode: "guided", outbound: real.config.outbound ?? "allow", rollback_timeout_s: real.config.rollback_timeout_s ?? 0 }]);
    assert.equal(s.apply, undefined, "Guided applied the policy");
    assert.match(await inner(page, '[data-testid="apply-started"]'), /Mode changed: web-1: Off → Guided\. Nothing was applied to that host: Off, Monitor and Guided don't enforce\./);
    await page.keyboard.press("Escape");
    // A rule that would cut DROOL's management path: Enforce can't be
    // chosen (nor applied without the audited override).
    state = { ...state, policy: { ...state.policy, conflicts: [{ message: "policy \"lock\" would remove the management path: SSH management", policy_id: "p", policy_name: "lock", flow: {} }] } };
    await firewallPage(page);
    await click(page, '[data-testid="fw-policy-status"] button', "Review and apply policy…");
    await waitFor(page, async () => !!(await page.$('[data-testid="review-mode-choice"]')), "the review");
    const enforce = (await options(page, "web-1")).find(([t]) => t.startsWith("Enforce"));
    assert.deepEqual(enforce, ["Enforce — Apply firewall rules with rollback protection. (not possible now)", true]);
    assert.match(await inner(page, '[data-testid="review-mode-outcome"]'), /Enforce isn't possible now: a rule would cut DROOL's management path/);
    await page.keyboard.press("Escape");
    assert.deepEqual(s.calls, [`PUT /api/v1/firewall/hosts/${id}`], "requests");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("a rule whose destination is addresses, not a host, is on no host — said where it is written, saved and reviewed", async () => {
  const page = await session(browser);
  try {
    await putDoc(page, doc);
    const noHost = doc + `  - name: ssh-internal
    from: internal
    to: internal
    allow: [ssh]
`;
    await page.goto(`${base}/firewall?tab=designer&view=yaml`, { waitUntil: "networkidle0" });
    await designerStatus(page, "Saved");
    await setYAML(page, noHost);
    await designerStatus(page, "Unsaved changes");
    await waitFor(page, async () => (await inner(page, '[data-testid="no-host-rules"]')).includes("Rule ssh-internal applies to no host"), "the preview's warning");
    const w = await inner(page, '[data-testid="no-host-rules"]');
    assert.match(w, /Its destination, Internal networks, is a set of addresses, not a DROOL host: DROOL puts a rule on the hosts its destination names/);
    assert.match(w, /web-1[\s\S]*in Internal networks: to allow ssh to (it|them), make (it|them) the destination/);
    assert.ok(!(await text(page)).includes("Save and review application"), "offered a review of a draft that changes no host");
    await click(page, "button[aria-pressed]", "Rules");
    await waitText(page, "applies to no host");
    await click(page, "button", "Edit ssh-internal");
    await waitFor(page, async () => !!(await page.$('[data-testid="rule-no-host"]')), "the rule's note");
    assert.match(await inner(page, '[data-testid="rule-no-host"]'), /No host gets this rule: its destination, Internal networks, is addresses, not a DROOL host/);
    await shot(page, "fw-rule-no-host");
    await click(page, "button", "Cancel");
    await click(page, "button", "Save policy");
    await waitFor(page, async () => !!(await page.$('[data-testid="after-save"]')), "the saved panel", 20000);
    const panel = await inner(page, '[data-testid="after-save"]');
    assert.match(panel, /No host's firewall changes with this save\./);
    assert.match(panel, /Rule ssh-internal applies to no host/);
    // The host whose address it contains says why the rule isn't there.
    await firewallPage(page);
    const why = await inner(page, '[data-testid="fw-address-only"]');
    assert.match(why, /Rule ssh-internal isn't part of this host's policy: its destination, Internal networks, contains this host's address/);
    assert.match(why, /To allow ssh to web-1, make web-1 the destination\./);
    // Corrected — web-1 as destination — it is a change for web-1.
    const fixed = (await api(page, "POST", "/firewall/document/check", { yaml: doc + "  - name: ssh-internal\n    from: internal\n    to: web-1\n    allow: [ssh]\n" })).body;
    assert.deepEqual(fixed.no_host_rules, []);
    assert.ok(fixed.impact.some((im) => im.host_name === "web-1" && im.added.includes("allow inbound TCP 22 from internal networks (policy ssh-internal)")), JSON.stringify(fixed.impact));
    assert.deepEqual(page.errors, []);
  } finally {
    await putDoc(admin, doc).catch(() => {});
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
  const monitor = enforceable(real, { config: { ...real.config, mode: "monitor" } });
  for (const [w, h, mobile] of [[1440, 1000, false], [1280, 900, false], [1024, 800, false], [390, 844, true]]) {
    for (const theme of ["light", "dark"]) {
      const page = await session(browser, { width: w, height: h, mobile });
      let cur = state;
      try {
        await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
        await stub(page, { plan: () => cur });
        await firewallPage(page);
        await waitFor(page, async () => (await page.$$('[data-testid="fw-observed-tab"] [data-flow-id]')).length > 0, "observed connections");
        // Only the tab row may scroll sideways on a phone (DESIGN-STANDARD §6).
        const wide = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth,
          [...document.querySelectorAll("main .overflow-x-auto")].filter((e) => e.scrollWidth > e.clientWidth + 2 && e.getAttribute("role") !== "tablist").length]);
        assert.deepEqual(wide, [0, 0], `${w} ${theme}: sideways`);
        const low = await page.evaluate(lowContrast);
        assert.deepEqual(low, [], `${w} ${theme}: contrast`);
        if (w === 1440 || w === 390) await shot(page, `fw-host-${w}-${theme}`);
        // The review, with a mode to choose and Enforce chosen, fits too
        // (visually hidden captions and stacked tables' headers aside).
        cur = monitor;
        await firewallPage(page);
        await click(page, '[data-testid="fw-policy-status"] button', "Review and apply policy…");
        await waitFor(page, async () => !!(await page.$('[data-testid="review-mode-choice"]')), "the review");
        await choose(page, "web-1", "enforce");
        await waitFor(page, async () => (await inner(page, '[data-testid="fw-apply-dialog"]')).includes("Could affect connectivity"), "the review of the apply");
        const dlg = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth,
          [...document.querySelectorAll('[data-testid="fw-apply-dialog"], [data-testid="fw-apply-dialog"] *')].filter((e) => e.scrollWidth > e.clientWidth + 2 && getComputedStyle(e).overflowX !== "visible" && e.clientWidth > 1)
            .map((e) => `${e.tagName}.${e.className} ${e.scrollWidth}/${e.clientWidth} ${(e.innerText || "").slice(0, 40)}`)]);
        assert.deepEqual(dlg, [0, []], `${w} ${theme}: the review scrolls sideways`);
        assert.deepEqual(await page.evaluate(lowContrast), [], `${w} ${theme}: contrast in the review`);
        if (w === 1440 || w === 390) await shot(page, `fw-review-${w}-${theme}`);
        assert.deepEqual(page.errors, []);
      } finally {
        await page.browserContext().close();
      }
    }
  }
});
