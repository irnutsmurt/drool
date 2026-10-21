// Firewall Designer: Topology, Rules and YAML edit one canonical policy.
// Needs hosts jump-a, server-a..c (reached through jump-a) and web-1 whose
// management paths were measured (test/integration TestBrowser sets this
// up). Each test starts from the same saved baseline.
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

const baseline = `networks:
  admin-lan:
    cidr: 192.168.1.0/24
groups:
  linux-servers:
    members: [server-a, server-b, server-c]
policy:
  - name: admin-to-jump
    from: admin-lan
    to: jump-a
    allow: [ssh]
  - name: jump-to-servers
    from: jump-a
    to: linux-servers
    allow: [ssh]
  - name: web-from-internet
    from: internet
    to: web-1
    allow: [http, https]
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
  await api(page, "PUT", "/firewall/hosts/" + page.ids["server-b"], { mode: "enforce" });
});

async function openDesigner(view = "topology") {
  await page.goto(`${base}/firewall?tab=designer&view=${view}`, { waitUntil: "networkidle0" });
  await waitFor(page, async () => !!(await page.$('[data-testid="designer-status"]')), "designer");
  await waitStatus("Saved");
}

const status = () => page.$eval('[data-testid="designer-status"]', (e) => e.innerText).catch(() => "");
async function waitStatus(s) {
  await waitFor(page, async () => (await status()).includes(s), "status " + s);
}
const yamlValue = () => page.$eval('textarea[aria-label="Firewall policy as YAML"]', (t) => t.value);
// Topology, Rules and YAML: a view switcher over one policy document.
const switcher = '[role="group"][aria-label="View the policy as"] button';
async function view(v) {
  await click(page, switcher, v);
  await waitFor(page, () => page.$$eval(`${switcher}[aria-pressed="true"]`, (xs, v) => xs.some((x) => x.textContent === v), v), "view " + v);
  await sleep(300);
}
const edgeLabels = () => page.$$eval("[data-edge-label]", (xs) => xs.map((x) => x.innerText.replace(/\n/g, " ")));
const nodeIds = () => page.$$eval(".react-flow__node", (xs) => xs.map((x) => x.getAttribute("data-id")));

async function placeOnCanvas(ref) {
  await page.select('select[aria-label="Place an object on the canvas"]', ref);
  await waitFor(page, async () => (await nodeIds()).includes(ref), "node " + ref);
}

async function drag(fromRef, toRef) {
  const src = await page.$(`.react-flow__node[data-id="${fromRef}"] .react-flow__handle.source`);
  const dst = await page.$(`.react-flow__node[data-id="${toRef}"] .react-flow__handle.target`);
  const a = await src.boundingBox();
  const b = await dst.boundingBox();
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.mouse.down();
  for (let i = 1; i <= 15; i++) {
    await page.mouse.move(a.x + ((b.x - a.x) * i) / 15, a.y + ((b.y - a.y) * i) / 15);
    await sleep(20);
  }
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
  await sleep(50);
  await page.mouse.up();
}

/** Chooses in one of the rule builder's pickers by typing and pressing
 * Enter on the highlighted match. */
async function pick(testId, query, label) {
  const sel = `[data-testid="${testId}"]`;
  await page.click(`${sel} input[role="combobox"]`);
  await page.type(`${sel} input[role="combobox"]`, query);
  await waitFor(page, () => page.$eval(`${sel} [role="option"][aria-selected="true"]`, (o, l) => o.innerText.includes(l), label).catch(() => false), `option ${label}`);
  await page.keyboard.press("Enter");
  await waitFor(page, () => page.$eval(`${sel} ul`, (u, l) => u.innerText.includes(l), label).catch(() => false), `chosen ${label}`);
}

/** Replaces the YAML text the way a paste would. */
async function setYAML(s) {
  const ta = await page.$('textarea[aria-label="Firewall policy as YAML"]');
  await ta.focus();
  await page.keyboard.down("Control");
  await page.keyboard.press("a");
  await page.keyboard.up("Control");
  await page.keyboard.press("Backspace");
  await page.keyboard.sendCharacter(s);
}

test("Topology → canonical model → Rules and YAML", async () => {
  await openDesigner("topology");
  await shot(page, "designer-topology");
  await placeOnCanvas("host:server-a");
  await placeOnCanvas("host:server-b");
  await sleep(800);
  await drag("host:server-a", "host:server-b");
  await waitFor(page, async () => !!(await page.$('[role="dialog"]')), "edge editor");
  // The edge editor: source and destination come from the drawn connection.
  const dlg = await page.$eval('[role="dialog"]', (d) => d.innerText);
  assert.match(dlg, /Source[\s\S]*server-a/);
  assert.match(dlg, /Destination[\s\S]*server-b/);
  // A port typed into the service search becomes a custom service.
  await pick("rule-service", "8812", "TCP 8812");
  await waitFor(page, async () => /Allows tcp-8812 \(TCP 8812\) from server-a to server-b\./.test(await page.$eval('[data-testid="rule-summary"]', (e) => e.innerText)), "the rule in words");
  await shot(page, "edge-editor");
  await click(page, "button", "Add to draft");
  await waitStatus("Unsaved changes");
  await waitFor(page, async () => (await edgeLabels()).some((l) => l.includes("tcp-8812 (TCP 8812)")), "edge label (with the new custom service's port)");
  await shot(page, "topology-new-edge");

  await view("YAML");
  const y = await yamlValue();
  assert.match(y, /tcp-8812:\n\s+protocol: tcp\n\s+port: 8812/);
  assert.match(y, /- name: allow-tcp-8812-server-a-to-server-b\n\s+from: server-a\n\s+to: server-b\n\s+allow:\n\s+- tcp-8812/);
  await view("Rules");
  const rules = await text(page);
  assert.match(rules, /allow-tcp-8812-server-a-to-server-b[\s\S]*Allow[\s\S]*server-a[\s\S]*server-b[\s\S]*tcp-8812 \(TCP 8812\)/);

  await click(page, "button", "Save policy");
  await waitStatus("Saved");
  const doc = (await api(page, "GET", "/firewall/document")).body.document;
  assert.deepEqual(doc.policy.at(-1), { name: "allow-tcp-8812-server-a-to-server-b", description: "", enabled: true, action: "allow", from: ["host:server-a"], to: ["host:server-b"], services: ["tcp-8812"] });
  assert.deepEqual(page.errors, []);
});

test("YAML → canonical model → Topology; invalid YAML is explained", async () => {
  await openDesigner("yaml");
  const y = await yamlValue();
  await setYAML(y + "  - name: web-to-db\n    from: web-1\n    to: server-c\n    allow: [postgres]\n");
  await waitStatus("Unsaved changes");
  await view("Topology");
  await waitFor(page, async () => (await edgeLabels()).some((l) => l.includes("postgres (TCP 5432)")), "postgres edge");
  assert.ok((await nodeIds()).includes("host:server-c"));
  await view("Rules");
  assert.match(await text(page), /web-to-db[\s\S]*postgres \(TCP 5432\)/);

  await view("YAML");
  await setYAML((await yamlValue()) + "  - name: broken\n    from: [web-1\n");
  await waitStatus("YAML has errors");
  await waitText(page, "Line ");
  await shot(page, "yaml-error");
  await setYAML((await yamlValue()).replace("  - name: broken\n    from: [web-1\n", "  - name: typo\n    from: nowhere\n    to: web-1\n    allow: [ssh]\n"));
  await waitStatus("problem");
  assert.match(await text(page), /unknown name "nowhere"/);
  assert.equal(await click(page, "button", "Save policy").catch(() => "disabled"), "disabled", "an invalid draft can't be saved");
  await click(page, "button", "Discard changes");
  await waitStatus("Saved");
  assert.ok(!(await yamlValue()).includes("web-to-db"));
});

test("Rules (keyboard only) → canonical model → Topology", async () => {
  await openDesigner("rules");
  await tabTo(page, () => document.activeElement?.textContent === "New rule", "New rule button");
  await page.keyboard.press("Enter");
  await waitText(page, "Destination — the host or hosts that receive it");
  const inPicker = (testId) => page.evaluate((t) => document.activeElement?.getAttribute("role") === "combobox" && !!document.activeElement.closest(`[data-testid="${t}"]`), testId);
  const active = (testId) => page.$eval(`[data-testid="${testId}"] [role="option"][aria-selected="true"]`, (o) => o.innerText).catch(() => "");
  // The dialog focuses the source search: type, Enter chooses the match.
  assert.ok(await inPicker("rule-source"), "the source search has the focus");
  await page.keyboard.type("web-1");
  await waitFor(page, async () => (await active("rule-source")).startsWith("web-1"), "web-1 highlighted");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Tab");
  assert.ok(await inPicker("rule-destination"), "Tab moves on to the destination");
  await page.keyboard.type("linux");
  await waitFor(page, async () => (await active("rule-destination")).startsWith("linux-servers"), "the Pack highlighted");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Tab");
  assert.ok(await inPicker("rule-service"), "then the service");
  // Escape closes the list, not the dialog.
  await page.keyboard.type("zz-nothing");
  await waitText(page, "No service matches");
  await page.keyboard.press("Escape");
  await waitFor(page, async () => !(await page.$('[data-testid="rule-service"] [role="listbox"]')), "the list closes");
  assert.ok(await page.$('[data-testid="rule-dialog"]'), "the dialog stays open");
  for (let i = 0; i < "zz-nothing".length; i++) await page.keyboard.press("Backspace");
  await page.keyboard.type("https");
  await waitFor(page, async () => (await active("rule-service")).startsWith("https"), "https highlighted");
  await page.keyboard.press("Enter");
  // The name follows the choices; a name typed in wins.
  assert.match(await page.$eval('[data-testid="rule-summary"]', (e) => e.innerText), /Allows https \(TCP 443\) from web-1 to linux-servers\./);
  await tabTo(page, () => document.activeElement?.tagName === "SUMMARY", "Name, description and status");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Tab");
  assert.equal(await page.evaluate(() => document.activeElement?.value), "allow-https-web-1-to-linux-servers");
  await page.keyboard.down("Control");
  await page.keyboard.press("a");
  await page.keyboard.up("Control");
  await page.keyboard.type("kbd-https");
  await shot(page, "keyboard-rule");
  await tabTo(page, () => document.activeElement?.textContent === "Add to draft", "Add to draft");
  await page.keyboard.press("Enter");
  await waitStatus("Unsaved changes");
  assert.match(await text(page), /kbd-https[\s\S]*web-1[\s\S]*linux-servers[\s\S]*https \(TCP 443\)/);
  await view("Topology");
  await waitFor(page, async () => (await edgeLabels()).some((l) => l === "https (TCP 443)"), "web-1 → linux-servers edge");
  await view("YAML");
  assert.match(await yamlValue(), /- name: kbd-https\n\s+from: web-1\n\s+to: linux-servers\n\s+allow:\n\s+- https/);
  await click(page, "button", "Discard changes");
  await waitStatus("Saved");
});

test("management path: explicit and indirect blocks are shown, highlighted and confirmed", async () => {
  await openDesigner("yaml");
  const y = await yamlValue();
  await setYAML(y + "  - name: lock-a\n    from: any\n    to: server-a\n    deny: [ssh]\n");
  await waitText(page, "Management path blocked");
  const t = await text(page);
  assert.match(t, /This change would prevent DROOL from managing server-a/);
  assert.match(t, /Required path:[\s\S]*DROOL Controller[\s\S]*→ jump-a : TCP \d+[\s\S]*→ server-a : TCP 22/);
  assert.match(t, /Blocked segment:\s*jump-a → server-a : TCP 22 by rule lock-a/);
  await click(page, "button", "Show on the topology");
  await waitFor(page, async () => (await edgeLabels()).some((l) => l.startsWith("BLOCKED · Protected DROOL management path")), "blocked overlay");
  await shot(page, "mgmt-blocked-topology");
  await view("Rules");
  assert.match(await text(page), /lock-a[\s\S]*blocks the management path/);
  await click(page, "button", "Save policy");
  await waitText(page, "Save a policy that blocks DROOL's management path?");
  await shot(page, "mgmt-save-confirm");
  await click(page, "button", "Keep editing");

  // Indirect: jump-a joins a group that may not SSH anywhere internal.
  await view("YAML");
  await setYAML(baseline.replace("policy:\n", "  bastions:\n    members: [jump-a]\npolicy:\n  - name: no-bastion-ssh\n    from: bastions\n    to: server-b\n    deny: [ssh]\n"));
  await waitText(page, "This change would prevent DROOL from managing server-b");
  assert.match(await text(page), /by rule no-bastion-ssh/);
  // Removing the allow that looks like management does not cut it.
  await setYAML(baseline.replace("  - name: jump-to-servers\n    from: jump-a\n    to: linux-servers\n    allow: [ssh]\n", ""));
  await waitStatus("Unsaved changes");
  assert.doesNotMatch(await text(page), /Management path blocked/);
  await click(page, "button", "Discard changes");
  await waitStatus("Saved");
});

test("inspector: edge rules, host explain and simulation on the draft", async () => {
  // A rule to inspect.
  const cur = await api(page, "GET", "/firewall/document");
  await api(page, "PUT", "/firewall/document", { yaml: baseline + "  - name: a-to-b\n    from: server-a\n    to: server-b\n    allow: [https]\n", revision: cur.body.revision });
  await openDesigner("topology");
  const label = await page.evaluateHandle(() => [...document.querySelectorAll("[data-edge-label]")].find((x) => x.innerText.trim() === "https (TCP 443)"));
  await label.asElement().click();
  await waitFor(page, async () => (await text(page)).includes("Edit rule"), "edge selected");
  await waitText(page, "Affected hosts: server-b");
  await page.select('[data-testid="simulate"] select[aria-label="From host"]', page.ids["server-a"]);
  await page.select('[data-testid="simulate"] select[aria-label="To host"]', page.ids["server-b"]);
  const port = await page.$('[data-testid="simulate"] input[aria-label="Simulation port"]');
  assert.equal(await port.evaluate((x) => x.value), "443", "the edge's service is the default question");
  await click(page, '[data-testid="simulate"] button', "Ask");
  await waitFor(page, async () => (await page.$eval('[data-testid="simulate"]', (e) => e.innerText)).match(/YES[\s\S]*a-to-b/), "YES by a-to-b");
  await port.focus();
  await page.keyboard.down("Control");
  await page.keyboard.press("a");
  await page.keyboard.up("Control");
  await port.type("5432");
  await click(page, '[data-testid="simulate"] button', "Ask");
  await waitFor(page, async () => (await page.$eval('[data-testid="simulate"]', (e) => e.innerText)).includes("NO"), "NO for 5432");
  await shot(page, "simulate");

  await click(page, ".react-flow__node", "web-1");
  await waitFor(page, async () => /inbound \(to this host\)/i.test(await text(page)), "host explain");
  const t = await text(page);
  assert.match(t, /DROOL management[\s\S]*SSH management from the controller/);
  assert.match(t, /allow\s*TCP 80, 443\s*from\s*the internet/);
  await shot(page, "explain-web-1");
});

test("emergency override needs explicit confirmation", async () => {
  // The test containers can't enforce, and the review offers to apply only
  // where DROOL can: web-1's firewall state says it can (plain nftables, in
  // Enforce). This checks the review's overrides; nothing is applied.
  const real = (await api(page, "GET", "/firewall/hosts/" + page.ids["web-1"])).body;
  const plan = { ...real, config: { ...real.config, mode: "enforce" }, transport: "ssh", pending_transaction: undefined,
    eligibility: { supported: true, reasons: [], detected: ["nftables v1.0.9"] } };
  await page.setRequestInterception(true);
  page.on("request", (r) => (r.method() === "GET" && new URL(r.url()).pathname === "/api/v1/firewall/hosts/" + page.ids["web-1"]
    ? r.respond({ status: 200, contentType: "application/json", body: JSON.stringify(plan) }) : r.continue()));
  // A host's firewall has its own page now (the host overview summarizes it).
  await page.goto(`${base}/hosts/${page.ids["web-1"]}/firewall`, { waitUntil: "networkidle0" });
  await click(page, "button", "Review and apply policy…");
  await waitText(page, "Overrides (audited)");
  await click(page, "summary", "Overrides");
  await click(page, "label", "Emergency override");
  await waitText(page, "you are about to apply a policy that DROOL");
  const apply = async () => page.$$eval("button", (bs) => bs.find((b) => b.textContent.includes("Apply with emergency override"))?.disabled);
  assert.equal(await apply(), true, "disabled before confirming");
  await page.type('input[aria-label="Type OVERRIDE to confirm the emergency override"]', "OVERRIDE");
  assert.equal(await apply(), true, "still needs a reason");
  await (await (await page.evaluateHandle(() => [...document.querySelectorAll("label")].find((l) => l.textContent.includes("Reason (recorded"))?.querySelector("input"))).asElement()).type("recovering from a typo");
  assert.equal(await apply(), false);
  await shot(page, "override-confirm");
  await page.keyboard.press("Escape");
  await page.setRequestInterception(false);
});

test("layout persists; moving boxes doesn't change the policy", async () => {
  await openDesigner("topology");
  const node = await page.$('.react-flow__node[data-id="host:web-1"]');
  const b = await node.boundingBox();
  await page.mouse.move(b.x + 20, b.y + 10);
  await page.mouse.down();
  await page.mouse.move(b.x + 160, b.y + 90, { steps: 10 });
  await page.mouse.up();
  await sleep(1200);
  const before = (await api(page, "GET", "/firewall/layout")).body.nodes?.["host:web-1"];
  assert.ok(before, "position stored");
  assert.ok((await status()).includes("Saved"), "moving a box is not a policy change");
  await page.reload({ waitUntil: "networkidle0" });
  await waitFor(page, async () => !!(await page.$('.react-flow__node[data-id="host:web-1"]')), "reloaded");
  const after = (await api(page, "GET", "/firewall/layout")).body.nodes["host:web-1"];
  assert.deepEqual(after, before);
  assert.deepEqual(page.errors, []);
});
