// Packs are objects of their own (dogfooding 2026-10-14): Create pack asks
// for a name and a description only, lands on the new pack's page, which
// says it has no hosts yet; hosts are added there, removed there without
// deleting the pack, and a host's packs can be changed from the host's page.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, launch, session, text, waitText, waitFor, click, fill, shot, api } from "./lib.mjs";

let browser, page;
const name = `e2e-pack-${Date.now().toString(36)}`;
let packID;

before(async () => {
  browser = await launch();
  page = await session(browser);
  page.on("dialog", (d) => d.accept()); // the remove confirmation
});
after(async () => {
  if (packID) await api(page, "DELETE", `/groups/${packID}`);
  await browser?.close();
});

const dialogText = () => page.$eval('[role="dialog"]', (d) => d.innerText);
const tableHosts = () => page.$$eval("main tbody tr td:first-child", (tds) => tds.map((td) => td.innerText.trim()));

test("create an empty pack, then manage its hosts", async () => {
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  const id = (n) => hosts.find((h) => h.name === n)?.id;
  assert.ok(id("nas-01") && id("pi-sd"), "fixture hosts nas-01 and pi-sd");

  // Create: a name and a description, no hosts.
  await page.goto(base + "/groups", { waitUntil: "networkidle0" });
  await click(page, "main button", "Create pack");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "the create dialog");
  assert.ok(!(await dialogText()).includes("nas-01"), "creating a pack doesn't ask for hosts");
  await fill(page, "Name", name);
  await fill(page, "Description (optional)", "Pack page test");
  await shot(page, "pack-create");
  await click(page, '[role="dialog"] button', "Create");

  // The new pack's page, empty.
  await waitFor(page, async () => /\/groups\/grp_/.test(page.url()), "the pack's page");
  packID = page.url().split("/groups/")[1];
  await waitText(page, "No hosts assigned yet");
  const empty = await text(page);
  assert.ok(empty.includes(name) && empty.includes("Pack page test") && empty.includes("Hosts (0)"), empty);
  assert.equal((await api(page, "GET", `/groups/${packID}`)).body.host_ids.length, 0);
  await shot(page, "pack-empty");

  // Add hosts.
  await click(page, "main button", "Add hosts");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "the add-hosts dialog");
  await click(page, '[role="dialog"] label', "nas-01");
  await click(page, '[role="dialog"] label', "pi-sd");
  await shot(page, "pack-add-hosts");
  await click(page, '[role="dialog"] button', "Add 2 hosts");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) === null, "the dialog closed");
  await waitText(page, "Hosts (2)");
  assert.deepEqual((await tableHosts()).sort(), ["nas-01", "pi-sd"]);

  // Remove one: the pack stays.
  await click(page, "main button", `Remove pi-sd from ${name}`);
  await waitText(page, "Hosts (1)");
  assert.deepEqual(await tableHosts(), ["nas-01"]);
  await shot(page, "pack-members");

  // From the host's page: add it back.
  await page.goto(base + `/hosts/${id("pi-sd")}`, { waitUntil: "networkidle0" });
  await click(page, "main button", "Change…");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "the host's packs dialog");
  await click(page, '[role="dialog"] label', name);
  await waitFor(page, async () => (await api(page, "GET", `/groups/${packID}`)).body.host_ids.includes(id("pi-sd")), "pi-sd added");
  await click(page, '[role="dialog"] button', "Done");
  await click(page, "main a", name); // the host page links to the pack's page
  await waitFor(page, async () => page.url().endsWith(`/groups/${packID}`), "the pack's page from the host");
  await waitText(page, "Hosts (2)");

  // Empty again: still a pack.
  for (const h of ["nas-01", "pi-sd"]) await click(page, "main button", `Remove ${h} from ${name}`);
  await waitText(page, "No hosts assigned yet");
  assert.equal((await api(page, "GET", `/groups/${packID}`)).status, 200);
  await page.goto(base + "/groups", { waitUntil: "networkidle0" });
  await waitText(page, `${name} (0 hosts)`);
  assert.deepEqual(page.errors, []);
});
