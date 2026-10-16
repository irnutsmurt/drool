// A Shepherd that has enrolled but not connected yet has no capabilities on
// record: the moment between `drool-agent install` enrolling and its service
// saying hello, which the host page's polling can catch. The host page and
// the Shepherds list say so instead of failing ("Cannot read properties of
// undefined (reading 'enabled')", dogfooding 2026-10-14).
// DROOL_E2E_PENDING_HOST names such a host.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, launch, session, text, waitText, waitFor, click } from "./lib.mjs";

let browser, page;

before(async () => {
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

test("a Shepherd that hasn't reported what it may do yet", async () => {
  const hostID = process.env.DROOL_E2E_PENDING_HOST;
  assert.ok(hostID, "DROOL_E2E_PENDING_HOST");

  await page.goto(base + "/hosts/" + hostID + "/management", { waitUntil: "networkidle0" });
  await waitFor(page, async () => /Shepherd details|Unexpected Application Error/.test(await text(page)), "the host's Management section");
  assert.ok(!(await text(page)).includes("Unexpected Application Error"), "the host page failed: " + page.errors.join("; "));
  await click(page, "summary", "Shepherd details");
  await waitText(page, "Not reported yet");
  assert.ok(!(await text(page)).includes("Unexpected Application Error"), "the host page failed: " + page.errors.join("; "));

  await page.goto(base + "/agents", { waitUntil: "networkidle0" });
  await waitText(page, "not reported yet");
  assert.ok(!(await text(page)).includes("Unexpected Application Error"), "Shepherds page");
  assert.deepEqual(page.errors, []);
});
