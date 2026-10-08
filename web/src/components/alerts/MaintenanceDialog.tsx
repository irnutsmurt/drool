import { useMemo, useState } from "react";
import { useGroups, useHosts } from "@/api/queries";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { useStartMaintenance, type MaintenanceInput } from "./queries";

const presets = [
  { key: "60", label: "1 hour" },
  { key: "120", label: "2 hours" },
  { key: "240", label: "4 hours" },
  { key: "1440", label: "24 hours" },
  { key: "until", label: "Until a time" },
  { key: "manual", label: "Until I end it" },
];

/** Start maintenance now. It only silences notifications: monitoring,
 * health issues and every safety mechanism keep working. */
export function MaintenanceDialog({ hostIds = [], groupIds = [], onClose }: { hostIds?: string[]; groupIds?: string[]; onClose: () => void }) {
  const hosts = useHosts();
  const groups = useGroups();
  const start = useStartMaintenance();
  const [selHosts, setSelHosts] = useState<Set<string>>(new Set(hostIds));
  const [selGroups, setSelGroups] = useState<Set<string>>(new Set(groupIds));
  const [filter, setFilter] = useState("");
  const [preset, setPreset] = useState("120");
  const [until, setUntil] = useState("");
  const [reason, setReason] = useState("");
  const fixed = hostIds.length + groupIds.length > 0;
  const [choose, setChoose] = useState(!fixed);
  const managed = (hosts.data?.items ?? []).filter((h) => !h.jump_only);
  const shown = useMemo(() => managed.filter((h) => h.name.toLowerCase().includes(filter.toLowerCase())).slice(0, 200), [managed, filter]);
  const toggle = (set: Set<string>, id: string, upd: (s: Set<string>) => void) => {
    const n = new Set(set);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    upd(n);
  };
  const names = [
    ...managed.filter((h) => selHosts.has(h.id)).map((h) => h.name),
    ...(groups.data?.items ?? []).filter((g) => selGroups.has(g.id)).map((g) => `group ${g.name}`),
  ];
  const submit = () => {
    const body: MaintenanceInput = { host_ids: [...selHosts], group_ids: [...selGroups], reason };
    if (preset === "manual") body.indefinite = true;
    else if (preset === "until") body.until = until ? new Date(until).getTime() : 0;
    else body.duration_minutes = Number(preset);
    start.mutate(body, { onSuccess: onClose });
  };
  return (
    <Dialog title="Start maintenance" onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <p className="text-sm text-muted">
          Notifications are paused for these hosts. Monitoring, health issues and alerts stay visible (marked
          “suppressed by maintenance”), and firewall rollback, agent recovery and host-key checks keep working.
        </p>
        {!choose ? (
          <div className="text-sm">
            <span className="font-medium">For: </span>{names.join(", ") || "…"}{" "}
            <button type="button" className="text-accent underline" onClick={() => setChoose(true)}>Change</button>
          </div>
        ) : (
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Hosts and groups</legend>
            {(groups.data?.items.length ?? 0) > 0 && (
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {groups.data!.items.map((g) => (
                  <Checkbox key={g.id} checked={selGroups.has(g.id)} onChange={() => toggle(selGroups, g.id, setSelGroups)} label={<>Group <b>{g.name}</b></>} />
                ))}
              </div>
            )}
            <Input aria-label="Filter hosts" placeholder="Filter hosts…" value={filter} onChange={(e) => setFilter(e.target.value)} />
            <div className="max-h-40 space-y-1 overflow-y-auto rounded-ui border border-line p-2">
              {shown.map((h) => (
                <Checkbox key={h.id} checked={selHosts.has(h.id)} onChange={() => toggle(selHosts, h.id, setSelHosts)} label={h.name} />
              ))}
              {shown.length === 0 && <p className="text-xs text-muted">No hosts match.</p>}
            </div>
          </fieldset>
        )}
        <fieldset>
          <legend className="mb-1.5 text-sm font-medium">How long</legend>
          <div role="radiogroup" className="flex flex-wrap gap-2">
            {presets.map((p) => (
              <label key={p.key} className={`cursor-pointer rounded-ui border px-3 py-1.5 text-sm has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent ${preset === p.key ? "border-accent bg-accent/15 font-medium" : "border-line"}`}>
                <input type="radio" name="mduration" className="sr-only" checked={preset === p.key} onChange={() => setPreset(p.key)} />
                {p.label}
              </label>
            ))}
          </div>
          {preset === "until" && (
            <div className="mt-2"><Field label="End"><Input type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value)} required /></Field></div>
          )}
        </fieldset>
        <Field label="Reason (recommended)" hint="Shown on suppressed alerts, e.g. “Kernel upgrade” or “Replacing storage”.">
          <Input value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} data-autofocus={fixed ? true : undefined} />
        </Field>
        {start.error && <Alert>{errorMessage(start.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={start.isPending || selHosts.size + selGroups.size === 0}>
            {start.isPending ? "Starting…" : "Start maintenance"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
