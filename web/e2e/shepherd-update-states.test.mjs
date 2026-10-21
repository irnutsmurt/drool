// Each Shepherd's update state in words (Phase 1 usability, ARCHITECTURE-ALPHA
// §46.3): "Up to date" only after a release check that completed, a missing
// release is "No update available" (never "Can't update"), the reason is
// said once, and the Shepherds list and the host's Shepherd card show the
// state Shepherds → Updates judged, with Update… where an update can be
// installed now. The server's judgement has its own tests (internal/api
// TestShepherdUpdateStates); the update status, the Shepherds and the host's
// agent are stubbed here, and starting an update is answered without one.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, waitFor } from "./lib.mjs";

let browser;
let admin;
let real; // the real update status, as the template
let hostA; // web-1: an older Shepherd, a release available
let hostB; // server-a: on DROOL's version

before(async () => {
  browser = await launch();
  admin = await session(browser);
  const hosts = (await api(admin, "GET", "/hosts")).body.items;
  hostA = hosts.find((h) => h.name === "web-1");
  hostB = hosts.find((h) => h.name === "server-a");
  real = (await api(admin, "GET", "/agents/updates/status")).body;
});
after(async () => browser?.close());

const inner = (page, sel) => page.$eval(sel, (e) => e.innerText).catch(() => "");
const shep = (h, state, extra = {}) => ({ host_id: h.id, host_name: h.name, version: "v1.4.0", state, online: true, verified: false, ...extra });
const identity = (h, update) => ({
  id: `agt_${h.name}`, host_id: h.id, status: "active", key_fingerprint: "SHA256:e2e", enrolled_at: new Date().toISOString(),
  cert_not_after: new Date(Date.now() + 864e5 * 30).toISOString(), cert_renew_after: new Date(Date.now() + 864e5 * 20).toISOString(),
  recovered_at: null, revoked_at: null, agent_version: update.version, protocol: 1, protocol_min: 1, protocol_max: 1, compatible: true, compat_reason: "",
  hostname: h.name, os: "linux", arch: "amd64", ceiling: {}, effective: {}, root_equivalent: [], manifest_version: 1, epoch_reported: "", epoch_matches: true,
  last_seen_at: new Date().toISOString(), last_address: "10.0.0.1", connected: true, update_state: "", update_version: "", update_detail: "", update_at: null,
  compatibility: { status: update.state === "available" ? "update_recommended" : "compatible", summary: "works", missing: [], needed: [] },
  release_trust: null, update,
});

/** Serves the status and the Shepherds from st; records starts. */
async function stub(page, st) {
  const s = { starts: [] };
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const path = new URL(r.url()).pathname;
    const m = r.method();
    const json = (body, status = 200) => r.respond({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (m === "GET" && path === "/api/v1/agents/updates/status") return json(st);
    if (m === "GET" && path === "/api/v1/agents") return json({ items: st.shepherds.map((u) => identity(u.host_id === hostA.id ? hostA : hostB, u)) });
    if (m === "GET" && path === `/api/v1/hosts/${hostA.id}/agent`) {
      const u = st.shepherds.find((x) => x.host_id === hostA.id);
      return json({ management: { ...hostA.management, preferred: "agent", active: "agent", agent_state: "connected", blocked: false },
        identity: identity(hostA, u), sessions: [], state_versions: [], requested_caps: null, ssh_bypasses_ceiling: false,
        update_available: u.state === "available" ? u.target : undefined });
    }
    if (m === "POST" && path === "/api/v1/agents/rollouts") {
      s.starts.push(JSON.parse(r.postData() ?? (await r.fetchPostData())));
      return json({ id: "rol_e2e", version: "v1.5.0", status: "running" }, 201);
    }
    return r.continue();
  });
  return s;
}

const base_ = () => ({ ...real, counts: { total: 2, current: 0, behind: 0, updating: 0, waiting: 0, blocked: 0, unavailable: 0, unknown: 0, offline: 0 },
  blocked: [], problems: [], active: [], history: [], stopped: undefined, latest: undefined });

test("a development build: an older Shepherd has no update available — information, said once, not a failure", async () => {
  const page = await session(browser);
  try {
    await stub(page, { ...base_(), state: "unavailable", verified: false, target: "",
      summary: "1 Shepherd is older than this DROOL (v1.4.0-12-gabcdef0), and no release DROOL can install is newer: official Shepherd releases can't be used by this DROOL.",
      counts: { ...base_().counts, current: 1, unavailable: 1 },
      shepherds: [shep(hostA, "unavailable", { version: "v1.3.0", reason: "official Shepherd releases can't be used by this DROOL" }), shep(hostB, "current", { version: "v1.4.0-12-gabcdef0" })] });
    await page.goto(`${base}/agents?tab=updates`, { waitUntil: "networkidle0" });
    await page.waitForSelector('[data-testid="updates-overview"]');
    const top = await inner(page, '[data-testid="updates-overview"]');
    assert.match(top, /No update available\s*1 Shepherd is older than this DROOL/);
    assert.ok(!/Can't update|Needs attention/.test(top), top);
    assert.match(await inner(page, '[data-testid="updates-counts"]'), /1 Shepherd on this DROOL's version · 1 older, no update available/);
    assert.equal(await page.$('[data-testid="updates-attention"]'), null, "a missing release listed as needing attention");
    await shot(page, "shepherd-states-unavailable");
    // The Shepherds list says the same, per Shepherd, with the reason.
    await page.goto(`${base}/agents`, { waitUntil: "networkidle0" });
    await waitFor(page, async () => (await page.$$('[data-testid="shepherd-update-state"]')).length === 2, "the update states");
    const states = await page.$$eval('[data-testid="shepherd-update-state"]', (xs) => xs.map((x) => [x.dataset.state, x.innerText.replace(/\s+/g, " ").replace(" :", ":").trim(), x.getAttribute("title")]));
    assert.deepEqual(states.sort(), [["current", "On DROOL's version", null], ["unavailable", "No update available: official Shepherd releases can't be used by this DROOL", "official Shepherd releases can't be used by this DROOL"]].sort());
    assert.ok(!(await inner(page, "main")).includes("Update…"), "an update offered with nothing to install");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("verified: up to date, and Update… where a release can be installed — in the list and on the host's card", async () => {
  const page = await session(browser);
  try {
    const s = await stub(page, { ...base_(), state: "available", verified: true, target: "v1.5.0", summary: "An update to v1.5.0 is available for 1 Shepherd.",
      policy: { ...real.policy, policy: "manual" }, counts: { ...base_().counts, current: 1, behind: 1 },
      shepherds: [shep(hostA, "available", { target: "v1.5.0", verified: true }), shep(hostB, "current", { version: "v1.5.0", verified: true })] });
    await page.goto(`${base}/agents?tab=updates`, { waitUntil: "networkidle0" });
    await page.waitForSelector('[data-testid="updates-counts"]');
    assert.match(await inner(page, '[data-testid="updates-counts"]'), /1 Shepherd up to date · 1 to update/);
    await page.goto(`${base}/agents`, { waitUntil: "networkidle0" });
    await waitFor(page, async () => (await inner(page, "main")).includes("Update available: v1.5.0"), "the update state");
    assert.match(await inner(page, "main"), /Up to date/);
    await click(page, "main button", `Update… the Shepherd on ${hostA.name} to v1.5.0`);
    await waitFor(page, async () => (await inner(page, '[role="dialog"]')).includes(`Update the Shepherd on ${hostA.name}?`), "the confirmation");
    assert.deepEqual(s.starts, [], "started before the confirmation");
    await click(page, '[role="dialog"] button', "Update to v1.5.0");
    await waitFor(page, async () => s.starts.length === 1, "the update started");
    assert.deepEqual(s.starts[0], { version: "v1.5.0", host_ids: [hostA.id] });
    // The host's Shepherd card: one state, and the update one click away.
    await page.goto(`${base}/hosts/${hostA.id}/management`, { waitUntil: "networkidle0" });
    await page.waitForSelector('[data-testid="host-shepherd"]');
    const card = await inner(page, '[data-testid="host-shepherd"]');
    assert.equal(card.match(/update available/gi)?.length, 1, `one update badge: ${card}`);
    assert.ok(await page.$$eval('[data-testid="host-shepherd"] button.bg-accent', (bs) => bs.some((b) => b.textContent.trim() === "Update Shepherd to v1.5.0…")), "the primary action");
    await shot(page, "shepherd-states-host-card");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});
