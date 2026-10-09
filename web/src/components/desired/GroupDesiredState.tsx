import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { plainLink } from "@/components/ui/links";
import { useDebounced } from "@/hooks/useDebounced";
import { errorMessage } from "@/lib/format";
import { useDesiredGroups, useInvalidateDesired } from "./queries";

/** A group's desired state: the baselines assigned to it and its priority
 * when a host's groups disagree. */
export function GroupDesiredState({ groupId }: { groupId: string }) {
  const { can } = useAuth();
  const groups = useDesiredGroups();
  const g = groups.data?.items.find((x) => x.group_id === groupId);
  const [edit, setEdit] = useState(false);
  if (!g) return null;
  return (
    <div className="mt-3 border-t border-line pt-2 text-sm">
      <span className="text-muted">Desired state: </span>
      {g.baselines.length ? g.baselines.join(", ") : <span className="text-muted">no baseline assigned</span>}
      <span className="text-muted"> · priority {g.priority}</span>
      {can("desired.manage") && <button type="button" className={`ml-2 ${plainLink}`} onClick={() => setEdit(true)}>Change priority</button>}
      {edit && <PriorityDialog groupId={groupId} name={g.name} current={g.priority} onClose={() => setEdit(false)} />}
    </div>
  );
}

function PriorityDialog({ groupId, name, current, onClose }: { groupId: string; name: string; current: number; onClose: () => void }) {
  const invalidate = useInvalidateDesired();
  const [value, setValue] = useState(String(current));
  const n = Number(value);
  const ok = value.trim() !== "" && Number.isInteger(n) && n >= -1000 && n <= 1000;
  const debounced = useDebounced(n, 300);
  const impact = useQuery({
    queryKey: ["desired", "priority-preview", groupId, debounced],
    queryFn: () => unwrap(api.POST("/desired/groups/{id}/priority/preview", { params: { path: { id: groupId } }, body: { priority: debounced } })),
    enabled: ok,
    retry: false,
  });
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/desired/groups/{id}/priority", { params: { path: { id: groupId } }, body: { priority: n } })),
    onSuccess: () => { invalidate(); onClose(); },
  });
  const i = impact.data;
  return (
    <Dialog title={`Desired-state priority: ${name}`} onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); save.mutate(); }}>
        <p className="text-sm text-muted">
          When a host is in several groups whose baselines declare the same resource differently, the group with the higher priority
          wins. Equal priorities that disagree are a conflict: DROOL shows it and doesn't choose. A host's own setting always wins.
        </p>
        <Field label="Priority" hint="-1000 to 1000. Default 0.">
          <Input type="number" min={-1000} max={1000} value={value} onChange={(e) => setValue(e.target.value)} data-autofocus />
        </Field>
        {i && debounced === n && (
          <div className="text-sm" aria-label="Impact">
            {!i.changed ? <p className="text-muted">No change.</p> : i.hosts_affected === 0 ? <p className="text-muted">No host's desired state changes.</p> : (
              <ul className="list-disc space-y-0.5 pl-5">
                <li>{i.hosts_affected} host{i.hosts_affected === 1 ? "" : "s"} affected</li>
                {i.counts.resolved_conflicts > 0 && <li>{i.counts.resolved_conflicts} conflict{i.counts.resolved_conflicts === 1 ? "" : "s"} resolved</li>}
                {i.counts.new_conflicts > 0 && <li className="text-danger">{i.counts.new_conflicts} new conflict{i.counts.new_conflicts === 1 ? "" : "s"}</li>}
                {i.counts.become_drifted > 0 && <li className="text-warn">{i.counts.become_drifted} host{i.counts.become_drifted === 1 ? "" : "s"} would drift</li>}
                {i.counts.become_compliant > 0 && <li>{i.counts.become_compliant} drifted host{i.counts.become_compliant === 1 ? "" : "s"} would comply</li>}
              </ul>
            )}
          </div>
        )}
        {impact.error && <Alert>{errorMessage(impact.error)}</Alert>}
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex items-center justify-end gap-2">
          <Link className={`mr-auto text-sm ${plainLink}`} to="/desired">Desired state</Link>
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!ok || save.isPending || debounced !== n || impact.isFetching}>Save</Button>
        </div>
      </form>
    </Dialog>
  );
}
