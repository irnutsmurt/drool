// Where Shepherds will connect, shown before anything changes on a host:
// the Shepherds page's trust tab and both install dialogs show the endpoint
// the controller resolves for this page's address (agent.advertise, else
// this address with agent.public_port, else agent.port). With
// DROOL_E2E_SHEPHERD_ENDPOINT it must be exactly that (the published-port
// container test: 2043:9443-style); without, it must be what the API says.
// The controller-address tab says which port an address without one gets.
// DROOL_E2E_SHEPHERD_HOST names an SSH host without a Shepherd (default:
// the first one the API lists).
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, launch, session, text, waitText, waitFor, click, shot, api } from "./lib.mjs";

let browser, page;

before(async () => {
  browser = await launch();
  page = await session(browser);
});
after(async () => browser?.close());

test("the endpoint Shepherds will connect to, before installing", async () => {
  const pki = (await api(page, "GET", "/agents/pki")).body;
  const expected = process.env.DROOL_E2E_SHEPHERD_ENDPOINT ?? pki.advertised_endpoints.join(", ");
  assert.equal(pki.advertised_endpoints.join(", "), expected, "the endpoint the API resolves");

  await page.goto(base + "/agents?tab=trust", { waitUntil: "networkidle0" });
  await waitText(page, "Agent endpoint");
  const trust = await text(page);
  assert.ok(trust.includes(expected + " — where Shepherds connect"), "trust tab: " + trust);
  assert.ok(trust.includes("DROOL listens on port " + pki.endpoint_port), "trust tab: listen port");

  // A new controller address without a port gets the port Shepherds are told.
  await page.goto(base + "/agents?tab=endpoints", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await text(page)).includes("Without a port, an address gets port " + pki.advertised_port + ","), "the port a new controller address gets");

  let hostID = process.env.DROOL_E2E_SHEPHERD_HOST;
  if (!hostID) {
    const hosts = (await api(page, "GET", "/hosts")).body.items;
    hostID = hosts.find((h) => h.management?.ssh_configured && h.management?.preferred !== "agent" && !h.jump_only && h.pinned_key)?.id;
  }
  assert.ok(hostID, "an SSH host without a Shepherd");
  await page.goto(base + "/hosts/" + hostID, { waitUntil: "networkidle0" });
  await click(page, "main button", "Install Shepherd (DROOL agent)");
  await page.waitForSelector('[data-testid="shepherd-endpoint"]');
  await waitFor(page, async () => (await text(page)).includes("Shepherds will connect to: " + expected), "the endpoint in the install dialog");
  await shot(page, "shepherd-install-endpoint");
  await click(page, '[role="dialog"] button', "Cancel");

  await click(page, "main button", "Manual install…");
  await waitFor(page, async () => (await text(page)).includes("Shepherds will connect to: " + expected), "the endpoint in the manual install dialog");
  await click(page, '[role="dialog"] button', "Cancel");
  assert.deepEqual(page.errors, []);
});
