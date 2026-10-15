// Re-authentication asked for by an action inside a dialog (final After-M17
// batch): the "Confirm it's you" prompt used to open *behind* the dialog that
// asked (both shared one z-index, and the prompt came first in the DOM), so
// the action looked hung while focus sat in the hidden prompt. Now the prompt
// is the topmost layer with focus in it and the page inert; Escape or a click
// on its backdrop cancels only the prompt and changes nothing; confirming
// repeats the action exactly once; focus comes back to the dialog. On the
// desktop viewport and a phone.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { base, launch, session, waitText, waitFor, fill, click, shot, api, sleep } from "./lib.mjs";

const adminPassword = process.env.DROOL_E2E_PASSWORD ?? "a-long-admin-password";
let browser;
before(async () => { browser = await launch(); });
after(async () => browser?.close());

// Which dialogs are open (in DOM order), which one is painted at the middle
// of the prompt, which one holds focus, and whether the page is inert.
const layers = (page) => page.evaluate(() => {
  const label = (el) => el?.closest('[role="dialog"]')?.getAttribute("aria-label") ?? null;
  const prompt = document.querySelector('[data-testid="reauth-dialog"]')?.closest('[role="dialog"]');
  let top = null;
  if (prompt) {
    const r = prompt.getBoundingClientRect();
    top = label(document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(40, r.height / 2)));
  }
  return { open: [...document.querySelectorAll('[role="dialog"]')].map((d) => d.getAttribute("aria-label")), top,
    focus: label(document.activeElement), rootInert: document.getElementById("root").inert };
});

// Whether a user can sign in with a password, in a context of its own.
async function signsIn(user, password) {
  const ctx = await browser.createBrowserContext();
  try {
    const page = await ctx.newPage();
    await page.goto(base + "/", { waitUntil: "networkidle0" });
    return await page.evaluate(async (u, p) => (await fetch("/api/v1/auth/login", { method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: u, password: p }) })).status === 200, user, password);
  } finally {
    await ctx.close();
  }
}

for (const vp of [{ name: "desktop" }, { name: "phone", mobile: true, width: 390, height: 844 }]) {
  test(`re-authentication from inside a dialog (${vp.name})`, async () => {
    const page = await session(browser, vp);
    const name = `e2e-pw-${vp.name}-${Date.now().toString(36)}`;
    const created = await api(page, "POST", "/users", { username: name, password: "first-password-123", role: "viewer" });
    assert.equal(created.status, 201, JSON.stringify(created.body));
    try {
      await page.goto(base + "/settings/users", { waitUntil: "networkidle0" });
      await sleep(3500); // past the test controller's re-authentication window (3 s)
      await click(page, "main button", `Set password for ${name}`);
      const title = `Set a new password for ${name}`;
      await waitFor(page, async () => (await layers(page)).open.includes(title), "the dialog");
      await fill(page, "New password", "second-password-456");
      await fill(page, "New password again", "second-password-456");
      const submit = await click(page, '[role="dialog"] button', "Set password");

      // The prompt is on top of the dialog that asked, with focus, and the
      // page and the dialog beneath don't react.
      await page.waitForSelector('[data-testid="reauth-dialog"]');
      let l = await layers(page);
      assert.deepEqual(l.open, [title, "Confirm it's you"]);
      assert.equal(l.top, "Confirm it's you", "the prompt is painted on top");
      assert.equal(l.focus, "Confirm it's you", "focus is in the prompt");
      assert.ok(l.rootInert, "the page beneath is inert");
      assert.ok(await submit.evaluate((b) => !!b.closest("[inert]")), "the dialog beneath is inert");
      assert.match(await page.$eval('[data-testid="reauth-reason"]', (e) => e.textContent), /another user's password/);
      for (let i = 0; i < 6; i++) await page.keyboard.press("Tab"); // Tab stays in the prompt
      assert.equal((await layers(page)).focus, "Confirm it's you");
      await shot(page, `reauth-over-dialog-${vp.name}`);

      // Escape cancels the prompt only: the dialog stays, focus returns to
      // it, the refusal is shown there, and nothing changed.
      await page.keyboard.press("Escape");
      await waitFor(page, async () => (await page.$('[data-testid="reauth-dialog"]')) === null, "the prompt closed");
      l = await layers(page);
      assert.deepEqual(l.open, [title]);
      assert.equal(l.focus, title, "focus is back in the dialog");
      assert.ok(!(await submit.evaluate((b) => !!b.closest("[inert]"))), "the dialog reacts again");
      await waitText(page, "Confirm it's you: this action needs your password again");
      assert.ok(await signsIn(name, "first-password-123"), "cancelled: the password is unchanged");

      // A click on the backdrop beside the prompt cancels the prompt only.
      await click(page, '[role="dialog"] button', "Set password");
      await page.waitForSelector('[data-testid="reauth-dialog"]');
      const vh = page.viewport().height;
      await page.mouse.click(4, vh - 4);
      await waitFor(page, async () => (await page.$('[data-testid="reauth-dialog"]')) === null, "the prompt closed by its backdrop");
      assert.deepEqual((await layers(page)).open, [title], "the dialog beneath stays open");
      assert.ok(await signsIn(name, "first-password-123"), "cancelled again: the password is unchanged");

      // Again, and confirm: the action is repeated once and succeeds.
      await click(page, '[role="dialog"] button', "Set password");
      await page.waitForSelector('[data-testid="reauth-dialog"]');
      await fill(page, "Password", adminPassword);
      await click(page, '[data-testid="reauth-dialog"] button', "Confirm");
      await waitText(page, `Password set for ${name}`);
      l = await layers(page);
      assert.deepEqual(l.open, []);
      assert.ok(!l.rootInert, "the page reacts again");
      assert.ok(!(await signsIn(name, "first-password-123")) && (await signsIn(name, "second-password-456")), "the new password works, the old one doesn't");
      const audit = (await api(page, "GET", `/audit?target=${created.body.id}&action=user.update&limit=50`)).body.items
        .filter((e) => e.action === "user.update" && e.result === "success" && JSON.stringify(e.details).includes('"password"'));
      assert.equal(audit.length, 1, "exactly one password change: " + JSON.stringify(audit));
      assert.ok(!JSON.stringify(audit).includes("second-password-456"), "the password isn't in the audit log");
      assert.deepEqual(page.errors, []);
    } finally {
      await api(page, "DELETE", `/users/${created.body.id}`);
      await page.browserContext().close();
    }
  });
}
