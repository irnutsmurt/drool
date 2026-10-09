// Desired state (M14): the sidebar entry and tabs from the keyboard; a
// baseline created in the editor with its impact shown before saving;
// a host's effective state with provenance (compliant, drift, unknown);
// a conflict between groups resolved by a priority with its impact;
// Review difference, Restore managed state (an ordinary job, then a
// check) and Accept current state on a real container; automatic restore
// controls; the drift warning on New job. Needs test/integration
// TestBrowser's environment (container host web-1; fixture hosts gpu-01
// and pi-sd with monitoring data from testdata/monitor.json).
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, field, fill, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;
const run = Date.now().toString(36).slice(-5); // names unique per run

before(async () => {
  browser = await launch();
});
after(async () => {
  // Leave the controller as other test files expect it: archive this
  // run's baselines and remove its groups.
  if (page) {
    for (const b of (await api(page, "GET", "/desired/baselines")).body?.items ?? []) {
      if (b.name.endsWith(run)) await api(page, "DELETE", `/desired/baselines/${b.id}`);
    }
    for (const g of (await api(page, "GET", "/groups")).body?.items ?? []) {
      if (g.name.endsWith(run)) await api(page, "DELETE", `/groups/${g.id}`);
    }
  }
  await browser?.close();
});
beforeEach(async () => {
  page?.close().catch(() => {});
  page = await session(browser);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  page.on("dialog", (d) => d.accept());
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  page.ids = Object.fromEntries(hosts.map((h) => [h.name, h.id]));
});

const go = async (path) => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await sleep(500);
};

/** Ticks the checkbox whose label starts with `name`. */
async function check(name) {
  const ok = await page.evaluate((n) => {
    for (const l of document.querySelectorAll("label")) {
      const box = l.querySelector('input[type="checkbox"]');
      if (box && l.innerText.trim().startsWith(n)) {
        if (!box.checked) box.click();
        return true;
      }
    }
    return false;
  }, name);
  assert.ok(ok, "no checkbox " + name);
  await sleep(150);
}

/** Waits until the page's text matches re (headings may be upper-cased by CSS). */
const waitRe = (re, ms) => waitFor(page, async () => re.test(await text(page)), String(re), ms);

/** Clicks the first enabled element matching selector whose text is exactly label. */
async function clickExact(selector, label) {
  await waitFor(page, async () => page.$$eval(selector, (els, l) => els.some((e) => e.textContent.trim() === l && !e.disabled), label), "button " + label);
  const els = await page.$$(selector);
  for (const el of els) {
    if (await el.evaluate((e, l) => e.textContent.trim() === l && !e.disabled, label)) {
      await el.click();
      return;
    }
  }
}

/** The resource row (a button) on a host's desired-state page. */
const openResource = async (name) => {
  await click(page, 'ul[aria-label="Managed resources"] > li > button', name);
  await sleep(500);
};

const resource = async (host, key) => (await api(page, "GET", `/desired/hosts/${page.ids[host]}/resource?key=${encodeURIComponent(key)}`)).body.resource;

const waitStatus = async (host, key, status, ms = 120000) => {
  let last = 0;
  await waitFor(page, async () => {
    if (Date.now() - last > 8000) {
      last = Date.now();
      await api(page, "POST", `/desired/hosts/${page.ids[host]}/refresh`);
    }
    return (await resource(host, key))?.status === status;
  }, `${host} ${key} ${status}`, ms);
};

test("desired state: sidebar entry and tabs from the keyboard", async () => {
  await go("/");
  await tabTo(page, () => document.activeElement?.textContent === "Desired state", "Desired state link");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => page.url().endsWith("/desired"), "desired url");
  await waitText(page, "What your machines should look like");
  const tabs = await page.$$eval('nav[aria-label="Desired state"] a', (as) => as.map((a) => [a.textContent, a.getAttribute("aria-current")]));
  assert.deepEqual(tabs, [["Overview", "page"], ["Baselines", null], ["History", null]]);
  assert.match(await text(page), /What your machines should look like/);
  await shot(page, "desired-overview-first");
  assert.deepEqual(page.errors, []);
});

test("create a baseline: simple case, impact before saving", async () => {
  await go("/desired/baselines/new");
  await waitText(page, "Impact before saving");
  await fill(page, "Name", `AI box ${run}`);
  await click(page, "label", "Groups and hosts I choose");
  await check("gpu-01");
  // The first row is a service: running and enabled by default.
  await fill(page, "Service", "ollama");
  await click(page, "button", "Add service");
  const services = await page.$$('input[placeholder="jellyfin.service"]');
  await services[1].type("nvidia-persistenced");
  const rows = await page.$$('ul[aria-label="Resources"] > li');
  const selects = await rows[1].$$("select");
  await selects[0].select("stopped");
  await selects[1].select("");
  await click(page, "button", "Add package");
  await (await page.$('input[placeholder="curl"]')).type(`e2e-pkg-${run}`);
  const last = (await page.$$('ul[aria-label="Resources"] > li')).pop();
  await (await last.$("select")).select("absent");
  // The server says what saving would do, before anything is saved.
  await waitText(page, "1 host affected", 30000);
  await waitText(page, "would drift");
  const t = await text(page);
  assert.match(t, /\+ ollama\.service: running, enabled at boot/);
  assert.match(t, /nvidia-persistenced\.service: stopped — differs \(state: stopped → running\)/);
  assert.match(t, new RegExp(`package e2e-pkg-${run}: absent — checked after saving`));
  await shot(page, "baseline-editor-impact");
  await click(page, "button", "Create baseline");
  await waitFor(page, async () => /\/desired\/baselines\/bl_/.test(page.url()), "baseline page");
  await waitText(page, "Revision 1");
  assert.match(await text(page), /applies to gpu-01/);
  await shot(page, "baseline-detail");
  assert.deepEqual(page.errors, []);
});

test("a host's effective desired state: compliant, drift, unknown and where each comes from", async () => {
  await waitStatus("gpu-01", "service:nvidia-persistenced.service", "drift");
  await go(`/hosts/${page.ids["gpu-01"]}/desired`);
  await waitText(page, "Managed resources");
  let t = await text(page);
  assert.match(t, /AI box .*rev\. 1, via this host/);
  await openResource("nvidia-persistenced.service");
  await waitRe(/where the desired value comes from/i);
  t = await text(page);
  assert.match(t, /Baseline AI box .*, assigned to this host/);
  assert.match(t, /State\s+stopped\s+running/);
  assert.match(t, /Drift detected/);
  await shot(page, "host-drift-detail");
  await openResource("nvidia-persistenced.service");
  // A package on an unreachable host is unknown, with the reason — never compliant.
  const pkg = await resource("gpu-01", `package:e2e-pkg-${run}`);
  assert.equal(pkg.status, "unknown");
  assert.match(pkg.note, /couldn't check/);
  const ol = await resource("gpu-01", "service:ollama.service");
  assert.equal(ol.status, "compliant");
  await openResource(`e2e-pkg-${run}`);
  await waitText(page, "couldn't check");
  await shot(page, "host-unknown");
  // The host overview links here; the dashboard counts the drift.
  await go(`/hosts/${page.ids["gpu-01"]}`);
  await waitText(page, "nvidia-persistenced.service — drift detected");
  await go("/");
  await waitText(page, "with drift");
  assert.deepEqual(page.errors, []);
});

test("equal group priorities that disagree are a conflict until a priority settles it", async () => {
  const g1 = (await api(page, "POST", "/groups", { name: `storage-${run}` })).body.id;
  const g2 = (await api(page, "POST", "/groups", { name: `quiet-${run}` })).body.id;
  for (const g of [g1, g2]) await api(page, "PATCH", `/groups/${g}`, { host_ids: [page.ids["pi-sd"]] });
  const mk = (name, g, state) => api(page, "POST", "/desired/baselines", { name: `${name} ${run}`, assignments: [{ scope: "group", group_id: g }],
    resources: [{ kind: "service", name: "pihole-FTL", state }] });
  assert.equal((await mk("Storage", g1, "running")).status, 201);
  assert.equal((await mk("Quiet", g2, "stopped")).status, 201);
  await waitStatus("pi-sd", "service:pihole-FTL.service", "conflict");
  await go(`/hosts/${page.ids["pi-sd"]}/desired`);
  await openResource("pihole-FTL.service");
  await waitRe(/competing declarations/i);
  const t = await text(page);
  assert.match(t, new RegExp(`Conflict between groups .*quiet-${run}.*storage-${run}.* \\(same priority 0\\)`));
  assert.match(t, /DROOL doesn't pick one/);
  const restore = await page.$$eval("button", (bs) => bs.filter((b) => b.textContent === "Restore managed state").map((b) => b.disabled));
  assert.deepEqual(restore, [true]);
  await shot(page, "host-conflict");
  // Groups page: change a priority, with its impact before saving.
  await go("/groups");
  await waitText(page, `storage-${run}`);
  const buttons = await page.$$("button");
  for (const b of buttons) {
    const card = await b.evaluate((x) => x.closest("section")?.innerText ?? "");
    if ((await b.evaluate((x) => x.textContent)) === "Change priority" && card.startsWith(`storage-${run}`)) {
      await b.click();
      break;
    }
  }
  await waitText(page, "Desired-state priority");
  await fill(page, "Priority", "10");
  await waitText(page, "1 conflict resolved");
  await shot(page, "group-priority-impact");
  await click(page, '[role="dialog"] button', "Save");
  await waitFor(page, async () => (await resource("pi-sd", "service:pihole-FTL.service")).status !== "conflict", "conflict resolved", 30000);
  const r = await resource("pi-sd", "service:pihole-FTL.service");
  assert.equal(r.status, "compliant");
  assert.match(r.source.label, new RegExp(`Inherited from group: storage-${run}`));
  assert.deepEqual(page.errors, []);
});

test("review difference, restore managed state and accept current state on a real host", async () => {
  const web = page.ids["web-1"];
  const art = await page.evaluate(async () => {
    const me = await (await fetch("/api/v1/auth/me")).json();
    const r = await fetch("/api/v1/artifacts?name=e2e.conf", { method: "POST", headers: { "X-DROOL-CSRF": me.csrf_token }, body: "managed = yes\n" });
    return (await r.json()).sha256;
  });
  const path = `/etc/drool-e2e-${run}.conf`;
  const b = await api(page, "POST", "/desired/baselines", { name: `Web config ${run}`, assignments: [{ scope: "host", host_id: web }],
    resources: [{ kind: "file", path, artifact: art, artifact_name: "e2e.conf", owner: "root", group: "root", mode: "0644", elevated: true }] });
  assert.equal(b.status, 201, JSON.stringify(b.body));
  await waitStatus("web-1", `file:${path}`, "drift");
  await go(`/hosts/${web}/desired`);
  await openResource(path);
  await click(page, "button", "Review difference");
  await waitText(page, "The file is missing on the host");
  assert.match(await text(page), /Read-only: reviewing never changes the host/);
  await shot(page, "review-missing-file");
  await page.keyboard.press("Escape");
  await sleep(300);
  // Restore: the exact plan first, then one ordinary job.
  await clickExact('ul[aria-label="Managed resources"] li button', "Restore managed state");
  await waitText(page, "as one ordinary job", 60000);
  assert.match(await text(page), new RegExp(`Deploy ${path.replace(/\./g, "\\.")} — on 1 host`));
  await shot(page, "restore-plan");
  await click(page, "button", "Restore 1 resource");
  await waitFor(page, async () => /\/jobs\/job_/.test(page.url()), "job page");
  await waitText(page, "Restore managed state, started by");
  const job = page.url().split("/").pop();
  await waitFor(page, async () => (await api(page, "GET", `/jobs/${job}`)).body.status === "completed", "job done", 120000);
  await waitFor(page, async () => (await resource("web-1", `file:${path}`)).status === "compliant", "restored", 60000);
  const hist = (await api(page, "GET", `/desired/history?host=${web}&key=${encodeURIComponent("file:" + path)}`)).body.items;
  assert.equal(hist[0].kind, "restored");
  assert.equal(hist[0].resolution, "restored");
  // Someone changes it on the host; accepting keeps it for this host only.
  const cmd = await api(page, "POST", "/jobs", { steps: [{ type: "command", command: `echo 'local = 1' | sudo tee -a ${path}` }], targets: { hosts: [web] } });
  await waitFor(page, async () => (await api(page, "GET", `/jobs/${cmd.body.id}`)).body.status === "completed", "edit job", 60000);
  await waitStatus("web-1", `file:${path}`, "drift");
  await go(`/hosts/${web}/desired`);
  await openResource(path);
  await click(page, "button", "Accept current state");
  await waitText(page, "this host's own desired state");
  await fill(page, "Note (optional)", "local tweak");
  await click(page, '[role="dialog"] button', "Accept current state");
  await waitText(page, "Accepted.");
  await shot(page, "accept-result");
  await click(page, '[role="dialog"] button', "Done");
  const r = await resource("web-1", `file:${path}`);
  assert.equal(r.status, "compliant");
  assert.equal(r.source.label, "Custom for this host");
  const h2 = (await api(page, "GET", `/desired/history?host=${web}&key=${encodeURIComponent("file:" + path)}`)).body.items;
  assert.equal(h2[0].kind, "accepted");
  assert.equal(h2[0].resolution, "configuration");
  assert.equal((await api(page, "GET", `/desired/baselines/${b.body.id}`)).body.revision, 1);
  // Reset to inherited: the host follows its baseline again (and differs).
  await go(`/hosts/${web}/desired`);
  await openResource(path);
  await clickExact('ul[aria-label="Managed resources"] li button', "Reset to inherited");
  await waitFor(page, async () => (await resource("web-1", `file:${path}`)).source.level === "assigned", "reset", 20000);
  assert.deepEqual(page.errors, []);
});

test("automatic restore: off by default, switched on in Advanced, shown with its owner", async () => {
  const bl = (await api(page, "GET", "/desired/baselines")).body.items.find((x) => x.name === `AI box ${run}`);
  assert.equal(bl.auto.enabled, false);
  await go(`/desired/baselines/${bl.id}/edit`);
  await click(page, "button", "Advanced: automatic restore (off)");
  await check("Automatically restore managed state");
  await waitText(page, "Rollout of automatic corrections");
  await shot(page, "baseline-auto-advanced");
  // Nothing else changed: saving only turns automatic restore on.
  await waitFor(page, async () => page.$$eval("button", (bs) => bs.some((b) => /Save revision/.test(b.textContent) && !b.disabled)), "save enabled", 20000);
  await click(page, "button", "Save revision");
  await waitFor(page, async () => !page.url().endsWith("/edit"), "back on the baseline");
  await waitText(page, "owned by");
  await shot(page, "baseline-auto-on");
  const after = (await api(page, "GET", `/desired/baselines/${bl.id}`)).body;
  assert.equal(after.auto.enabled, true);
  await click(page, "button", "Turn off");
  await waitText(page, "Off: drift is shown");
  assert.deepEqual(page.errors, []);
});

test("new job: a step that contradicts desired state is warned about", async () => {
  await go("/jobs/new");
  await click(page, "button", "Service");
  await fill(page, "Service", "nvidia-persistenced");
  await (await field(page, "Action")).select("start");
  await check("gpu-01");
  await waitText(page, "This contradicts desired state", 20000);
  assert.match(await text(page), /On gpu-01: nvidia-persistenced\.service is managed to stay stopped \(Baseline AI box .*, assigned to this host\)\. Starting it will create desired-state drift\./);
  await shot(page, "new-job-desired-warning");
  assert.deepEqual(page.errors, []);
});
