import { useState, type FormEvent } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, type Group, type Host } from "@/api/queries";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";

/** Adding hosts to a pack and taking them out, one change at a time: from the
 * pack's page or from a host's. */
export function usePackMembership() {
  const qc = useQueryClient();
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qk.groups });
    void qc.invalidateQueries({ queryKey: qk.hosts });
  };
  const add = useMutation({
    mutationFn: (v: { pack: string; hosts: string[] }) =>
      unwrap(api.POST("/groups/{id}/members", { params: { path: { id: v.pack } }, body: { host_ids: v.hosts } })),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (v: { pack: string; host: string }) =>
      unwrap(api.DELETE("/groups/{id}/members/{host_id}", { params: { path: { id: v.pack, host_id: v.host } } })),
    onSuccess: invalidate,
  });
  return { add, remove };
}

/** A new pack: a name and, if you like, a description. Hosts come later. */
export function CreatePackDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (g: Group) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/groups", { body: { name: name.trim(), description: description.trim() } })),
    onSuccess: (g) => { void qc.invalidateQueries({ queryKey: qk.groups }); onCreated(g); },
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }
  return (
    <Dialog title="Create a pack" onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        <Field label="Name" hint="Letters, digits, '.', '_' and '-', starting with a letter or digit; no spaces.">
          <Input value={name} placeholder="media-servers" required data-autofocus onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Description (optional)">
          <Input value={description} placeholder="Jellyfin, Plex and the NAS" onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <p className="text-sm text-muted">You add hosts on the pack's page, now or later. A pack can stay empty.</p>
        {create.error && <Alert>{errorMessage(create.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim() || create.isPending}>{create.isPending ? "Creating…" : "Create"}</Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Pick hosts that aren't in the pack yet and add them. */
export function AddHostsDialog({ pack, hosts, onClose }: { pack: Group; hosts: Host[]; onClose: () => void }) {
  const { add } = usePackMembership();
  const [picked, setPicked] = useState<string[]>([]);
  const [filter, setFilter] = useState("");
  const candidates = hosts.filter((h) => !pack.host_ids.includes(h.id)).sort((a, b) => a.name.localeCompare(b.name));
  const f = filter.trim().toLowerCase();
  const shown = candidates.filter((h) => !f || [h.name, h.address, ...h.tags].some((s) => s.toLowerCase().includes(f)));
  const toggle = (id: string, on: boolean) => setPicked((p) => (on ? [...p, id] : p.filter((x) => x !== id)));
  return (
    <Dialog title={`Add hosts to ${pack.name}`} onClose={onClose}>
      <div className="space-y-4">
        {hosts.length === 0 ? (
          <p className="text-sm">There are no hosts in DROOL yet. <Link className="text-accent" to="/hosts/new">Add a host</Link> first.</p>
        ) : candidates.length === 0 ? (
          <p className="text-sm">Every host is already in this pack.</p>
        ) : (
          <>
            {candidates.length > 8 && (
              <Field label="Find hosts"><Input value={filter} placeholder="name, address or tag" onChange={(e) => setFilter(e.target.value)} /></Field>
            )}
            <div className="max-h-80 space-y-2 overflow-y-auto">
              {shown.map((h) => (
                <Checkbox key={h.id} checked={picked.includes(h.id)} onChange={(on) => toggle(h.id, on)}
                  label={<>{h.name} <span className="font-mono text-xs text-muted">{h.address}</span></>} />
              ))}
              {shown.length === 0 && <p className="text-sm text-muted">No host matches.</p>}
            </div>
          </>
        )}
        {add.error && <Alert>{errorMessage(add.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={picked.length === 0 || add.isPending}
            onClick={() => add.mutate({ pack: pack.id, hosts: picked }, { onSuccess: onClose })}>
            {picked.length === 0 ? "Add hosts" : `Add ${picked.length} host${picked.length === 1 ? "" : "s"}`}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

/** A host's packs, from the host's page: tick to add, untick to remove. */
export function HostPacksDialog({ host, packs, onClose }: { host: Host; packs: Group[]; onClose: () => void }) {
  const { add, remove } = usePackMembership();
  const busy = add.isPending || remove.isPending;
  return (
    <Dialog title={`Packs of ${host.name}`} onClose={onClose}>
      <div className="space-y-4">
        {packs.length === 0 ? (
          <p className="text-sm">There are no packs yet. <Link className="text-accent" to="/groups">Create one</Link> on the Packs page.</p>
        ) : (
          <div className="max-h-80 space-y-2 overflow-y-auto">
            {[...packs].sort((a, b) => a.name.localeCompare(b.name)).map((g) => (
              <Checkbox key={g.id} checked={host.group_ids.includes(g.id)} disabled={busy}
                onChange={(on) => (on ? add.mutate({ pack: g.id, hosts: [host.id] }) : remove.mutate({ pack: g.id, host: host.id }))}
                label={<>{g.name}{g.description && <span className="text-xs text-muted"> — {g.description}</span>}</>} />
            ))}
          </div>
        )}
        <p className="text-xs text-muted">Changes are saved as you tick. A pack's settings apply to the host while it's a member.</p>
        {(add.error ?? remove.error) && <Alert>{errorMessage(add.error ?? remove.error)}</Alert>}
        <div className="flex justify-end"><Button onClick={onClose}>Done</Button></div>
      </div>
    </Dialog>
  );
}
