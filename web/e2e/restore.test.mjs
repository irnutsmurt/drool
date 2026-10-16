// M16 restore reconciliation in the browser, against a controller that was
// really restored from a backup (test/integration TestRestoreReconciliation
// runs this file when its Shepherds have been compared; it is not part of
// TestBrowser's list). The host named by DROOL_E2E_AHEAD changed after the
// backup; DROOL_E2E_SAME didn't.
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import { base, click, confirmIn, launch, session, shot, sleep, text, waitFor, waitText, fill } from "./lib.mjs";

const ahead = process.env.DROOL_E2E_AHEAD;
const same = process.env.DROOL_E2E_SAME;
const password = process.env.DROOL_E2E_PASSWORD;
let browser;
let page;

before(async () => {
  if (!ahead) return;
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

test("Settings → System shows the restore and what each host needs", { skip: !ahead }, async () => {
  await page.goto(base + "/settings/system", { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="restore-reconciliation"]');
  const t = await text(page);
  for (const s of ["Reconciling", "Reconciled: 1", "Ahead of DROOL: 1", "Hosts not reconciled yet",
    "Shepherd configuration: host has newer (revision 2, DROOL 1)", "Accept host's state", "Finish reconciliation"]) {
    assert.ok(t.includes(s), `missing ${JSON.stringify(s)}`);
  }
  // The reconciled host is only listed with all hosts.
  assert.ok(!t.includes("rst-same"), "a reconciled host listed among those left");
  await click(page, "button", "Show all 2 hosts");
  await waitText(page, "rst-same");
  await shot(page, "restore-system");
  // The header's security indicator says so too.
  await page.click('[data-testid="security-status"]');
  await waitText(page, "This controller was restored from a backup");
  await page.keyboard.press("Escape");
});

test("the host page says what waits for reconciliation", { skip: !ahead }, async () => {
  await page.goto(`${base}/hosts/${ahead}`, { waitUntil: "networkidle0" });
  await waitText(page, "Restore reconciliation — Ahead of DROOL.");
  await waitText(page, "Until then DROOL doesn't change its firewall, and its Shepherd, if it has one, refuses this controller's commands.");
  await shot(page, "restore-host");
  await page.goto(`${base}/hosts/${same}`, { waitUntil: "networkidle0" });
  await sleep(1000);
  assert.ok(!(await text(page)).includes("Restore reconciliation"), "notice on a reconciled host");
});

test("accepting the host's state, after confirming who you are", { skip: !ahead }, async () => {
  await page.goto(base + "/settings/system", { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="restore-reconciliation"]');
  await sleep(3500); // past the test controller's re-authentication window
  await click(page, "button", "Accept host's state");
  await confirmIn(page, "Accept host's state");
  await page.waitForSelector('[data-testid="reauth-dialog"]');
  await fill(page, "Password", password);
  await click(page, '[data-testid="reauth-dialog"] button', "Confirm");
  await waitText(page, "Every host is reconciled", 60000);
  await waitFor(page, async () => (await text(page)).includes("Finished"), "the reconciliation finishing", 30000);
  await shot(page, "restore-finished");
  assert.deepEqual(page.errors, []);
});
