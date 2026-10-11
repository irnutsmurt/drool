// M16 security in the browser: two-factor setup from the Account page (a QR
// code that really decodes to the authenticator URI, the key to type in,
// confirmation, recovery codes), signing in with a code and with a recovery
// code, the re-authentication prompt that a high-impact action raises and
// that repeats the action once confirmed, and the audit log's integrity
// card. The test controller lets re-authentication lapse after 3 s
// (DROOL_TEST_AUTH_TIMING=fast).
import { test, after, before } from "node:test";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { api, base, click, fill, launch, session, shot, sleep, text, waitFor, waitText } from "./lib.mjs";

let browser;
let admin;
const run = Date.now().toString(36).slice(-5);
const u = { user: `e2e-2fa-${run}`, password: "two-factor-test-password" };
let userId;
let secret;
let lastStep = 0;
let recovery = [];

function b32decode(s) {
  const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0, val = 0;
  const out = [];
  for (const c of s.replace(/\s+/g, "").toUpperCase()) {
    val = (val << 5) | alpha.indexOf(c);
    bits += 5;
    if (bits >= 8) {
      out.push((val >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

function totpAt(step) {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
  const h = crypto.createHmac("sha1", b32decode(secret)).update(msg).digest();
  const off = h[h.length - 1] & 0x0f;
  return String((h.readUInt32BE(off) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}

/** A code from a time step that hasn't been used yet (each works once):
 * waits for the next 30 s step when needed. */
async function freshCode() {
  for (;;) {
    const step = Math.floor(Date.now() / 30000);
    if (step > lastStep) {
      lastStep = step;
      return totpAt(step);
    }
    await sleep(1000);
  }
}

before(async () => {
  browser = await launch();
  admin = await session(browser);
  const r = await api(admin, "POST", "/users", { username: u.user, password: u.password, role: "operator" });
  assert.equal(r.status, 201, JSON.stringify(r.body));
  userId = r.body.id;
});
after(async () => {
  if (admin && userId) await api(admin, "DELETE", `/users/${userId}`).catch(() => {});
  await browser?.close();
});

/** Signs in on the login page; with two-factor, finishes with `second`. */
async function signIn(second) {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  await page.setViewport({ width: 1280, height: 900 });
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(e.message));
  await page.goto(base + "/", { waitUntil: "networkidle0" });
  await fill(page, "Username", u.user);
  await fill(page, "Password", u.password);
  await page.keyboard.press("Enter");
  if (second) {
    await waitText(page, "6-digit code from your authenticator");
    if (second.recovery) {
      await click(page, "button", "Use a recovery code");
      await fill(page, "Recovery code", second.recovery);
    } else {
      await fill(page, "Authenticator code", await freshCode());
    }
    await page.keyboard.press("Enter");
  }
  await waitFor(page, async () => !(await text(page)).includes("Sign in to DROOL"), "signed in");
  return page;
}

/** Answers the re-authentication prompt if it is open. */
async function confirmIfAsked(page, withCode) {
  if (!(await page.$('[data-testid="reauth-dialog"]'))) return false;
  await fill(page, "Password", u.password);
  if (withCode) await fill(page, "Authenticator code", await freshCode());
  await click(page, '[data-testid="reauth-dialog"] button', "Confirm");
  return true;
}

test("two-factor: set up with the QR code, then it's needed to sign in", async () => {
  const page = await signIn();
  await page.goto(base + "/settings/account", { waitUntil: "networkidle0" });
  await waitText(page, "Signing in needs only your password");
  await click(page, "button", "Set up two-factor authentication");
  await sleep(500);
  await confirmIfAsked(page, false);
  await page.waitForSelector('[data-testid="mfa-qr"]');
  secret = (await page.$eval('[data-testid="mfa-secret"]', (e) => e.textContent)).replace(/\s+/g, "");
  assert.match(secret, /^[A-Z2-7]{32}$/);
  // The QR code decodes to the authenticator URI with this secret.
  const qr = await page.$('[data-testid="mfa-qr"]');
  const png = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "e2e-qr-")), "qr.png");
  await qr.screenshot({ path: png });
  if (fs.existsSync("/usr/bin/zbarimg")) {
    const decoded = execFileSync("/usr/bin/zbarimg", ["--quiet", "--raw", png]).toString().trim();
    assert.match(decoded, new RegExp(`^otpauth://totp/DROOL(:|%3A)${u.user}\\?.*secret=${secret}`));
  }
  await shot(page, "2fa-setup");
  await fill(page, "Enter the 6-digit code the app shows", "000000");
  await click(page, "button", "Turn on");
  await waitText(page, "not valid");
  await fill(page, "Enter the 6-digit code the app shows", await freshCode());
  await click(page, "button", "Turn on");
  await page.waitForSelector('[data-testid="recovery-codes"]');
  recovery = await page.$$eval('[data-testid="recovery-codes"] li', (ls) => ls.map((l) => l.textContent.trim()));
  assert.equal(recovery.length, 10);
  assert.ok(recovery.every((c) => /^[a-z2-9]{4}(-[a-z2-9]{4}){3}$/.test(c)), recovery.join(" "));
  await shot(page, "2fa-recovery-codes");
  await click(page, "button", "I've saved them");
  await waitText(page, "10 unused recovery codes left");
  assert.deepEqual(page.errors, []);

  // Password alone isn't enough any more; a code from the authenticator is.
  const p2 = await signIn({});
  assert.deepEqual(p2.errors, []);
  // A recovery code works once.
  const p3 = await signIn({ recovery: recovery[0].toUpperCase() });
  await p3.goto(base + "/settings/account", { waitUntil: "networkidle0" });
  await waitText(p3, "9 unused recovery codes left");
});

test("a high-impact action asks to confirm, then goes through", async () => {
  const page = await signIn({});
  await page.goto(base + "/settings/tokens", { waitUntil: "networkidle0" });
  await sleep(3500); // past the test controller's re-authentication window
  await fill(page, "Name", `e2e-token-${run}`);
  await page.keyboard.press("Enter");
  await page.waitForSelector('[data-testid="reauth-dialog"]');
  await waitText(page, "and a code from your authenticator");
  await shot(page, "reauth-prompt");
  // Cancelling leaves things as they were.
  await click(page, '[data-testid="reauth-dialog"] button', "Cancel");
  await sleep(500);
  assert.equal(await page.$('[data-testid="reauth-dialog"]'), null);
  assert.ok(!(await text(page)).includes(`e2e-token-${run}`), "the token was created without confirming");
  // Confirming repeats the request: the token is created.
  await page.keyboard.press("Enter");
  await page.waitForSelector('[data-testid="reauth-dialog"]');
  await confirmIfAsked(page, true);
  await waitText(page, `e2e-token-${run}`);
  const tokens = await api(page, "GET", "/tokens");
  assert.ok(tokens.body.items.some((t) => t.name === `e2e-token-${run}`));
  assert.deepEqual(page.errors, []);
});

test("an administrator resets a lost authenticator", async () => {
  const r = await api(admin, "POST", `/users/${userId}/mfa/reset`);
  assert.equal(r.status, 204, JSON.stringify(r.body));
  const page = await signIn(); // password only again
  await page.goto(base + "/settings/account", { waitUntil: "networkidle0" });
  await waitText(page, "Signing in needs only your password");
});

test("the audit log shows that its history checks out", async () => {
  await admin.goto(base + "/audit", { waitUntil: "networkidle0" });
  await waitFor(admin, async () => /Intact|Not checked yet/.test(await text(admin)), "integrity state");
  await click(admin, "button", "Check now");
  await waitText(admin, "Intact");
  await admin.waitForSelector('[data-testid="audit-sink"]');
  const t = await text(admin);
  assert.ok(t.includes("Records are chained by hashes"), "page description");
  assert.ok(!/immutable/i.test(t), "the audit log is never called immutable");
  await shot(admin, "audit-integrity");
});

test("Secure your installation: problems, recommendations, what DROOL can't check", async () => {
  await admin.goto(base + "/settings/system#secure", { waitUntil: "networkidle0" });
  await admin.waitForSelector('[data-testid="security-checklist"]');
  const level = async (id) => admin.$eval(`[data-check="${id}"]`, (e) => e.getAttribute("data-level")).catch(() => null);
  // The test controller runs with DROOL_TEST_* switches: never for
  // production, so a problem.
  assert.equal(await level("test-mode"), "problem");
  // The administrator has no second factor; the Agent Root CA key is on
  // the controller: recommendations, not blockers.
  assert.equal(await level("mfa"), "recommended");
  assert.equal(await level("root-key"), "recommended");
  assert.equal(await level("https"), "done");
  assert.equal(await level("tls"), "optional");
  // What it can't see, it says so.
  assert.equal(await level("master-key"), "manual");
  assert.equal(await level("backups"), "manual");
  const t = await text(admin);
  assert.ok(t.includes("DROOL works without any of these"), "it doesn't present hardening as a prerequisite");
  assert.ok(/can't see whether you keep a copy/.test(t), "honest about the master key copy");
  // Problems come first.
  const order = await admin.$$eval("[data-check]", (els) => els.map((e) => e.getAttribute("data-level")));
  assert.equal(order[0], "problem", order.join(","));
  await shot(admin, "secure-your-installation");
  // Navigation search finds it by other words.
  await admin.goto(base + "/", { waitUntil: "networkidle0" });
  await admin.keyboard.down("Control");
  await admin.keyboard.press("k");
  await admin.keyboard.up("Control");
  await admin.keyboard.type("hardening");
  await admin.keyboard.press("Enter");
  await waitFor(admin, async () => admin.url().endsWith("/settings/system#secure"), "search → Secure your installation");
});
