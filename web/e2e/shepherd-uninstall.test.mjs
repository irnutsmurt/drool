// Uninstalling a Shepherd from its host's page (final After-M17 batch; the
// Shepherd removes itself since the dogfooding correction batch): how it
// goes and what it removes; the removal; what DROOL confirmed over SSH. Run
// by test/integration TestShepherdUninstallOnHost against a host with a
// real, connected Shepherd (DROOL_E2E_HOST); the Go test checks the host
// afterwards.
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
  await waitFor(page, async () => /The Shepherd removes itself/.test(await dialog()), "the plan", 60000);
  const plan = await dialog();
  assert.match(plan, /starts a helper that outlives it/);
  assert.match(plan, /Afterwards DROOL checks over SSH that nothing of it is left/);
  assert.match(plan, /\/usr\/libexec\/drool-agent/);
  assert.match(plan, /\/var\/lib\/drool-agent/);
  assert.match(plan, /In DROOL/);
  assert.match(plan, /revoke/i);
  assert.match(plan, /DROOL's own SSH account stay as they are/);
  assert.doesNotMatch(plan, /BEGIN|PRIVATE KEY/, "no secrets in the plan");
  await shot(page, "shepherd-uninstall-plan");
  await click(page, '[data-testid="uninstall-shepherd"] button', "Uninstall Shepherd");
  await waitFor(page, async () => /Verified removed|Self-removal initiated|Partly removed|unknown/i.test(await dialog()), "the result", 180000);
  const done = await dialog();
  assert.match(done, /Verified removed/);
  assert.match(done, /checked over SSH/);
  assert.match(done, /manages .* over SSH/);
  await shot(page, "shepherd-uninstall-done");
  await click(page, '[data-testid="uninstall-shepherd"] button', "Done");
  // The page now says SSH, and offers to install a Shepherd again.
  await waitFor(page, async () => /Install (a )?Shepherd/.test(await text(page)), "install offered again", 30000);
  assert.deepEqual(page.errors, []);
});
