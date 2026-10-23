// Packs are objects of their own (dogfooding 2026-10-14): Create pack asks
// for a name and a description only, lands on the new pack's page, which
// says it has no hosts yet; hosts are added there, removed there without
// deleting the pack, and a host's packs can be changed from the host's page.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, launch, session, text, waitText, waitFor, click, confirmIn, fill, shot, api } from "./lib.mjs";

let browser, page;
const name = `e2e-pack-${Date.now().toString(36)}`;
let packID;

before(async () => {
  browser = await launch();
  page = await session(browser);
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
  await waitText(page, "No hosts in this Pack yet");
  const empty = await text(page);
  assert.ok(empty.includes(name) && empty.includes("Pack page test") && empty.includes("Hosts (0)"), empty);
  assert.ok(!empty.includes("Install updates") && !empty.includes("Run on this pack"), "an empty pack offers no runs");
  // What it sets, each where it's changed; Delete only under Actions, last.
  await page.waitForSelector('[data-testid="pack-settings"]');
  assert.match(await page.$eval('[data-testid="pack-settings"]', (d) => d.innerText), /Health checks[\s\S]*None: its hosts use every host's health checks/);
  assert.ok(!(await page.$$eval("main button", (bs) => bs.map((b) => b.textContent.trim()))).some((t) => /^Delete/.test(t)), "no Delete button in sight");
  assert.equal((await api(page, "GET", `/groups/${packID}`)).body.host_ids.length, 0);
  await shot(page, "pack-empty");

  // Add hosts.
  await click(page, "main button", "Add hosts");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "the add-hosts dialog");
  await click(page, '[role="dialog"] label', "nas-01");
  await click(page, '[role="dialog"] label', "pi-sd");
  // Joining says what the hosts get.
  await waitFor(page, async () => /What they get from/.test(await dialogText()), "what they get");
  assert.match(await dialogText(), /No health checks of its own: every host's apply/);
  await shot(page, "pack-add-hosts");
  await click(page, '[role="dialog"] button', "Add 2 hosts");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) === null, "the dialog closed");
  await waitText(page, "Hosts (2)");
  assert.deepEqual((await tableHosts()).sort(), ["nas-01", "pi-sd"]);

  // Remove one: the pack stays.
  await click(page, "main button", `Remove pi-sd from ${name}`);
  await waitText(page, `Remove pi-sd from ${name}?`);
  await confirmIn(page, "Remove from the Pack");
  await waitText(page, "Hosts (1)");
  assert.deepEqual(await tableHosts(), ["nas-01"]);
  await shot(page, "pack-members");

  // Like a host's header: the common runs in sight, reviewed before they
  // start (Scenario C: update a Pack).
  // Install updates is reviewed in place; the run composer is one link away.
  await click(page, "main button", "Install updates…");
  await page.waitForSelector('[data-testid="install-updates"]');
  await waitText(page, `Install updates on Pack ${name}?`);
  await click(page, '[data-testid="install-updates"] a', "open it in the run composer");
  await waitFor(page, async () => page.url().endsWith(`/jobs/new?op=system_update&groups=${packID}`), "New run for the pack");
  await waitText(page, "Review: run on 1 host");
  assert.match(await text(page), /System update/);

  // From the host's Management section: add it back.
  await page.goto(base + `/hosts/${id("pi-sd")}/management`, { waitUntil: "networkidle0" });
  await click(page, "main button", "Change…");
  await waitFor(page, async () => (await page.$('[role="dialog"]')) !== null, "the host's packs dialog");
  await click(page, '[role="dialog"] label', name);
  await waitFor(page, async () => (await api(page, "GET", `/groups/${packID}`)).body.host_ids.includes(id("pi-sd")), "pi-sd added");
  await click(page, '[role="dialog"] button', "Done");
  await click(page, "main a", name); // the host page links to the pack's page
  await waitFor(page, async () => page.url().endsWith(`/groups/${packID}`), "the pack's page from the host");
  await waitText(page, "Hosts (2)");

  // Empty again: still a pack.
  for (const h of ["nas-01", "pi-sd"]) {
    await click(page, "main button", `Remove ${h} from ${name}`);
    await confirmIn(page, "Remove from the Pack");
  }
  await waitText(page, "No hosts in this Pack yet");
  assert.equal((await api(page, "GET", `/groups/${packID}`)).status, 200);
  await page.goto(base + "/groups", { waitUntil: "networkidle0" });
  await waitText(page, `${name} (0 hosts)`);
  // The list is for finding a Pack: no editors and no Delete on its cards.
  assert.ok(!(await page.$$eval('[data-testid="pack-card"] button', (bs) => bs.map((b) => b.textContent.trim()))).some((t) => /Delete|Customize|priority/i.test(t)), "editors on the list");
  assert.deepEqual(page.errors, []);
});

const deleteFromMenu = async () => {
  await click(page, '[data-testid="pack-actions"]', "Actions");
  await click(page, '[role="menuitem"]', "Delete pack…");
};

// Deleting a pack something still uses (final After-M17 batch): the dialog
// lists what uses it and where to change it, and offers no Delete; once that
// is changed, it says what goes with the pack, and deleting keeps the hosts.
test("deleting a pack that is still in use", async () => {
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  const nas = hosts.find((h) => h.name === "nas-01").id;
  const pack = (await api(page, "POST", "/groups", { name: `${name}-del`, description: "to delete" })).body;
  await api(page, "POST", `/groups/${pack.id}/members`, { host_ids: [nas] });
  const ov = (await api(page, "PUT", "/monitoring/health/overrides", { scope: "group", group_id: pack.id, check: "cpu", fields: { warning: 80 } })).body;
  assert.ok(ov.override?.id, JSON.stringify(ov));

  await page.goto(base + `/groups/${pack.id}`, { waitUntil: "networkidle0" });
  await deleteFromMenu();
  await page.waitForSelector('[data-testid="delete-pack"]');
  await waitText(page, "is still in use, so DROOL won't delete it");
  const blocked = await page.$eval('[data-testid="delete-pack"]', (d) => d.innerText);
  assert.ok(blocked.includes("Monitoring setting: High CPU utilization") && blocked.includes("Reset it to inherited"), blocked);
  assert.ok(!(await page.$$eval('[data-testid="delete-pack"] button', (bs) => bs.map((b) => b.textContent))).includes("Delete pack"), "no Delete while in use");
  await shot(page, "pack-delete-in-use");
  // The server refuses too, whatever the browser does.
  assert.equal((await api(page, "DELETE", `/groups/${pack.id}`)).status, 409);
  await click(page, '[data-testid="delete-pack"] button', "Close");

  // Changed: now it can go, and the dialog says what goes with it.
  await api(page, "DELETE", `/monitoring/health/overrides/${ov.override.id}`);
  await deleteFromMenu();
  await waitText(page, "Nothing else uses");
  const free = await page.$eval('[data-testid="delete-pack"]', (d) => d.innerText);
  assert.ok(free.includes("1 host leaves the Pack; the hosts stay in DROOL") && free.includes("No host is deleted"), free);
  await shot(page, "pack-delete");
  await click(page, '[data-testid="delete-pack"] button', "Delete pack");
  await waitFor(page, async () => page.url().endsWith("/groups"), "back on the packs page");
  assert.equal((await api(page, "GET", `/groups/${pack.id}`)).status, 404);
  assert.equal((await api(page, "GET", `/hosts/${nas}`)).status, 200);
  assert.deepEqual(page.errors, []);
});

// Where another Pack decides (Phase 2, ARCHITECTURE-ALPHA §47): the Pack's
// page lists, from the server's verdicts, the hosts where its setting
// doesn't apply because another of their Packs disagrees, in words, and
// settles it in one step.
test("a Pack's page: where another Pack decides, settled by Let … win", async () => {
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  const nas = hosts.find((h) => h.name === "nas-01").id;
  const a = (await api(page, "POST", "/groups", { name: `${name}-a` })).body;
  const b = (await api(page, "POST", "/groups", { name: `${name}-b` })).body;
  try {
    for (const p of [a, b]) await api(page, "POST", `/groups/${p.id}/members`, { host_ids: [nas] });
    await api(page, "PUT", "/monitoring/health/overrides", { scope: "group", group_id: a.id, check: "memory", fields: { warning: 70 } });
    await api(page, "PUT", "/monitoring/health/overrides", { scope: "group", group_id: b.id, check: "memory", fields: { warning: 75 } });
    await page.goto(base + `/groups/${a.id}`, { waitUntil: "networkidle0" });
    await page.waitForSelector('[data-testid="pack-conflicts-list"]');
    const t = await page.$eval('[data-testid="pack-conflicts-list"]', (d) => d.innerText);
    assert.match(t, /nas-01 · Memory pressure, warning at/);
    assert.match(t, new RegExp(`This Pack \\(70%\\) and ${name}-b \\(75%\\) disagree at\\s+equal priority \\(0\\): 90% from Recommended applies\\.`));
    assert.match(await page.$eval('[data-testid="pack-settings"]', (d) => d.innerText), /Set here: Memory pressure/);
    await shot(page, "pack-conflicts");
    await click(page, '[data-testid="pack-conflicts-list"] button', `Let ${name}-a win…`);
    await waitText(page, `Let ${name}-a win over ${name}-b?`);
    await confirmIn(page, `Set ${name}-a's priority to 1`);
    await page.waitForSelector('[data-testid="pack-conflicts-none"]');
    assert.equal((await api(page, "GET", `/monitoring/health/groups`)).body.groups.find((g) => g.group_id === a.id).priority, 1);
    // The other Pack's page: outranked, said so.
    await page.goto(base + `/groups/${b.id}`, { waitUntil: "networkidle0" });
    await page.waitForSelector('[data-testid="pack-conflicts-list"]');
    assert.match(await page.$eval('[data-testid="pack-conflicts-list"]', (d) => d.innerText),
      new RegExp(`This Pack sets 75%; ${name}-a \\(priority 1\\) outranks it: 70% from the Pack ${name}-a applies\\.`));
    assert.deepEqual(page.errors, []);
  } finally {
    for (const p of [a, b]) {
      for (const o of ((await api(page, "GET", `/monitoring/health/overrides?scope=group&group_id=${p.id}`)).body.items ?? [])) await api(page, "DELETE", `/monitoring/health/overrides/${o.id}`);
      await api(page, "DELETE", `/groups/${p.id}`);
    }
  }
});
