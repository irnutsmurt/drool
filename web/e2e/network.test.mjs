// Network monitoring (M10): fleet Network activity, a host's Network
// connections, connection details, the Configured / Observed topology and
// "Create policy from observed connection". Needs test/integration
// TestBrowser's environment: hosts jump-a, server-a..c, web-1; a GeoIP/ASN
// test database; observations ingested from DROOL_TEST_NETFLOWS_FILE
// (hundreds of public addresses in many networks, recent and old).
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

const baseline = `groups:
  linux-servers:
    members: [server-a, server-b, server-c]
policy:
  - name: web-out
    from: web-1
    to: internet
    allow: [https]
  - name: jump-to-servers
    from: jump-a
    to: linux-servers
    allow: [ssh]
`;

let browser;
let page;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });
beforeEach(async () => {
  page?.close().catch(() => {});
  page = await session(browser);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  const cur = await api(page, "GET", "/firewall/document");
  const r = await api(page, "PUT", "/firewall/document", { yaml: baseline, revision: cur.body.revision });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  await api(page, "PUT", "/firewall/layout", {});
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  page.ids = Object.fromEntries(hosts.map((h) => [h.name, h.id]));
  // The observations file is ingested shortly after the hosts exist.
  await waitFor(page, async () => ((await api(page, "GET", "/network/flows?limit=1")).body?.items ?? []).length > 0, "observations", 60000);
});

const rows = () => page.$$eval('[data-testid="flow-table"] tbody tr[data-flow-id]', (trs) => trs.map((t) => t.innerText));
const shown = () => page.$eval('[data-testid="flow-table"] [aria-live]', (e) => e.innerText).catch(() => "");

async function select(label, value) {
  const ok = await page.evaluate((l, v) => {
    for (const lab of document.querySelectorAll("label")) {
      const span = lab.querySelector("span");
      const sel = lab.querySelector("select");
      if (span?.textContent.trim() === l && sel) {
        const opt = [...sel.options].find((o) => o.value === v || o.textContent.startsWith(v));
        if (!opt) return false;
        sel.value = opt.value;
        sel.dispatchEvent(new Event("change", { bubbles: true }));
        return true;
      }
    }
    return false;
  }, label, value);
  assert.ok(ok, `select ${label} = ${value}`);
  await sleep(300);
  await page.waitForNetworkIdle({ idleTime: 300 }).catch(() => {});
}

test("fleet Network activity: summary, server-side filters, paging and details from the keyboard", async () => {
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length > 0, "rows");
  const t = await text(page);
  assert.match(t, /How hosts are observed/);
  assert.match(t, /Continuous: conntrack/);
  assert.match(t, /Snapshot only: SSH/);
  await shot(page, "network-fleet");

  // Paging: 200+ flows, 50 per page, the server keeps the cursor.
  assert.match(await shown(), /Showing 1–50 of \d+/);
  const first = await rows();
  await click(page, "button", "Next");
  await waitFor(page, async () => /Showing 51–100/.test(await shown()), "page 2");
  assert.notDeepEqual(await rows(), first);
  await click(page, "button", "Previous");
  await waitFor(page, async () => /Showing 1–50/.test(await shown()), "page 1 again");

  // Quick filter: what no policy describes.
  await click(page, 'button[aria-pressed]', "Not in policy");
  await waitFor(page, async () => (await rows()).every((r) => r.includes("Not in policy")) && (await rows()).length > 0, "rows not in policy");
  assert.match(page.url(), /policy=unclassified/);
  await click(page, "button", "Clear");

  // External connections to one country, then one network (under More filters).
  await click(page, "button", "More filters");
  await select("Country", "US");
  await waitFor(page, async () => { const r = await rows(); return r.length > 0 && r.every((x) => x.includes("(US)")); }, "US rows");
  await select("Network (ASN)", "15169");
  await waitFor(page, async () => { const r = await rows(); return r.length > 0 && r.every((x) => x.includes("AS15169")); }, "AS15169 rows");
  await shot(page, "network-filtered");
  await click(page, "button", "Clear");

  // Anything using TCP 22.
  const port = await page.evaluateHandle(() => [...document.querySelectorAll("label")].find((l) => l.querySelector("span")?.textContent === "Port")?.querySelector("input"));
  await port.asElement().type("22");
  await waitFor(page, async () => { const r = await rows(); return r.length > 0 && r.every((x) => x.includes("TCP 22")); }, "port 22 rows");
  await click(page, "button", "Clear");
  await waitFor(page, async () => /Showing 1–50 of/.test(await shown()), "cleared");

  // Details with the keyboard only: Tab to the first row's button, Enter.
  await page.focus("body");
  await tabTo(page, () => !!document.activeElement?.closest?.('[data-testid="flow-table"] tbody') && document.activeElement.tagName === "BUTTON", "first connection");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => !!(await page.$('[data-testid="flow-details"]')), "details");
  const d = await page.$eval('[data-testid="flow-details"]', (e) => e.innerText);
  assert.match(d, /Source/);
  assert.match(d, /Destination/);
  assert.match(d, /Firewall/);
  assert.match(d, /Observed by/);
  await shot(page, "network-details");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[data-testid="flow-details"]')), "details closed");
  assert.deepEqual(page.errors, []);
});

test("new external destinations and an unclassified connection's details", async () => {
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await click(page, 'button[aria-pressed]', "New external destinations");
  await waitFor(page, async () => (await rows()).some((r) => r.includes("185.10.20.30")), "new destination");
  const r = await rows();
  assert.ok(r.every((x) => /Today|Yesterday/.test(x)), "only recently first seen destinations");
  // A new destination is context, not an alarm: no threat words anywhere.
  assert.doesNotMatch(await text(page), /malicious|suspicious|threat|risk score/i);
  // Its details: no process could be tied to it, said plainly; the policy
  // doesn't cover it; country and network are context.
  await page.evaluate(() => [...document.querySelectorAll('[data-testid="flow-table"] tbody tr')].find((t) => t.innerText.includes("185.10.20.30"))?.querySelector("button")?.click());
  await waitFor(page, async () => !!(await page.$('[data-testid="flow-details"]')), "details");
  const d = await page.$eval('[data-testid="flow-details"]', (e) => e.innerText);
  assert.match(d, /Unknown — DROOL couldn't tie this connection to a process/);
  assert.match(d, /Not in policy/);
  assert.match(d, /What it is[\s\S]*Unknown/);
  assert.match(d, /context, not a verdict/);
  assert.match(d, /Create policy from observed connection/);
  await shot(page, "network-new-destination");
  assert.deepEqual(page.errors, []);
});

test("create policy from an observed connection opens a designer draft — nothing is saved", async () => {
  const before = (await api(page, "GET", "/firewall/document")).body.revision;
  const flows = (await api(page, "GET", "/network/flows?policy=unclassified&scope=external&limit=50")).body.items;
  const f = flows.find((x) => x.port === 8812) ?? flows[0];
  // Open its details from the table (filtered to its address).
  await page.goto(base + "/network?peer=" + encodeURIComponent(f.remote_ip), { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length > 0, "row");
  await page.evaluate(() => document.querySelector('[data-testid="flow-table"] tbody tr button')?.click());
  await waitFor(page, async () => !!(await page.$('[data-testid="flow-details"]')), "details");
  await click(page, "button", "Create policy from observed connection");
  await waitFor(page, async () => (await text(page)).includes("was added from an observed connection"), "draft notice", 20000);
  assert.match(page.url(), /view=rules/);
  await waitFor(page, async () => (await page.$eval('[data-testid="designer-status"]', (e) => e.innerText)).includes("Unsaved changes"), "unsaved");
  const t = await text(page);
  assert.match(t, new RegExp(f.remote_ip.replace(/\./g, "\\.")));
  await shot(page, "network-suggest-draft");
  // Still only a draft: the stored policy did not change.
  assert.equal((await api(page, "GET", "/firewall/document")).body.revision, before);
  await click(page, "button", "Discard changes");
  assert.deepEqual(page.errors, []);
});

test("a host's network activity: summary on the host, its own page with listening, snapshot only for SSH, filters", async () => {
  const id = page.ids["web-1"];
  // The host overview summarizes and links to the host's Network Activity page.
  await page.goto(`${base}/hosts/${id}`, { waitUntil: "networkidle0" });
  await page.waitForSelector('[data-testid="tile-network"]');
  assert.equal(await page.$eval('[data-testid="tile-network"] a', (a) => a.getAttribute("href")), `/hosts/${id}/network`);
  assert.equal(await page.$('[data-testid="flow-table"]'), null, "no connection table on the host overview");
  await click(page, '[data-testid="tile-network"] a', "Open");
  await waitFor(page, async () => page.url().endsWith(`/hosts/${id}/network`), "host network page");
  await waitText(page, "How this host is observed");
  await waitFor(page, async () => (await text(page)).includes("Snapshot only: SSH"), "snapshot label");
  assert.match(await text(page), /never continuously monitored/);
  // A real snapshot over SSH: the controller's own connection shows up as
  // DROOL management, recognized from where DROOL's SSH comes from.
  await click(page, "button", "Take snapshot");
  await waitText(page, "Snapshot taken", 60000);
  await click(page, 'button[aria-pressed]', "Inbound");
  await waitFor(page, async () => (await rows()).some((r) => r.includes("DROOL management")), "management connection", 20000);
  assert.match(await text(page), /Listening \(\d+\)/);
  await click(page, 'button[aria-pressed]', "External");
  await waitFor(page, async () => { const r = await rows(); return r.length > 0 && r.every((x) => /External/.test(x)); }, "external rows");
  await shot(page, "network-host");
  assert.deepEqual(page.errors, []);
});

// The dialog's control labelled `label` (labels repeat in the filter bar).
async function formField(label) {
  const h = await page.evaluateHandle((l) => {
    for (const lab of document.querySelectorAll('[data-testid="classify-form"] label')) {
      if (lab.querySelector("span")?.textContent.trim() === l) return lab.querySelector("input,select");
    }
    return document.querySelector(`[data-testid="classify-form"] [aria-label="${l}"]`);
  }, label);
  assert.ok(h.asElement(), `form field ${label}`);
  return h.asElement();
}
async function formSelect(label, value) {
  const el = await formField(label);
  await el.evaluate((s, v) => {
    s.value = v;
    s.dispatchEvent(new Event("change", { bubbles: true }));
  }, value);
}

test("classifying traffic: DROOL's own by evidence, a label for one connection, widened, filtered and deleted", async () => {
  for (const r of (await api(page, "GET", "/network/classifications")).body.items) await api(page, "DELETE", `/network/classifications/${r.id}`);
  // Someone's SSH from the LAN uses port 22 too: a known service, never
  // DROOL's own traffic.
  await page.goto(base + "/network?peer=192.168.1.25", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length > 0, "LAN ssh row");
  assert.ok((await rows()).every((r) => r.includes("Known service") && !r.includes("DROOL management")), (await rows()).join("\n"));

  // An unknown destination: label it from its details (a dialog over the
  // details dialog).
  await page.goto(base + "/network?peer=185.10.20.30", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length === 1, "row");
  assert.match((await rows())[0], /Unknown/);
  await page.evaluate(() => document.querySelector('[data-testid="flow-table"] tbody tr button')?.click());
  await waitFor(page, async () => !!(await page.$('[data-testid="flow-classification"]')), "what it is");
  assert.match(await page.$eval('[data-testid="flow-classification"]', (e) => e.innerText), /Nothing says what this is yet/);
  // Classify is in the inspector's footer, in sight; the form replaces the details (no dialog on top).
  await click(page, '[data-testid="flow-inspector"] button', "Classify…");
  await waitFor(page, async () => !!(await page.$('[data-testid="classify-form"]')), "classify form");
  assert.match(await page.$eval('[data-testid="classify-scope"]', (e) => e.innerText), /this connection only/);
  assert.match(await page.$eval('[data-testid="classify-scope"]', (e) => e.innerText), /no firewall rule is created/);
  await (await formField("Label")).type("Backup provider");
  await page.click('[data-testid="classify-form"] input[type="checkbox"]');
  await shot(page, "network-classify");
  await click(page, '[data-testid="classify-form"] button', "Save classification");
  await waitFor(page, async () => !(await page.$('[data-testid="classify-form"]')), "form closed");
  await waitFor(page, async () => /Expected[\s\S]*Backup provider[\s\S]*labeled from one connection/.test(await page.$eval('[data-testid="flow-classification"]', (e) => e.innerText)), "labeled");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[data-testid="flow-details"]')), "details closed");
  await waitFor(page, async () => (await rows())[0]?.includes("Backup provider"), "row relabeled");

  // Widen it on the Classifications tab: every host's outbound 8812.
  await page.goto(base + "/network?tab=classifications", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await page.$eval('[data-testid="classifications"]', (e) => e.innerText)).includes("one connection"), "listed");
  await page.click('button[aria-label="Edit Backup provider"]');
  await waitFor(page, async () => !!(await page.$('[data-testid="classify-form"]')), "edit form");
  await formSelect("Hosts", "");
  await formSelect("Remote side", "any");
  await waitFor(page, async () => /every connection that matches/.test(await page.$eval('[data-testid="classify-scope"]', (e) => e.innerText)), "wider scope");
  await click(page, '[data-testid="classify-form"] button', "Save classification");
  await waitFor(page, async () => (await page.$eval('[data-testid="classifications"]', (e) => e.innerText)).includes("any host → anywhere, TCP 8812"), "widened");
  await shot(page, "network-classifications");

  // It applies to traffic already observed: the other 8812 destinations.
  await page.goto(base + "/network?class=approved", { waitUntil: "networkidle0" });
  await waitFor(page, async () => { const r = await rows(); return r.length > 1 && r.every((x) => x.includes("Backup provider")) && r.some((x) => x.includes("95.216.1.")); }, "approved rows");
  // DROOL's own traffic is its own filter.
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await click(page, "button", "More filters");
  await click(page, 'button[aria-pressed]', "DROOL's own traffic");
  await waitFor(page, async () => /class=drool/.test(page.url()), "drool filter");

  // Deleted: back to unknown.
  await page.goto(base + "/network?tab=classifications", { waitUntil: "networkidle0" });
  await page.click('button[aria-label="Delete Backup provider"]');
  await click(page, "button", "Delete classification");
  await waitText(page, "No classifications yet");
  await page.goto(base + "/network?peer=185.10.20.30", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows())[0]?.includes("Unknown"), "unknown again");
  assert.deepEqual(page.errors, []);
});

test("topology: Configured, Observed and both, grouped by network owner, with a list equivalent", async () => {
  await page.goto(`${base}/firewall?tab=designer&view=topology`, { waitUntil: "networkidle0" });
  await waitFor(page, async () => !!(await page.$('[data-testid="topology-mode"]')), "mode switch");
  // "kind: label | full wording (the tooltip and accessible name)".
  const labels = () => page.$$eval("[data-edge-label]", (xs) => xs.map((x) => x.getAttribute("data-edge-label") + ": " + x.textContent + " | " + x.getAttribute("title")));
  assert.ok((await labels()).every((l) => !l.startsWith("observed")), "configured shows no observations");

  await click(page, '[data-testid="topology-mode"] button', "Observed");
  await waitFor(page, async () => (await labels()).some((l) => l.startsWith("observed")), "observed edges", 20000);
  assert.ok((await labels()).every((l) => !l.startsWith("rule")), "observed only: no rules");
  // External networks are grouped (at most 25 owners plus "Other"), not one
  // node per address.
  const nodes = await page.$$eval(".react-flow__node", (xs) => xs.map((x) => x.innerText));
  assert.ok(nodes.length <= 40, `readable: ${nodes.length} nodes`);
  assert.ok(nodes.some((n) => n.includes("Google LLC")), "an ASN node");
  assert.ok(nodes.some((n) => n.includes("Other external networks")), "small networks folded");
  assert.ok((await labels()).some((l) => /^observed: ⚠ .*\| ⚠ Observed · unclassified/.test(l)), "unclassified edges are distinct and labelled");
  await shot(page, "topology-observed");

  // Expand one network to its addresses.
  await page.evaluate(() => {
    const n = [...document.querySelectorAll(".react-flow__node")].find((x) => x.innerText.includes("Google LLC"));
    [...n.querySelectorAll("button")].find((b) => b.textContent === "Show addresses")?.click();
  });
  await waitFor(page, async () => (await page.$$eval(".react-flow__node", (xs) => xs.map((x) => x.innerText))).some((n) => /^\s*•?\s*142\.250\./m.test(n) || n.includes("142.250.")), "addresses", 20000);
  await shot(page, "topology-observed-expanded");

  await click(page, '[data-testid="topology-mode"] button', "Configured + Observed");
  await waitFor(page, async () => { const l = await labels(); return l.some((x) => x.startsWith("rule")) && l.some((x) => x.startsWith("observed")); }, "both");
  await shot(page, "topology-both");

  // The list equivalent: open it, pick an observed line with the keyboard.
  await page.evaluate(() => { const d = document.querySelector('[data-testid="observed-list"]'); if (d && !d.open) d.querySelector("summary").click(); });
  await waitFor(page, () => page.$eval('[data-testid="observed-list"]', (d) => d.open), "list open");
  await page.focus('[data-testid="observed-list"] summary');
  await tabTo(page, () => document.activeElement?.textContent === "Details" && !!document.activeElement.closest('[data-testid="observed-list"]'), "list Details");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => !!(await page.$('[data-testid="observed-details"]')), "observed details");
  const d = await page.$eval('[data-testid="observed-details"]', (e) => e.innerText);
  assert.match(d, /Connections/);
  assert.match(d, /Show all in Network Activity/);
  assert.deepEqual(page.errors, []);
});

test("country and network owner: shown from the databases, genuinely unknown said so, no 'not configured' note", async () => {
  await page.goto(base + "/network", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length > 0, "rows");
  assert.equal(await page.$('[data-testid="enrichment-note"]'), null, "a note although both databases are loaded");
  // An address neither database knows: unknown, said so.
  await page.goto(base + "/network?peer=49.12.200.77", { waitUntil: "networkidle0" });
  await waitFor(page, async () => (await rows()).length === 1, "the unknown address");
  const r = (await rows())[0];
  assert.match(r, /country unknown/);
  assert.match(r, /network unknown/);
  await click(page, '[data-testid="flow-table"] tbody button', "Details");
  await waitFor(page, async () => !!(await page.$('[data-testid="flow-details"]')), "details");
  assert.match(await page.$eval('[data-testid="flow-details"]', (e) => e.innerText), /Country\s+Unknown \(the GeoIP country database has no answer for this address\)/);
  await page.keyboard.press("Escape");
  assert.deepEqual(page.errors, []);
});

test("empty results, settings and enrichment status don't break", async () => {
  await page.goto(base + "/network?peer=192.0.2.123", { waitUntil: "networkidle0" });
  await waitText(page, "No connections match");
  await page.goto(base + "/network?tab=settings", { waitUntil: "networkidle0" });
  await waitFor(page, async () => !!(await page.$('[data-testid="enrichment-status"]')), "enrichment status");
  const t = await text(page);
  assert.match(t, /never sends\s+addresses to an online service/);
  assert.match(t, /Keep observations for/);
  await shot(page, "network-settings");
  assert.deepEqual(page.errors, []);
});
