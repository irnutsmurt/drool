import { useState } from "react";
import { Card } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Badge } from "@/components/ui/Badge";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input, Select } from "@/components/ui/Field";
import { Alert } from "@/components/ui/Alert";
import {
  deleteNetwork, deleteRule, deleteService, entriesLabel, moveRule, refLabel, kindLabel, parseRef, rulesUsing, rulesUsingService,
  serviceLabel, upsertGroup, upsertNetwork, upsertService, validPorts,
  type Doc, type Entry, type Group, type Network, type Service, type ServiceInfo,
} from "./model";
import { RuleDialog } from "./RuleDialog";

type Props = {
  doc: Doc; edit: (d: Doc) => void; services: ServiceInfo[]; hosts: { id: string; name: string }[];
  storedGroups: Set<string>; canWrite: boolean; highlight?: string[]; onExplainRule?: (index: number) => void;
  /** Rules just added from an observed connection (M10): marked, not alarmed. */
  fresh?: string[];
};

/** The conventional editor: everything the topology can do, with normal,
 * keyboard-accessible controls. */
export function RulesView(p: Props) {
  return (
    <div className="space-y-4">
      <PoliciesCard {...p} />
      <div className="grid gap-4 xl:grid-cols-3">
        <NetworksCard {...p} />
        <GroupsCard {...p} />
        <ServicesCard {...p} />
      </div>
    </div>
  );
}

function RefList({ refs }: { refs: string[] }) {
  return (
    <span className="flex flex-wrap gap-1">
      {refs.map((r) => (
        <span key={r} className="rounded bg-surface-2 px-1.5 py-0.5 text-xs" title={kindLabel[parseRef(r).kind]}>{refLabel(r)}</span>
      ))}
    </span>
  );
}

function PoliciesCard({ doc, edit, services, hosts, canWrite, highlight = [], fresh = [] }: Props) {
  const [dialog, setDialog] = useState<{ index?: number } | null>(null);
  const all = [...services.filter((s) => s.builtin), ...doc.services.map((s) => ({ ...s, builtin: false }))];
  return (
    <Card title="Rules" actions={canWrite && <Button size="sm" variant="primary" onClick={() => setDialog({})}>New rule</Button>}>
      <p className="mb-3 text-sm text-muted">
        Each rule lets a <strong>source</strong> reach a <strong>destination</strong> on <strong>services</strong> (allow), or stops it (deny).
        Denies always win over allows; DROOL&apos;s own management path is always kept open ahead of every rule.
      </p>
      {doc.policy.length === 0 ? (
        <p className="text-sm text-muted">No rules yet. Hosts that DROOL enforces accept only their management path and replies until you add some.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <caption className="sr-only">Firewall rules in evaluation order</caption>
            <thead>
              <tr className="border-b border-line text-left text-xs uppercase text-muted">
                <th className="py-2 pr-2">#</th><th className="pr-2">Rule</th><th className="pr-2">Action</th><th className="pr-2">Source</th>
                <th className="pr-2">Destination</th><th className="pr-2">Service</th><th className="pr-2"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {doc.policy.map((r, i) => (
                <tr key={r.name + i} className={`border-b border-line align-top ${highlight.includes(r.name) ? "bg-danger-bg" : fresh.includes(r.name) ? "bg-accent/10" : ""} ${r.enabled ? "" : "opacity-60"}`}>
                  <td className="py-2 pr-2 text-muted">{i + 1}</td>
                  <td className="py-2 pr-2">
                    <div className="font-medium">{r.name}</div>
                    {r.description && <div className="text-xs text-muted">{r.description}</div>}
                    {!r.enabled && <Badge>disabled</Badge>}
                    {highlight.includes(r.name) && <Badge tone="danger">blocks the management path</Badge>}
                    {fresh.includes(r.name) && <Badge tone="accent">new · from an observed connection</Badge>}
                  </td>
                  <td className="py-2 pr-2"><Badge tone={r.action === "deny" ? "danger" : "ok"}>{r.action === "deny" ? "Deny" : "Allow"}</Badge></td>
                  <td className="py-2 pr-2"><RefList refs={r.from} /></td>
                  <td className="py-2 pr-2"><RefList refs={r.to} /></td>
                  <td className="py-2 pr-2 text-xs">{r.services.map((s) => <div key={s}>{serviceLabel(s, all)}</div>)}</td>
                  <td className="whitespace-nowrap py-2">
                    {canWrite && (
                      <>
                        <Button size="sm" variant="ghost" aria-label={`Move ${r.name} up`} disabled={i === 0} onClick={() => edit(moveRule(doc, i, -1))}>↑</Button>
                        <Button size="sm" variant="ghost" aria-label={`Move ${r.name} down`} disabled={i === doc.policy.length - 1} onClick={() => edit(moveRule(doc, i, 1))}>↓</Button>
                        <Button size="sm" variant="ghost" onClick={() => setDialog({ index: i })}>Edit<span className="sr-only"> {r.name}</span></Button>
                        <Button size="sm" variant="ghost" onClick={() => edit(deleteRule(doc, i))}>Delete<span className="sr-only"> {r.name}</span></Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {dialog && (
        <RuleDialog doc={doc} services={services} hosts={hosts} index={dialog.index}
          onSave={(d) => { edit(d); setDialog(null); }}
          onDelete={dialog.index !== undefined ? () => { edit(deleteRule(doc, dialog.index!)); setDialog(null); } : undefined}
          onClose={() => setDialog(null)} />
      )}
    </Card>
  );
}

function usedBy(doc: Doc, idx: number[]) {
  return idx.map((i) => doc.policy[i]!.name).join(", ");
}

function NetworksCard({ doc, edit, canWrite }: Props) {
  const [open, setOpen] = useState<{ n?: Network } | null>(null);
  const [err, setErr] = useState("");
  return (
    <Card title="Networks" actions={canWrite && <Button size="sm" onClick={() => setOpen({})}>New network</Button>}>
      <p className="mb-2 text-xs text-muted">Named address ranges (IPv4 and IPv6) to use as sources or destinations.</p>
      {err && <Alert>{err}</Alert>}
      <ul className="divide-y divide-line text-sm">
        {doc.networks.map((n) => (
          <li key={n.name} className="flex items-start justify-between gap-2 py-2">
            <div><div className="font-medium">{n.name}</div><div className="font-mono text-xs text-muted">{n.cidrs.join(", ")}</div></div>
            {canWrite && (
              <div className="flex shrink-0 gap-1">
                <Button size="sm" variant="ghost" onClick={() => setOpen({ n })}>Edit<span className="sr-only"> {n.name}</span></Button>
                <Button size="sm" variant="ghost" onClick={() => {
                  const used = rulesUsing(doc, `network:${n.name}`);
                  if (used.length) setErr(`Network ${n.name} is used by: ${usedBy(doc, used)}. Change those rules first.`);
                  else { setErr(""); edit(deleteNetwork(doc, n.name)); }
                }}>Delete<span className="sr-only"> {n.name}</span></Button>
              </div>
            )}
          </li>
        ))}
        {doc.networks.length === 0 && <li className="py-2 text-xs text-muted">None.</li>}
      </ul>
      {open && <NetworkDialog doc={doc} network={open.n} onClose={() => setOpen(null)} onSave={(n) => { edit(upsertNetwork(doc, n, open.n?.name)); setOpen(null); }} />}
    </Card>
  );
}

function NetworkDialog({ doc, network, onSave, onClose }: { doc: Doc; network?: Network; onSave: (n: Network) => void; onClose: () => void }) {
  const [name, setName] = useState(network?.name ?? "");
  const [cidrs, setCidrs] = useState((network?.cidrs ?? []).join("\n"));
  const [description, setDescription] = useState(network?.description ?? "");
  const [err, setErr] = useState("");
  const save = () => {
    const list = cidrs.split(/[\s,]+/).filter(Boolean);
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/.test(name)) return setErr("Use letters, digits, '.', '_' or '-' for the name.");
    if (["internet", "internal", "any"].includes(name.toLowerCase())) return setErr(`“${name}” is reserved.`);
    if (doc.networks.some((n) => n.name.toLowerCase() === name.toLowerCase() && n.name !== network?.name)) return setErr("A network with this name exists.");
    if (list.length === 0) return setErr("Add at least one CIDR.");
    const bad = list.find((c) => !/^[0-9a-fA-F:.]+\/\d{1,3}$/.test(c));
    if (bad) return setErr(`“${bad}” is not a CIDR such as 192.168.1.0/24 or 2001:db8::/32.`);
    onSave({ name, description, cidrs: list });
  };
  return (
    <Dialog title={network ? `Edit network ${network.name}` : "New network"} onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name"><Input data-autofocus value={name} onChange={(e) => setName(e.target.value)} placeholder="admin-lan" /></Field>
        <Field label="CIDRs" hint="One per line; IPv4 and IPv6 can be mixed. Changing them changes every rule that uses the network (a policy change, applied by transaction).">
          <textarea className="h-24 w-full rounded-ui border border-line bg-surface p-2 font-mono text-sm" value={cidrs} onChange={(e) => setCidrs(e.target.value)} placeholder={"192.168.1.0/24\n2001:db8:10::/48"} />
        </Field>
        <Field label="Description (optional)"><Input value={description} onChange={(e) => setDescription(e.target.value)} /></Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2"><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={save}>Save network</Button></div>
      </div>
    </Dialog>
  );
}

function GroupsCard({ doc, edit, hosts, canWrite, storedGroups }: Props) {
  const [open, setOpen] = useState<{ g?: Group } | null>(null);
  const [err, setErr] = useState("");
  return (
    <Card title="Packs (host groups)" actions={canWrite && <Button size="sm" onClick={() => setOpen({})}>New pack</Button>}>
      <p className="mb-2 text-xs text-muted">DROOL&apos;s packs, shared with runs and the rest of DROOL (written <span className="font-mono">group:NAME</span> in YAML). A rule for a pack is one rule for all its members.</p>
      {err && <Alert>{err}</Alert>}
      <ul className="divide-y divide-line text-sm">
        {doc.groups.map((g) => (
          <li key={g.name} className="flex items-start justify-between gap-2 py-2">
            <div><div className="font-medium">{g.name}</div><div className="text-xs text-muted">{g.members.length ? g.members.join(", ") : "no members"}</div></div>
            {canWrite && (
              <div className="flex shrink-0 gap-1">
                <Button size="sm" variant="ghost" onClick={() => setOpen({ g })}>Members<span className="sr-only"> of {g.name}</span></Button>
                {!storedGroups.has(g.name.toLowerCase()) && (
                  <Button size="sm" variant="ghost" onClick={() => {
                    const used = rulesUsing(doc, `group:${g.name}`);
                    if (used.length) setErr(`Pack ${g.name} is used by: ${usedBy(doc, used)}. Change those rules first.`);
                    else { setErr(""); edit({ ...doc, groups: doc.groups.filter((x) => x.name !== g.name) }); }
                  }}>Remove<span className="sr-only"> {g.name}</span></Button>
                )}
              </div>
            )}
          </li>
        ))}
        {doc.groups.length === 0 && <li className="py-2 text-xs text-muted">None.</li>}
      </ul>
      <p className="mt-2 text-xs text-muted">Existing packs are deleted on the Packs page, not here.</p>
      {open && <GroupDialog doc={doc} group={open.g} hosts={hosts} onClose={() => setOpen(null)} onSave={(g) => { edit(upsertGroup(doc, g, open.g?.name)); setOpen(null); }} />}
    </Card>
  );
}

function GroupDialog({ doc, group, hosts, onSave, onClose }: { doc: Doc; group?: Group; hosts: { id: string; name: string }[]; onSave: (g: Group) => void; onClose: () => void }) {
  const [name, setName] = useState(group?.name ?? "");
  const [members, setMembers] = useState<string[]>(group?.members ?? []);
  const [description, setDescription] = useState(group?.description ?? "");
  const [err, setErr] = useState("");
  const [q, setQ] = useState("");
  const save = () => {
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/.test(name)) return setErr("Use letters, digits, '.', '_' or '-' for the name.");
    if (!group && doc.groups.some((g) => g.name.toLowerCase() === name.toLowerCase())) return setErr("A pack with this name exists.");
    onSave({ name, description, members });
  };
  const shown = hosts.filter((h) => h.name.toLowerCase().includes(q.toLowerCase()));
  return (
    <Dialog title={group ? `Members of ${group.name}` : "New pack"} onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name"><Input data-autofocus value={name} onChange={(e) => setName(e.target.value)} disabled={!!group} /></Field>
        <Field label="Description (optional)"><Input value={description} onChange={(e) => setDescription(e.target.value)} /></Field>
        <fieldset className="space-y-1">
          <legend className="text-sm font-medium">Members ({members.length})</legend>
          <Input aria-label="Filter hosts" placeholder="Filter hosts…" value={q} onChange={(e) => setQ(e.target.value)} />
          <div className="max-h-56 overflow-y-auto rounded-ui border border-line p-2">
            {shown.map((h) => (
              <label key={h.id} className="flex items-center gap-2 py-0.5 text-sm">
                <input type="checkbox" checked={members.includes(h.name)} onChange={(e) => setMembers(e.target.checked ? [...members, h.name] : members.filter((m) => m !== h.name))} />
                {h.name}
              </label>
            ))}
          </div>
        </fieldset>
        <p className="text-xs text-muted">Membership changes the pack everywhere in DROOL when you save. Rules that use the pack change on its members' firewalls.</p>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2"><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={save}>Save pack</Button></div>
      </div>
    </Dialog>
  );
}

function ServicesCard({ doc, edit, services, canWrite }: Props) {
  const [open, setOpen] = useState<{ s?: Service } | null>(null);
  const [err, setErr] = useState("");
  const [showBuiltin, setShowBuiltin] = useState(false);
  return (
    <Card title="Services" actions={canWrite && <Button size="sm" onClick={() => setOpen({})}>New service</Button>}>
      <p className="mb-2 text-xs text-muted">Named protocol/port combinations. Built-in services always show what they mean on the wire.</p>
      {err && <Alert>{err}</Alert>}
      <ul className="divide-y divide-line text-sm">
        {doc.services.map((s) => (
          <li key={s.name} className="flex items-start justify-between gap-2 py-2">
            <div><div className="font-medium">{s.name}</div><div className="text-xs text-muted">{entriesLabel(s.entries)}</div></div>
            {canWrite && (
              <div className="flex shrink-0 gap-1">
                <Button size="sm" variant="ghost" onClick={() => setOpen({ s })}>Edit<span className="sr-only"> {s.name}</span></Button>
                <Button size="sm" variant="ghost" onClick={() => {
                  const used = rulesUsingService(doc, s.name);
                  if (used.length) setErr(`Service ${s.name} is used by: ${usedBy(doc, used)}. Change those rules first.`);
                  else { setErr(""); edit(deleteService(doc, s.name)); }
                }}>Delete<span className="sr-only"> {s.name}</span></Button>
              </div>
            )}
          </li>
        ))}
        {doc.services.length === 0 && <li className="py-2 text-xs text-muted">No custom services.</li>}
      </ul>
      <button type="button" className="mt-2 text-xs text-accent underline" aria-expanded={showBuiltin} onClick={() => setShowBuiltin(!showBuiltin)}>
        {showBuiltin ? "Hide" : "Show"} built-in services
      </button>
      {showBuiltin && (
        <ul className="mt-1 text-xs">
          {services.filter((s) => s.builtin).map((s) => <li key={s.name}><strong>{s.name}</strong> <span className="text-muted">{entriesLabel(s.entries)}</span></li>)}
        </ul>
      )}
      {open && <ServiceDialog doc={doc} service={open.s} builtin={services} onClose={() => setOpen(null)} onSave={(s) => { edit(upsertService(doc, s, open.s?.name)); setOpen(null); }} />}
    </Card>
  );
}

function ServiceDialog({ doc, service, builtin, onSave, onClose }: { doc: Doc; service?: Service; builtin: ServiceInfo[]; onSave: (s: Service) => void; onClose: () => void }) {
  const [name, setName] = useState(service?.name ?? "");
  const [description, setDescription] = useState(service?.description ?? "");
  const [entries, setEntries] = useState<Entry[]>(service?.entries ?? [{ protocol: "tcp", ports: "" }]);
  const [err, setErr] = useState("");
  const save = () => {
    if (!/^[a-z0-9][a-z0-9_-]{0,47}$/.test(name)) return setErr("Service names use lowercase letters, digits, - and _.");
    if (builtin.some((b) => b.builtin && b.name === name)) return setErr(`“${name}” is a built-in service.`);
    if (doc.services.some((s) => s.name === name && s.name !== service?.name)) return setErr("A service with this name exists.");
    for (const e of entries) {
      if ((e.protocol === "tcp" || e.protocol === "udp") && !validPorts(e.ports ?? "")) return setErr("Ports must be 1–65535, ranges (8000-8010) or lists (80,443).");
    }
    onSave({ name, description, entries });
  };
  return (
    <Dialog title={service ? `Edit service ${service.name}` : "New service"} onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name"><Input data-autofocus value={name} onChange={(e) => setName(e.target.value)} placeholder="app-8812" /></Field>
        {entries.map((e, i) => (
          <div key={i} className="flex items-end gap-2">
            <Field label="Protocol">
              <Select value={e.protocol} onChange={(ev) => setEntries(entries.map((x, j) => (j === i ? { protocol: ev.target.value as Entry["protocol"], ports: x.ports, icmp_type: ev.target.value.startsWith("icmp") ? "echo-request" : undefined } : x)))}>
                <option value="tcp">TCP</option><option value="udp">UDP</option><option value="icmp">ICMP (IPv4)</option><option value="icmpv6">ICMPv6</option>
              </Select>
            </Field>
            {e.protocol === "tcp" || e.protocol === "udp" ? (
              <Field label="Port"><Input value={e.ports ?? ""} onChange={(ev) => setEntries(entries.map((x, j) => (j === i ? { ...x, ports: ev.target.value } : x)))} placeholder="8812" /></Field>
            ) : (
              <Field label="ICMP type">
                <Select value={e.icmp_type ?? "echo-request"} onChange={(ev) => setEntries(entries.map((x, j) => (j === i ? { ...x, icmp_type: ev.target.value } : x)))}>
                  {["echo-request", "echo-reply", "destination-unreachable", "time-exceeded"].map((t) => <option key={t}>{t}</option>)}
                </Select>
              </Field>
            )}
            {entries.length > 1 && <Button size="sm" variant="ghost" onClick={() => setEntries(entries.filter((_, j) => j !== i))}>Remove</Button>}
          </div>
        ))}
        <Button size="sm" onClick={() => setEntries([...entries, { protocol: "udp", ports: "" }])}>Add protocol/port</Button>
        <Field label="Description (optional)"><Input value={description} onChange={(e) => setDescription(e.target.value)} /></Field>
        {service && rulesUsingService(doc, service.name).length > 0 && (
          <Alert tone="warn">Rules using this service change too: {usedBy(doc, rulesUsingService(doc, service.name))}.</Alert>
        )}
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2"><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={save}>Save service</Button></div>
      </div>
    </Dialog>
  );
}
