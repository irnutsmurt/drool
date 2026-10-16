// Adding a host with DROOL's own SSH account (After M17): the wizard's
// account choice, a password typed for the setup only, the host key, the
// connection test as the setup account, the plan, confirming who you are,
// and the switch; then the host page and Credentials afterwards. Needs the
// host TestBrowser starts for it (DROOL_E2E_ACCOUNT_HOST="port fingerprint",
// DROOL_E2E_ACCOUNT_PASSWORD: pwuser signs in with it and sudo asks for it).
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { api, base, launch, session, text, waitText, waitFor, field, fill, click, shot, sleep } from "./lib.mjs";

const spec = process.env.DROOL_E2E_ACCOUNT_HOST;
const setupPassword = process.env.DROOL_E2E_ACCOUNT_PASSWORD ?? "";
const adminPassword = process.env.DROOL_E2E_PASSWORD ?? "a-long-admin-password";
let browser, page;

before(async () => {
  if (!spec) return;
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

test("adding a host with a dedicated DROOL SSH account", { skip: !spec && "needs DROOL_E2E_ACCOUNT_HOST" }, async () => {
  const [port, fingerprint] = spec.split(" ");
  await page.goto(base + "/hosts/new", { waitUntil: "networkidle0" });
  // The recommended choice comes first and is chosen; what it means is said
  // before anything happens.
  const t0 = await text(page);
  for (const want of ["Create a dedicated DROOL SSH account", "Recommended", "Use this SSH account", "4. DROOL account",
    "DROOL won't delete or change the account you use for the setup", "root-equivalent administrative capability"]) {
    assert.ok(t0.includes(want), "connection step lacks: " + want);
  }
  await fill(page, "Name", "acct-browser");
  await fill(page, "Address", "127.0.0.1");
  await fill(page, "SSH port", port);
  await fill(page, "Existing SSH account", "pwuser");
  await fill(page, "Password", setupPassword);
  await fill(page, "Sudo password", setupPassword);
  assert.equal(await (await field(page, "DROOL's account name")).evaluate((x) => x.value), "drool");
  await shot(page, "addhost-connection");
  await click(page, "main button", "Continue");

  // The host key, checked against the fingerprint read on the host.
  await waitText(page, "Trust this key", 30000);
  await fill(page, "Paste the expected fingerprint to highlight the matching key", fingerprint);
  await waitText(page, "matches expected");
  await click(page, "main button", "Trust this key");

  // The test signs in as the setup account and becomes root (a container
  // has no systemd: usable all the same, so the wizard goes on).
  await waitText(page, "sudo with password", 60000);
  await click(page, "main button", "Continue");

  // The plan: what changes, what DROOL forgets, what stays.
  await waitText(page, "On acct-browser, DROOL will", 60000);
  const plan = await text(page);
  for (const want of ["Create the Linux account drool", "/etc/sudoers.d/drool-account-drool", "brand-new connection",
    "DROOL forgets setup-acct-browser-login", "DROOL forgets setup-acct-browser-sudo",
    "DROOL won't delete or change the account it uses for the setup (pwuser)", "root-equivalent"]) {
    assert.ok(plan.includes(want), "plan lacks: " + want);
  }
  await shot(page, "addhost-plan");
  await sleep(3500); // past the test controller's re-authentication window
  await click(page, "main button", "Create drool and switch");
  await page.waitForSelector('[data-testid="reauth-dialog"]');
  await fill(page, "Password", adminPassword);
  await click(page, '[data-testid="reauth-dialog"] button', "Confirm");
  await waitText(page, "acct-browser now signs in as drool", 120000);
  const done = await text(page);
  assert.ok(done.includes("created now") && done.includes("worked, and so did sudo without a password"), done);
  assert.equal((done.match(/forgotten/g) ?? []).length, 2, "both setup credentials forgotten: " + done);
  await shot(page, "addhost-done");

  // The host's workspace: it signs in as drool (Management), and the setup
  // isn't offered again.
  await click(page, "main button", "Done");
  await waitFor(page, async () => page.url().includes("/hosts/host_"), "the host page");
  await click(page, '[data-testid="host-sections"] a', "Management");
  await waitText(page, "drool@127.0.0.1", 30000);
  await waitText(page, "passwordless sudo");
  assert.ok(!(await text(page)).includes("Set up a dedicated DROOL SSH account"));
  // Nothing entered for the setup is left under Credentials.
  await page.goto(base + "/credentials", { waitUntil: "networkidle0" });
  const creds = await text(page);
  assert.ok(!creds.includes("setup-acct-browser") && !creds.includes("for setup only"), creds);
  assert.deepEqual(page.errors, []);
});

test("removing that host cleans up DROOL's account on it", { skip: !spec && "needs DROOL_E2E_ACCOUNT_HOST" }, async () => {
  const hosts = await page.evaluate(async () => (await (await fetch("/api/v1/hosts")).json()).items);
  const h = hosts.find((x) => x.name === "acct-browser");
  assert.ok(h, "acct-browser was added");
  // A schedule that runs only on it: removing the host takes it out, and
  // the schedule, left with nothing to run on, is disabled and says why.
  const sch = await api(page, "POST", "/schedules", { name: "acct-browser updates", time_zone: "UTC",
    recurrence: { kind: "weekly", time: "03:00", weekdays: ["sun"] }, targets: { hosts: [h.id] }, steps: [{ type: "command", command: "true" }] });
  assert.equal(sch.status, 201, JSON.stringify(sch.body));
  await page.goto(`${base}/hosts/${h.id}/management`, { waitUntil: "networkidle0" });
  await click(page, "main button", "Remove host…");
  const dialog = () => page.$eval('[data-testid="remove-host"]', (e) => e.innerText).catch(() => "");
  await waitFor(page, async () => /Clean up DROOL access/.test(await dialog()), "the removal plan", 60000);
  const plan = await dialog();
  for (const want of ["Clean up DROOL access and remove the host (recommended)", "✓ DROOL's SSH account drool", "/etc/sudoers.d/drool-account-drool",
    "Remove from DROOL only", "Nothing on acct-browser changes", "can't watch the end of it", "firewall rules"]) {
    assert.ok(plan.includes(want), "removal plan lacks: " + want);
  }
  const changes = await page.$eval('[data-testid="remove-host-changes"]', (e) => e.innerText);
  assert.match(changes, /Either way, in DROOL:\s*Schedule “acct-browser updates”: acct-browser is taken out of its targets\. It has nothing left that it runs on, so it's disabled until its targets are chosen again\./);
  // The recommended choice is chosen.
  assert.equal(await page.$eval('[data-testid="remove-host"] input[type="radio"]:checked', (r) => r.closest("label").innerText.split("\n")[0]),
    "Clean up DROOL access and remove the host (recommended)");
  await shot(page, "remove-host-plan");
  await sleep(3500); // past the test controller's re-authentication window
  await click(page, '[role="dialog"] button', "Clean up and remove");
  await page.waitForSelector('[data-testid="reauth-dialog"]');
  await fill(page, "Password", adminPassword);
  await click(page, '[data-testid="reauth-dialog"] button', "Confirm");
  await waitFor(page, async () => /is removed from DROOL/.test(await dialog()), "the result", 120000);
  const done = await dialog();
  assert.match(done, /SSH account: initiated\./);
  assert.match(done, /Cleanup initiated/);
  assert.match(done, /can't verify the final state through that account/);
  await shot(page, "remove-host-done");
  await click(page, '[role="dialog"] button', "Done");
  await waitFor(page, async () => new URL(page.url()).pathname === "/hosts", "back on the host list");
  assert.ok(!(await text(page)).includes("acct-browser"), "the host is still listed");
  const after = (await api(page, "GET", `/schedules/${sch.body.id}`)).body;
  assert.equal(after.enabled, false);
  assert.deepEqual(after.config.targets.hosts, []);
  assert.equal(after.attention, "Disabled: its only target, acct-browser, was removed from DROOL. Choose its targets, then enable it.");
  await api(page, "DELETE", `/schedules/${sch.body.id}`);
  assert.deepEqual(page.errors, []);
});
