import { describe, expect, it } from "vitest";
import { hostTargeting } from "./HostSchedules";
import type { Schedule } from "./queries";

const sched = (id: string, targets: Schedule["config"]["targets"]): Schedule => ({
  id, name: id, kind: "job", enabled: true, archived: false, owner: null, revision: 1, when: "", workflow_update_available: false,
  action: "", targets_text: "", created_at: 0, updated_at: 0, config: { recurrence: { kind: "daily" } as never, time_zone: "UTC", targets },
} as Schedule);

describe("schedules that reach a host", () => {
  const host = { id: "h1", group_ids: ["g-media"], tags: ["web", "debian"] };
  const all = [
    sched("direct", { hosts: ["h1"] }),
    sched("pack", { groups: ["g-media", "g-other"] }),
    sched("tag", { tags: ["web"] }),
    sched("excluded", { groups: ["g-media"], exclude_hosts: ["h1"] }),
    sched("other", { hosts: ["h2"], groups: ["g-other"], tags: ["db"] }),
  ];
  const got = hostTargeting(host, all, { "g-media": "media-servers" });

  it("lists direct, Pack and tag targeting, never unrelated schedules", () => {
    expect(got.map((r) => r.schedule.id)).toEqual(["direct", "pack", "tag", "excluded"]);
  });
  it("says how each one reaches the host", () => {
    expect(got[0]).toMatchObject({ direct: true, packs: [], tags: [], excluded: false });
    expect(got[1]).toMatchObject({ direct: false, packs: ["media-servers"], excluded: false });
    expect(got[2]).toMatchObject({ direct: false, tags: ["web"] });
    expect(got[3]).toMatchObject({ excluded: true, packs: ["media-servers"] });
  });
});
