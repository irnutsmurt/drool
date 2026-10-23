import { describe, expect, it } from "vitest";
import type { Host } from "@/api/queries";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { RebootCell, UpdatesCell, connectionReasons } from "./health";

const now = new Date().toISOString();
const host = (status: string, health: object | null): Host =>
  ({ id: "h1", name: "web-01", status, health: health && { checked_at: now, stale: false, reachable: true, max_fs_used_pct: null, max_fs_mount: "", ...health } }) as never;

describe("a host's connection reasons", () => {
  it("are the server's verdicts on reaching the host, most serious first", () => {
    expect(connectionReasons(host("hostkey_mismatch", { reachable: false }))).toEqual(["host key changed", "offline"]);
    expect(connectionReasons(host("auth_failed", null))).toEqual(["access denied"]);
    expect(connectionReasons(host("pending_hostkey", null))).toEqual(["needs host key approval"]);
    expect(connectionReasons(host("ready", { stale: true, reachable: false }))[0]).toMatch(/^no data since /);
  });
  it("never judge health themselves: a full disk is monitoring's verdict, with its thresholds", () => {
    // 96% used: Critical by the Recommended profile, or fine where someone
    // set 98%. Either way the alert says it, not a number in the UI.
    expect(connectionReasons(host("ready", { max_fs_used_pct: 96, max_fs_mount: "/" }))).toEqual([]);
  });
  it("leave updates and reboots to their own line", () => {
    expect(connectionReasons(host("ready", { security_updates: 3, reboot_required: true }))).toEqual([]);
  });
});

describe("updates and reboots in a host list", () => {
  const cell = (C: typeof UpdatesCell, health: object | null) =>
    renderToStaticMarkup(createElement(C, { host: host("ready", health) })).replace(/<[^>]+>/g, "");
  it("say not checked or unknown, never a bare zero or a blank", () => {
    expect(cell(UpdatesCell, null)).toBe("not checked yet");
    expect(cell(UpdatesCell, { updates_available: null })).toBe("unknown");
    expect(cell(RebootCell, null)).toBe("unknown");
    expect(cell(RebootCell, { reboot_required: false })).toBe("no");
  });
  it("from a host that reports now, as they are", () => {
    expect(cell(UpdatesCell, { updates_available: 4, security_updates: 1, updates_checked_at: now })).toBe("4 (1 security)");
    expect(cell(RebootCell, { reboot_required: true })).toBe("required");
  });
  it("from an offline or stale host, marked last known", () => {
    expect(cell(UpdatesCell, { reachable: false, updates_available: 0, updates_checked_at: now })).toBe("0 · last known");
    expect(cell(UpdatesCell, { stale: true, updates_available: 3, security_updates: 0, updates_checked_at: now })).toBe("3 · last known");
    expect(cell(RebootCell, { stale: true, reboot_required: true })).toBe("required · last known");
  });
});
