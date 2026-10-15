// Uninstalling a Shepherd from its host's page (final After-M17 batch):
// what DROOL will remove, read from the host first; the removal; what DROOL
// confirmed. Run by test/integration TestShepherdUninstallOnHost against a
// host with a real, connected Shepherd (DROOL_E2E_HOST); the Go test checks
// the host afterwards.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, click, launch, session, shot, text, waitFor, waitText } from "./lib.mjs";

let browser, page;
before(async () => {
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

test("uninstall a Shepherd: the plan, the removal, the result", async () => {
  const id = process.env.DROOL_E2E_HOST;
  assert.ok(id, "DROOL_E2E_HOST");
  await page.goto(`${base}/hosts/${id}`, { waitUntil: "networkidle0" });
  await click(page, "button", "Uninstall Shepherd…");
  const dialog = () => page.$eval('[data-testid="uninstall-shepherd"]', (e) => e.innerText).catch(() => "");
  await waitFor(page, async () => /DROOL will \(as root, over SSH\)/.test(await dialog()), "the plan", 60000);
  const plan = await dialog();
  assert.match(plan, /stop the Shepherd/);
  assert.match(plan, /\/usr\/libexec\/drool-agent/);
  assert.match(plan, /\/var\/lib\/drool-agent/);
  assert.match(plan, /In DROOL/);
  assert.match(plan, /revoke/i);
  assert.match(plan, /DROOL's own SSH account stay as they are/);
  assert.doesNotMatch(plan, /BEGIN|PRIVATE KEY/, "no secrets in the plan");
  await shot(page, "shepherd-uninstall-plan");
  await click(page, '[data-testid="uninstall-shepherd"] button', "Uninstall Shepherd");
  await waitFor(page, async () => /Removed:/.test(await dialog()), "the result", 120000);
  const done = await dialog();
  assert.match(done, /managed over SSH|over SSH/);
  await shot(page, "shepherd-uninstall-done");
  await click(page, '[data-testid="uninstall-shepherd"] button', "Done");
  // The page now says SSH, and offers to install a Shepherd again.
  await waitFor(page, async () => /Install (a )?Shepherd/.test(await text(page)), "install offered again", 30000);
  assert.deepEqual(page.errors, []);
});
