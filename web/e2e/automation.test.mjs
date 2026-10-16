// Scheduling, workflows and rollouts (M13): the Jobs area's tabs, a
// workflow from a template with revisions, "Schedule this run" with the
// recommended rollout and a preview of its waves, Run now, the newer
// revision banner, a recurring maintenance window, a canary pause and a
// failed verification. Needs test/integration TestBrowser's environment
// (container hosts web-1 and server-a..c).
import { test, after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { api, base, click, confirmIn, field, fill, launch, session, shot, sleep, tabTo, text, waitFor, waitText } from "./lib.mjs";

let browser;
let page;

before(async () => {
  browser = await launch();
});
after(async () => {
  await browser?.close();
});
beforeEach(async () => {
  page?.close().catch(() => {});
  page = await session(browser);
  await page.evaluate(() => localStorage.setItem("drool.theme", "light"));
  const hosts = (await api(page, "GET", "/hosts")).body.items;
  page.ids = Object.fromEntries(hosts.map((h) => [h.name, h.id]));
});

const go = async (path) => {
  await page.goto(base + path, { waitUntil: "networkidle0" });
  await sleep(400);
};
// A running or paused job's page keeps its live updates (SSE) open, so
// the network never goes idle there.
const goLive = async (path) => {
  await page.goto(base + path, { waitUntil: "load" });
  await sleep(600);
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

/** Chooses a radio "pill" or option by its label text. */
async function choose(label) {
  await click(page, "label", label);
  await sleep(150);
}

/** Empties a number or text field the way a person does. */
async function clear(label) {
  const el = await field(page, label);
  await el.focus();
  await page.keyboard.down("Control");
  await page.keyboard.press("a");
  await page.keyboard.up("Control");
  await page.keyboard.press("Backspace");
  await sleep(150);
}

async function select(label, value) {
  const el = await field(page, label);
  await el.select(value);
  await sleep(150);
}

const waitJobDone = (id) => waitFor(page, async () => ["completed", "cancelled", "interrupted"].includes((await api(page, "GET", `/jobs/${id}`)).body.status), "job " + id, 120000);

test("Runs, Schedules and Workflows: peer sidebar destinations, from the keyboard", async () => {
  await go("/jobs");
  const automate = () => page.$$eval('nav[aria-label="Main"] ul[aria-labelledby="nav-automate"] a', (as) => as.map((a) => [a.textContent.trim(), a.getAttribute("aria-current")]));
  assert.deepEqual(await automate(), [["Runs", "page"], ["Schedules", null], ["Workflows", null], ["Desired State", null]]);
  assert.match(await text(page), /A run is one execution on chosen hosts/);
  await tabTo(page, () => document.activeElement?.textContent === "Schedules", "Schedules link");
  await page.keyboard.press("Enter");
  await waitFor(page, async () => page.url().endsWith("/jobs/schedules"), "schedules url");
  await waitText(page, "No schedules yet");
  assert.match(await text(page), /Every run is an ordinary job you'll find under Runs/);
  // The page under /jobs/schedules marks Schedules, not Runs; no tabs repeat the sidebar.
  assert.deepEqual(await automate(), [["Runs", null], ["Schedules", "page"], ["Workflows", null], ["Desired State", null]]);
  assert.equal(await page.$('nav[aria-label="Jobs"]'), null);
  await shot(page, "schedules-empty");
  // A run's page keeps Runs marked.
  await go("/jobs/new");
  assert.deepEqual((await automate())[0], ["Runs", "page"]);
  assert.deepEqual(page.errors, []);
});

test("workflow from a template, then a new revision", async () => {
  await go("/jobs/workflows/new");
  await waitText(page, "Update and verify services");
  assert.match(await text(page), /Safe fleet update/);
  await shot(page, "workflow-templates");
  await click(page, "button", "Use “Update and verify services”");
  await waitText(page, "Step 4: Verify health");
  const t = await text(page);
  for (const s of ["Step 1: System update", "Step 2: Verify host is reachable", "Step 3: Verify services are running"]) assert.ok(t.includes(s), s);
  assert.match(t, /To save: name at least one service/);
  await fill(page, "Services", "ssh jellyfin");
  await click(page, "button", "Create workflow");
  await waitFor(page, async () => /\/jobs\/workflows\/wf_/.test(page.url()), "workflow page");
  await waitText(page, "Revision 1");
  // Reorder from the keyboard: move the health check up, save a new revision.
  await tabTo(page, () => document.activeElement?.getAttribute("aria-label") === "Move step 4 up", "Move step 4 up", 100);
  await page.keyboard.press("Enter");
  await waitText(page, "Step 3: Verify health");
  await click(page, "button", "Save as a new revision");
  await waitText(page, "Revision 2");
  await shot(page, "workflow-revisions");
  const id = page.url().split("/").pop();
  const wf = (await api(page, "GET", `/workflows/${id}`)).body;
  assert.equal(wf.latest_revision, 2);
  assert.deepEqual(wf.steps.map((s) => s.type), ["system_update", "verify_host", "verify_health", "verify_services"]);
  assert.deepEqual(wf.steps[3].services, ["jellyfin.service", "ssh.service"]);
  // Revision 1 is kept as it was.
  await click(page, "button", "View revision 1");
  await waitText(page, "Verify services are running: jellyfin.service, ssh.service");
  await page.keyboard.press("Escape");
  // Scheduling it offers today's package dry run (read-only).
  await click(page, "main a", "Schedule"); // in the page, not the sidebar's Schedules
  await waitFor(page, async () => page.url().includes("/jobs/schedules/new?workflow="), "schedule editor for the workflow");
  await waitText(page, "Runs revision 2 exactly as it is now");
  await check("web-1");
  await click(page, "button", "Check package changes now");
  await waitFor(page, async () => (await page.$$eval('[aria-label="Package changes per host"] li', (l) => l.map((x) => x.innerText).join("\n"))).includes("web-1"),
    "package dry run", 120000);
  await shot(page, "schedule-package-check");
  assert.deepEqual(page.errors, []);
});

test("schedule this job: recommended rollout, preview waves, run now, history, disable", async () => {
  await go("/jobs/new");
  await fill(page, "Command", "echo scheduled-$(hostname)");
  await check("web-1");
  await check("server-a");
  // A change on several hosts: the recommended rollout is the default.
  await waitText(page, "Recommended for 2 hosts: 1 canary host first");
  await waitText(page, "Canary");
  await shot(page, "new-job-rollout");
  await click(page, "button", "Schedule this run");
  await waitFor(page, async () => page.url().endsWith("/jobs/schedules/new"), "schedule editor");
  await waitText(page, "Step 1: Command");
  await fill(page, "Name", "Nightly echo");
  await choose("Every day");
  await select("Time zone", "UTC");
  await waitText(page, "Every day at 03:00 (UTC)");
  // The preview resolves today's targets into the exact waves.
  await waitFor(page, async () => (await page.$$eval('[aria-label="Rollout waves"] li', (l) => l.map((x) => x.innerText.replace(/\s+/g, " ")))).join("|") === "Canary server-a|Batch 1 web-1",
    "preview waves");
  const t = await text(page);
  assert.match(t, /Targets are resolved again at every run/);
  assert.match(t, /skipped and recorded as missed/);
  await shot(page, "schedule-editor-preview");
  await click(page, "button", "Create schedule");
  await waitFor(page, async () => /\/jobs\/schedules\/sch_/.test(page.url()), "schedule page");
  await waitText(page, "Nothing yet");
  const id = page.url().split("/").pop();
  assert.match(await text(page), /Every day at 03:00 \(UTC\)/);
  // Run now: an ordinary job, with the schedule as its origin.
  await click(page, "button", "Run now");
  await waitText(page, "It runs with your permissions");
  await confirmIn(page, "Run now");
  await waitFor(page, async () => /\/jobs\/job_/.test(page.url()), "job page");
  const job = page.url().split("/").pop();
  await waitText(page, "Started with Run now from schedule");
  await waitText(page, "Rollout progress");
  await waitJobDone(job);
  await go(`/jobs/${job}`);
  await waitText(page, "2 succeeded");
  await shot(page, "job-from-schedule");
  await go(`/jobs/schedules/${id}`);
  await waitText(page, "Run now by");
  assert.match(await text(page), /succeeded/);
  // Disabling keeps everything; the list says so.
  await click(page, "button", "Disable");
  await waitText(page, "disabled");
  await go("/jobs/schedules");
  await waitText(page, "Nightly echo");
  assert.match(await text(page), /disabled/);
  await shot(page, "schedules-list");
  assert.deepEqual(page.errors, []);
});

test("new run: reviewed before it starts; a rollout the user chose is kept", async () => {
  await go("/jobs/new");
  await fill(page, "Command", "echo reviewed-$(hostname)");
  await fill(page, "Name", "Reviewed echo");
  await check("web-1");
  await check("server-a");
  await waitText(page, "Recommended for 2 hosts:");
  // The user's own rollout, every host at once: kept as the targets change,
  // with the recommendation offered beside it, never applied for them.
  await click(page, "button", "Change rollout…");
  await choose("No canary");
  await clear("Batch size");
  await clear("Pause after failures");
  await waitText(page, "Rollout: all hosts in one wave, up to 5 at a time, no failure limit.");
  await check("server-b");
  await waitText(page, "Recommended for 3 hosts:");
  assert.match(await text(page), /Rollout: all hosts in one wave/);
  // The review: what, on which hosts, how it rolls out and what that risks.
  await click(page, "button", "Review: run on 3 hosts");
  await page.waitForSelector('[data-testid="run-confirm"]');
  const review = await page.$eval('[data-testid="run-confirm"]', (d) => d.innerText);
  for (const s of ["Start this run on 3 hosts?", "echo reviewed-$(hostname)", "server-a", "server-b", "web-1", "all hosts in one wave", "Every host at once"]) {
    assert.ok(review.includes(s), `${s} in: ${review}`);
  }
  await shot(page, "run-review");
  // Escape: nothing starts.
  await page.keyboard.press("Escape");
  await waitFor(page, async () => (await page.$('[data-testid="run-confirm"]')) === null, "the review closed");
  assert.ok(!(await api(page, "GET", "/jobs")).body.items.some((j) => j.name === "Reviewed echo"), "no run after Escape");
  // The recommendation, chosen; then Start.
  await click(page, "button", "Use recommended rollout");
  await waitText(page, "Rollout: 1 canary host first");
  await click(page, "button", "Review: run on 3 hosts");
  await page.waitForSelector('[data-testid="run-confirm"]');
  assert.match(await page.$eval('[data-testid="run-confirm"]', (d) => d.innerText), /1 canary host first, then batches of 5/);
  await confirmIn(page, "Start run on 3 hosts", { testId: "run-confirm", wait: false });
  await waitFor(page, async () => /\/jobs\/job_/.test(page.url()), "job page");
  const job = page.url().split("/").pop();
  await waitJobDone(job);
  const spec = (await api(page, "GET", `/jobs/${job}`)).body.spec;
  assert.equal(spec.strategy.canary_count, 1);
  assert.equal(spec.strategy.batch_size, 5);
  assert.deepEqual(page.errors, []);
});

test("a schedule keeps its workflow revision until it's updated", async () => {
  const wf = (await api(page, "POST", "/workflows", { name: "Echo version", steps: [{ type: "command", command: "echo v1", read_only: true }] })).body;
  const sc = await api(page, "POST", "/schedules", {
    name: "Weekly echo", time_zone: "UTC", recurrence: { kind: "weekly", time: "04:00", weekdays: ["sun"] },
    targets: { hosts: [page.ids["web-1"]] }, workflow: { id: wf.id },
  });
  assert.equal(sc.status, 201, JSON.stringify(sc.body));
  await api(page, "PUT", `/workflows/${wf.id}`, { name: "Echo version", steps: [{ type: "command", command: "echo v2", read_only: true }] });
  await go(`/jobs/schedules/${sc.body.id}`);
  await waitText(page, "A newer workflow revision is available");
  await shot(page, "schedule-newer-revision");
  await click(page, "main a", "Review and update");
  await waitText(page, "Use revision 2");
  await click(page, "button", "Use revision 2");
  await click(page, "button", "Save changes");
  await waitFor(page, async () => page.url().endsWith(sc.body.id), "back on the schedule");
  await waitFor(page, async () => !(await text(page)).includes("A newer workflow revision is available"), "banner gone");
  assert.match(await text(page), /revision 2/);
  const after = (await api(page, "GET", `/schedules/${sc.body.id}`)).body;
  assert.equal(after.config.workflow.revision, 2);
  assert.deepEqual(page.errors, []);
});

test("recurring maintenance window from Alerts → Maintenance", async () => {
  await go("/alerts?view=maintenance");
  await waitText(page, "Recurring windows");
  await click(page, "main a", "New recurring window");
  await waitText(page, "New recurring maintenance window");
  await fill(page, "Name", "Sunday patching");
  await select("Time zone", "UTC");
  await check("web-1");
  await choose("2 hours");
  await fill(page, "Reason (recommended)", "Weekly patching");
  await waitText(page, "Each window lasts");
  assert.match(await text(page), /Every Sunday at 03:00 \(UTC\)/);
  await shot(page, "window-editor");
  await click(page, "button", "Create schedule");
  await waitFor(page, async () => /\/jobs\/schedules\/sch_/.test(page.url()), "window page");
  await waitText(page, "Recurring maintenance window");
  await go("/alerts?view=maintenance");
  await waitText(page, "Sunday patching");
  assert.match(await text(page), /for 2 hours/);
  await shot(page, "recurring-windows");
  assert.deepEqual(page.errors, []);
});

test("a failing canary pauses the rollout before anyone else starts", async () => {
  const hosts = ["server-a", "server-b", "server-c", "web-1"].map((n) => page.ids[n]);
  const j = (await api(page, "POST", "/jobs", {
    name: "Canary check", steps: [{ type: "command", command: "test $(hostname) != $(hostname)" }],
    targets: { hosts }, strategy: { concurrency: 5, canary_count: 1, batch_size: 2, failure_threshold: 2 },
  })).body;
  await goLive(`/jobs/${j.id}`);
  await waitText(page, "Paused: Canary server-a failed", 60000);
  const t = await text(page);
  assert.match(t, /3 hosts have not started/);
  assert.match(t, /Hosts that failed stay failed when you resume/);
  const waves = await page.$$eval('[aria-label="Rollout waves"] li', (l) => l.map((x) => x.innerText.replace(/\s+/g, " ")));
  assert.ok(/^Canary server-a 1 failed/.test(waves[0]), waves.join("|"));
  assert.ok(/^Batch 1 .*2 not started/.test(waves[1]), waves.join("|"));
  await shot(page, "job-canary-paused");
  await click(page, "main button", "Cancel");
  await waitText(page, "What already changed on a host stays changed.");
  await confirmIn(page, "Cancel the run");
  await waitText(page, "cancelled");
  assert.deepEqual(page.errors, []);
});

test("a failed check explains itself", async () => {
  const j = (await api(page, "POST", "/jobs", {
    name: "Check a service", steps: [{ type: "command", command: "true", read_only: true }, { type: "verify_services", services: ["drool-e2e-missing"], timeout_s: 5 }],
    targets: { hosts: [page.ids["web-1"]] },
  })).body;
  await waitJobDone(j.id);
  await go(`/jobs/${j.id}`);
  await waitText(page, "What ran (2 steps, in order on each host)");
  await waitText(page, "A check after the change didn't pass");
  assert.match(await text(page), /drool-e2e-missing\.service/);
  assert.match(await text(page), /Checked by DROOL \(nothing runs on the host\)/);
  await shot(page, "job-verification-failed");
  assert.deepEqual(page.errors, []);
});
