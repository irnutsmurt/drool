import { useState } from "react";
import type { Schemas } from "@/api/client";
import { useGroups, useHosts } from "@/api/queries";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input } from "@/components/ui/Field";

export type TargetSpec = Schemas["TargetSpec"];

export const hasTargets = (t: TargetSpec) => (t.hosts?.length ?? 0) + (t.groups?.length ?? 0) + (t.tags?.length ?? 0) > 0;

/** Hosts, groups and tags. Groups and tags are selectors: a schedule
 * resolves them again at every run. */
export function TargetPicker({ v, set, groupsHint, noTags }: { v: TargetSpec; set: (v: TargetSpec) => void; groupsHint?: string; noTags?: boolean }) {
  const hosts = useHosts();
  const groups = useGroups();
  const [filter, setFilter] = useState("");
  const [tags, setTags] = useState((v.tags ?? []).join(" "));
  const hostIds = v.hosts ?? [];
  const groupIds = v.groups ?? [];
  const visible = (hosts.data?.items ?? []).filter(
    (h) => !h.jump_only && (!filter || `${h.name} ${h.address} ${h.tags.join(" ")}`.toLowerCase().includes(filter.toLowerCase())),
  );
  const toggle = (list: string[], id: string, on: boolean) => (on ? [...list, id] : list.filter((x) => x !== id));
  return (
    <div className="space-y-4">
      <Input placeholder="Filter hosts…" aria-label="Filter hosts" value={filter} onChange={(e) => setFilter(e.target.value)} />
      <div className="max-h-64 space-y-1.5 overflow-y-auto">
        {visible.map((h) => (
          <Checkbox key={h.id} checked={hostIds.includes(h.id)} onChange={(on) => set({ ...v, hosts: toggle(hostIds, h.id, on) })}
            label={<span className="inline-flex items-center gap-2">{h.name} <HostStatusBadge status={h.status} /></span>} />
        ))}
        {visible.length === 0 && <p className="text-sm text-muted">No hosts.</p>}
      </div>
      {(groups.data?.items.length ?? 0) > 0 && (
        <div className="space-y-1.5 border-t border-line pt-3">
          <div className="text-xs font-medium uppercase text-muted">Groups</div>
          {groupsHint && <p className="text-xs text-muted">{groupsHint}</p>}
          {groups.data!.items.map((g) => (
            <Checkbox key={g.id} checked={groupIds.includes(g.id)} onChange={(on) => set({ ...v, groups: toggle(groupIds, g.id, on) })}
              label={`${g.name} (${g.host_ids.length})`} />
          ))}
        </div>
      )}
      {!noTags && (
        <Field label="Tags" hint="Hosts with any of these tags.">
          <Input value={tags} placeholder="role:web" onChange={(e) => {
            setTags(e.target.value);
            set({ ...v, tags: e.target.value.split(/[,\s]+/).filter(Boolean) });
          }} />
        </Field>
      )}
    </div>
  );
}
