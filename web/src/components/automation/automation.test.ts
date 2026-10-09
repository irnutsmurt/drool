import { describe, expect, it } from "vitest";
import { draftFromOp, draftProblem, draftToOp, newDraft } from "./StepEditor";
import { isCautious, mutates, recommendedRollout, rolloutFrom, rolloutText, strategyFrom } from "./Rollout";
import { recurFrom, recurrenceFrom } from "./RecurrenceFields";
import type { Operation } from "./queries";

describe("step drafts", () => {
  it("round-trips every editable step", () => {
    const ops: Operation[] = [
      { type: "command", command: "df -h", become: true, read_only: true, timeout_s: 30 },
      { type: "script", body: "echo hi", interpreter: "bash", become: false, read_only: false },
      { type: "package", action: "install", names: ["nginx", "curl"], allow_removals: false },
      { type: "system_update", mode: "full_upgrade", refresh: true, reboot: "if_required", allow_removals: true, timeout_s: 3600 },
      { type: "service", name: "nginx", state: "restarted" },
      { type: "reboot", wait: true },
      { type: "wait", seconds: 90 },
      { type: "verify_host", timeout_s: 300 },
      { type: "verify_services", services: ["jellyfin.service", "ssh.service"], timeout_s: 180 },
      { type: "verify_health", policy: "no_warning", timeout_s: 120 },
    ];
    for (const op of ops) {
      const back = draftToOp(draftFromOp(op)) as Record<string, unknown>;
      for (const [k, v] of Object.entries(op)) expect([op.type, k, back[k]]).toEqual([op.type, k, v]);
    }
  });

  it("treats a zero timeout as the default (templates send 0)", () => {
    const d = draftFromOp({ type: "system_update", mode: "upgrade", reboot: "if_required", timeout_s: 0 });
    expect(d.timeout).toBe("");
    expect((draftToOp(d) as { timeout_s?: number }).timeout_s).toBeUndefined();
  });

  it("keeps what it can't edit exactly as saved", () => {
    const op: Operation = { type: "script", body: { debian: "apt-get moo", redhat: "dnf --version" } };
    const d = draftFromOp(op);
    expect(d.raw).toBe(op);
    expect(draftToOp(d)).toBe(op);
    expect(draftProblem(d)).toBe("");
  });

  it("says what's missing", () => {
    expect(draftProblem(newDraft("verify_services"))).toBe("name at least one service");
    expect(draftProblem({ ...newDraft("wait"), seconds: "0" })).toBe("wait 1–3600 seconds");
    expect(draftProblem(draftFromOp({ type: "verify_services", services: null as unknown as string[] }))).toBe("name at least one service");
  });
});

describe("rollouts", () => {
  it("recommends a careful rollout only for changes", () => {
    expect(mutates([{ type: "verify_host" }, { type: "command", command: "uptime", read_only: true }])).toBe(false);
    expect(mutates([{ type: "system_update" }])).toBe(true);
    expect(isCautious({ concurrency: 10 })).toBe(false);
    expect(isCautious(strategyFrom(recommendedRollout))).toBe(true);
  });

  it("describes and round-trips strategies", () => {
    const s = strategyFrom(recommendedRollout);
    expect(s).toEqual({ concurrency: 5, failure_threshold: 2, canary_count: 1, canary_hosts: undefined, batch_size: 5 });
    expect(rolloutText(s)).toBe("1 canary host first, then batches of 5, up to 5 at a time, pause after 2 failures");
    expect(rolloutText({ concurrency: 10 })).toBe("all hosts in one wave, up to 10 at a time, no failure limit");
    expect(strategyFrom(rolloutFrom({ concurrency: 3, canary_hosts: ["h1"], batch_size: 2 }))).toEqual(
      { concurrency: 3, failure_threshold: null, canary_count: undefined, canary_hosts: ["h1"], batch_size: 2 });
  });
});

describe("recurrence", () => {
  it("offers weekly as one day or selected weekdays", () => {
    expect(recurFrom({ kind: "weekly", time: "03:00", weekdays: ["sun"] }, "UTC").mode).toBe("weekly");
    const s = recurFrom({ kind: "weekly", time: "03:00", weekdays: ["mon", "fri"] }, "UTC");
    expect(s.mode).toBe("weekdays");
    expect(recurrenceFrom(s)).toEqual({ kind: "weekly", time: "03:00", weekdays: ["mon", "fri"] });
    expect(recurrenceFrom(recurFrom({ kind: "monthly", time: "04:30", day_of_month: 31 }, "UTC"))).toEqual({ kind: "monthly", time: "04:30", day_of_month: 31 });
  });
});
