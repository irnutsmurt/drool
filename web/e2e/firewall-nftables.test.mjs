// A host without nftables (firewall environment check): DROOL offers to
// install it instead of sending the user to a separate package run
// (DESIGN-STANDARD §1, Workflow first). Check stays read-only; Install
// nftables… previews the package run, asks, runs it, shows progress and the
// outcome, and checks the environment again — without touching the mode or
// enforcing anything. Monitor mode presents the missing dependency as
// information, Enforce as a problem. Where installing isn't possible, it
// says why. The host's firewall state and the run API are stubbed (the
// package engine has its own tests): nothing is installed on web-1.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, waitFor, waitText } from "./lib.mjs";

let browser;
let admin;
let id;
let real;
const reason = "nftables (nft) is not installed: DROOL can install it (Install nftables…, under Firewall environment)";
const pkgStep = { type: "package", action: "install", names: ["nftables"] };

before(async () => {
  browser = await launch();
  admin = await session(browser);
  id = (await api(admin, "GET", "/hosts")).body.items.find((h) => h.name === "web-1").id;
  real = (await api(admin, "GET", `/firewall/hosts/${id}`)).body;
});
after(async () => browser?.close());

const plan = (mode, installed) => ({
  ...real,
  config: { ...real.config, mode },
  applied: { ...real.applied, enforced: false, drift: "" },
  environment: { ...(real.environment ?? {}), nft: installed ? "/usr/sbin/nft" : "", nft_version: installed ? "nftables v1.0.9" : "" },
  eligibility: installed ? { supported: true, reasons: [], detected: ["nftables v1.0.9"] } : { supported: false, reasons: [reason], detected: [] },
  environment_checked_at: new Date().toISOString(),
});
const simHost = (extra) => ({ host_id: id, host_name: "web-1", package_manager: "apt", extra_removals: [],
  plan: { install: [{ name: "nftables", to: "1.0.9-1" }, { name: "libnftables1", to: "1.0.9-1" }], upgrade: [], remove: [], downgrade: [] }, ...extra });

/** Stubs the host's firewall state and the run API on a page; records the
 * API calls it sees. */
async function stub(page, { mode, sim = simHost(), recheckFails = false }) {
  const s = { calls: [], bodies: {}, installed: false, polls: 0 };
  await page.setRequestInterception(true);
  page.on("request", async (r) => {
    const path = new URL(r.url()).pathname;
    const m = r.method();
    const json = (body, status = 200) => r.respond({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path.startsWith("/api/v1/")) s.calls.push(`${m} ${path}`);
    if (m === "GET" && path === `/api/v1/firewall/hosts/${id}`) return json(plan(mode, s.installed));
    if (m === "POST" && path === "/api/v1/jobs/simulate") {
      s.bodies.simulate = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ hosts: [sim] });
    }
    if (m === "POST" && path === "/api/v1/jobs") {
      s.bodies.job = JSON.parse(r.postData() ?? (await r.fetchPostData()));
      return json({ id: "job_nft_e2e", name: s.bodies.job.name, status: "pending" }, 201);
    }
    if (m === "GET" && path === "/api/v1/jobs/job_nft_e2e") return json({ id: "job_nft_e2e", status: ++s.polls < 3 ? "running" : "succeeded" });
    if (m === "GET" && path === `/api/v1/jobs/job_nft_e2e/hosts/${id}`) return json({ host_id: id, state: "succeeded" });
    if (m === "POST" && path === `/api/v1/firewall/hosts/${id}/detect`) {
      if (s.bodies.job && recheckFails) {
        return json({ title: "Gateway Timeout", status: 502, detail: "the firewall check timed out", error_class: "TimeoutError", retryable: true }, 502);
      }
      if (s.bodies.job) s.installed = true; // the check after the installation finds it
      return json({});
    }
    return r.continue();
  });
  return s;
}

const firewallPage = async (page) => {
  await page.goto(`${base}/hosts/${id}/firewall`, { waitUntil: "domcontentloaded" });
  await waitText(page, "Firewall environment");
};

test("nftables missing in Monitor: information, a read-only Check, and Install nftables… end to end", async () => {
  const page = await session(browser);
  try {
    const s = await stub(page, { mode: "monitor" });
    await firewallPage(page);
    // Information, not a failure: Monitor mode doesn't need it.
    await page.waitForSelector('[data-testid="fw-env-info"]');
    const info = await page.$eval('[data-testid="fw-env-info"]', (e) => e.innerText);
    assert.match(info, /Enforcement isn't available here yet\. Monitor mode doesn't need it: monitoring, simulation and explanation work/);
    assert.match(info, /nftables \(nft\) is not installed: DROOL can install it/);
    assert.equal(await page.$eval('[data-testid="fw-env-info"]', (e) => e.closest('[role="alert"]') === null && e.getAttribute("role") === null), true, "not an alert");
    // The setting and what is in force are told apart.
    assert.match(await page.$eval('[data-testid="fw-in-force"]', (e) => e.innerText), /^In force on the host: nothing of DROOL's\. The mode is what DROOL is set to do\.$/);

    // Check only inspects the host.
    await click(page, "main button", "Check");
    await waitFor(page, async () => s.calls.includes(`POST /api/v1/firewall/hosts/${id}/detect`), "the check");
    await sleep(500);
    assert.ok(!s.calls.some((c) => c.startsWith("POST /api/v1/jobs")), `Check started a run: ${s.calls.join(", ")}`);

    // Install nftables…: the package manager's dry run, then the confirmation.
    await click(page, '[data-testid="nft-install"] button', "Install nftables…");
    await page.waitForSelector('[data-testid="nft-install-dialog"]');
    await waitFor(page, async () => /libnftables1/.test(await page.$eval('[data-testid="nft-install-dialog"]', (e) => e.innerText)), "the dry run's plan");
    const dialog = await page.$eval('[data-testid="nft-install-dialog"]', (e) => e.innerText);
    assert.match(dialog, /DROOL installs the nftables package with web-1's package manager \(apt\)/);
    assert.match(dialog, /the firewall mode stays Monitor, nothing is enforced, and DROOL checks the firewall environment again/);
    assert.deepEqual(s.bodies.simulate, { targets: { hosts: [id] }, step: pkgStep });
    assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim()), "Cancel", "the focus starts on the safe choice");
    assert.ok(!s.calls.includes("POST /api/v1/jobs"), "nothing started before the confirmation");

    await click(page, '[data-testid="nft-install-dialog"] button', "Install nftables");
    await waitFor(page, async () => !(await page.$('[data-testid="nft-install-dialog"]')), "the dialog closes");
    assert.deepEqual(s.bodies.job.steps, [pkgStep]);
    assert.deepEqual(s.bodies.job.targets, { hosts: [id] });
    assert.match(s.bodies.job.name, /^Install nftables on web-1/);
    // Progress, then the outcome, then the environment checked again.
    await waitText(page, "Installing nftables on web-1…");
    await waitText(page, "nftables is installed.", 20000);
    await waitText(page, "Enforcement is supported here: plain nftables, no other firewall manager.", 20000);
    assert.equal(s.calls.filter((c) => c === `POST /api/v1/firewall/hosts/${id}/detect`).length, 2, "the environment was checked again once");
    assert.match(await page.$eval('[data-testid="nft-install"]', (e) => e.innerText), /nftables is installed\. The environment was checked again\. View the run/);
    // Nothing else: no mode change, no apply.
    assert.ok(!s.calls.some((c) => c.startsWith(`PUT /api/v1/firewall/hosts/${id}`) || c.includes("/apply")), s.calls.join(", "));
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("a check after the installation that doesn't finish isn't reported as done", async () => {
  const page = await session(browser);
  try {
    await stub(page, { mode: "monitor", recheckFails: true });
    await firewallPage(page);
    await click(page, '[data-testid="nft-install"] button', "Install nftables…");
    await waitFor(page, async () => /libnftables1/.test(await page.$eval('[data-testid="nft-install-dialog"]', (e) => e.innerText)), "the plan");
    await click(page, '[data-testid="nft-install-dialog"] button', "Install nftables");
    await waitFor(page, async () => /nftables is installed\. Checking the environment again didn't finish: choose Check\./.test(
      await page.$eval('[data-testid="nft-install"]', (e) => e.innerText)), "the unfinished check said so", 20000);
    assert.ok(!/The environment was checked again/.test(await page.$eval('[data-testid="nft-install"]', (e) => e.innerText)));
  } finally {
    await page.browserContext().close();
  }
});

test("nftables missing in Enforce: a problem, with the same action", async () => {
  const page = await session(browser);
  try {
    await stub(page, { mode: "enforce" });
    await firewallPage(page);
    await waitFor(page, async () => /Enforce is set, but DROOL can't enforce on this host: nothing is filtered until this is resolved/.test(
      await page.$$eval('[role="alert"]', (a) => a.map((x) => x.innerText).join("\n"))), "the problem");
    assert.match(await page.$eval('[data-testid="fw-in-force"]', (e) => e.innerText), /Enforce takes effect when the policy is applied/);
    assert.ok(await page.$('[data-testid="nft-install"] button'), "Install nftables…");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
  }
});

test("where installing isn't possible, the dialog says why and offers nothing to click", async () => {
  for (const [sim, why] of [
    [simHost({ skipped: "No supported package manager (apt or dnf) is known for this host.", plan: undefined }), /DROOL can't install it on web-1: No supported package manager/],
    [simHost({ extra_removals: [{ name: "iptables-persistent" }] }), /would also remove iptables-persistent\. DROOL doesn't do that from here/],
  ]) {
    const page = await session(browser);
    try {
      const s = await stub(page, { mode: "monitor", sim });
      await firewallPage(page);
      // The keyboard path: Enter opens it, Escape closes it, focus comes back.
      await page.waitForSelector('[data-testid="nft-install"] button');
      await page.focus('[data-testid="nft-install"] button');
      await page.keyboard.press("Enter");
      await page.waitForSelector('[data-testid="nft-install-dialog"]');
      await waitFor(page, async () => why.test(await page.$eval('[data-testid="nft-install-dialog"]', (e) => e.innerText)), String(why));
      const buttons = await page.$$eval('[data-testid="nft-install-dialog"] button', (b) => b.map((x) => x.textContent.trim()));
      assert.ok(!buttons.includes("Install nftables"), buttons.join(", "));
      await page.keyboard.press("Escape");
      await waitFor(page, async () => !(await page.$('[data-testid="nft-install-dialog"]')), "closed");
      await waitFor(page, async () => (await page.evaluate(() => document.activeElement?.textContent.trim())) === "Install nftables…", "focus back on the action");
      assert.ok(!s.calls.includes("POST /api/v1/jobs"), "nothing started");
      assert.deepEqual(page.errors, []);
    } finally {
      await page.browserContext().close();
    }
  }
});

test("without the permissions, the action isn't offered and the page says why", async () => {
  const op = { user: "nft-operator", password: "a-long-operator-password-2" };
  const made = await api(admin, "POST", "/users", { username: op.user, password: op.password, role: "operator" });
  const page = await session(browser, { as: op });
  try {
    await stub(page, { mode: "monitor" });
    await firewallPage(page);
    await page.waitForSelector('[data-testid="nft-install-unavailable"]');
    assert.match(await page.$eval('[data-testid="nft-install-unavailable"]', (e) => e.innerText), /needs permission to run jobs and to manage the firewall: ask an administrator/);
    assert.ok(!(await page.$$eval("main button", (b) => b.some((x) => x.textContent.trim() === "Install nftables…"))), "no action offered");
    assert.deepEqual(page.errors, []);
  } finally {
    await page.browserContext().close();
    if (made.body?.id) await api(admin, "DELETE", `/users/${made.body.id}`);
  }
});

test("the action and its dialog fit at 1440, 1280, 1024 and 390 px, in both themes", async () => {
  for (const theme of ["light", "dark"]) {
    for (const width of [1440, 1280, 1024, 390]) {
      const page = await session(browser, { width, height: 900, mobile: width < 600 });
      try {
        await page.evaluate((t) => localStorage.setItem("drool.theme", t), theme);
        await stub(page, { mode: "monitor" });
        await firewallPage(page);
        await page.waitForSelector('[data-testid="nft-install"] button');
        const sideways = () => page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
        assert.ok((await sideways()) <= 0, `${theme} ${width}: the page scrolls sideways`);
        await page.$eval('[data-testid="nft-install"]', (e) => e.scrollIntoView({ block: "center" }));
        await shot(page, `nft-env-${width}-${theme}`);
        await click(page, '[data-testid="nft-install"] button', "Install nftables…");
        await waitFor(page, async () => /libnftables1/.test(await page.$eval('[data-testid="nft-install-dialog"]', (e) => e.innerText)), "the plan");
        const box = await page.$eval('[role="dialog"]', (d) => { const r = d.getBoundingClientRect(); return { left: r.left, right: r.right, vw: innerWidth }; });
        assert.ok(box.left >= 0 && box.right <= box.vw, `${theme} ${width}: the dialog leaves the window ${JSON.stringify(box)}`);
        assert.ok((await sideways()) <= 0, `${theme} ${width}: sideways with the dialog`);
        await shot(page, `nft-dialog-${width}-${theme}`);
        assert.deepEqual(page.errors, []);
      } finally {
        await page.browserContext().close();
      }
    }
  }
});
