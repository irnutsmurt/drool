import { useId, useMemo, useState } from "react";
import { Dialog } from "@/components/ui/Dialog";
import { Button } from "@/components/ui/Button";
import { Field, Input, Select } from "@/components/ui/Field";
import { Alert } from "@/components/ui/Alert";
import {
  addRule, affectedHosts, ensureCustomService, entriesLabel, kindLabel, parseRef, refLabel, uniqueRuleName, updateRule, validPorts,
  type Doc, type RefKind, type Rule, type ServiceInfo,
} from "./model";

/** Picks one reference: a host, group, network, CIDR or keyword. */
export function RefPicker({ doc, hosts, onAdd, label, exclude = [] }: {
  doc: Doc; hosts: { id: string; name: string }[]; onAdd: (ref: string) => void; label: string; exclude?: string[];
}) {
  const id = useId();
  const [kind, setKind] = useState<RefKind>("host");
  const [value, setValue] = useState("");
  const [cidrError, setCidrError] = useState("");
  const options: { value: string; label: string }[] =
    kind === "host" ? hosts.map((h) => ({ value: `host:${h.name}`, label: h.name }))
      : kind === "group" ? doc.groups.map((g) => ({ value: `group:${g.name}`, label: `${g.name} (${g.members.length} hosts)` }))
        : kind === "network" ? doc.networks.map((n) => ({ value: `network:${n.name}`, label: `${n.name} (${n.cidrs.join(", ")})` }))
          : [];
  const add = () => {
    let ref = "";
    if (kind === "internet" || kind === "internal" || kind === "any") ref = kind;
    else if (kind === "cidr") {
      if (!/^[0-9a-fA-F:.]+\/\d{1,3}$/.test(value.trim()) && !/^[0-9a-fA-F:.]+$/.test(value.trim())) {
        setCidrError("Enter an address or range such as 192.168.1.0/24 or 2001:db8::/32.");
        return;
      }
      const v = value.trim();
      ref = `cidr:${v.includes("/") ? v : v + (v.includes(":") ? "/128" : "/32")}`;
    } else ref = value;
    if (!ref || exclude.includes(ref)) return;
    setCidrError("");
    setValue("");
    onAdd(ref);
  };
  return (
    <div className="flex flex-wrap items-end gap-2">
      <label className="flex flex-col gap-1 text-xs">
        <span className="text-muted">{label}: kind</span>
        <Select aria-label={`${label} kind`} value={kind} onChange={(e) => { setKind(e.target.value as RefKind); setValue(""); }} className="w-44">
          {(["host", "group", "network", "cidr", "internet", "internal", "any"] as RefKind[]).map((k) => <option key={k} value={k}>{kindLabel[k]}</option>)}
        </Select>
      </label>
      {kind === "cidr" ? (
        <label className="flex flex-1 flex-col gap-1 text-xs">
          <span className="text-muted">Address or range</span>
          <Input id={id} aria-label={`${label} address or range`} placeholder="192.168.1.0/24 or 2001:db8::/32" value={value}
            onChange={(e) => setValue(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
        </label>
      ) : options.length > 0 || kind === "host" || kind === "group" || kind === "network" ? (
        <label className="flex flex-1 flex-col gap-1 text-xs">
          <span className="text-muted">{kindLabel[kind]}</span>
          <Select aria-label={`${label} ${kindLabel[kind].toLowerCase()}`} value={value} onChange={(e) => setValue(e.target.value)}>
            <option value="">{options.length ? "choose…" : `no ${kindLabel[kind].toLowerCase()}s defined`}</option>
            {options.map((o) => <option key={o.value} value={o.value} disabled={exclude.includes(o.value)}>{o.label}</option>)}
          </Select>
        </label>
      ) : null}
      <Button size="sm" onClick={add} disabled={(kind === "host" || kind === "group" || kind === "network") && !value}>Add {label.toLowerCase()}</Button>
      {cidrError && <p className="w-full text-xs text-danger">{cidrError}</p>}
    </div>
  );
}

function RefChips({ refs, onRemove, label }: { refs: string[]; onRemove: (r: string) => void; label: string }) {
  if (refs.length === 0) return <p className="text-xs text-muted">No {label.toLowerCase()} yet.</p>;
  return (
    <ul className="flex flex-wrap gap-1.5" aria-label={label}>
      {refs.map((r) => (
        <li key={r} className="inline-flex items-center gap-1 rounded bg-surface-2 px-2 py-0.5 text-xs">
          {["internet", "internal", "any"].includes(parseRef(r).kind) ? refLabel(r) : <><span className="text-muted">{kindLabel[parseRef(r).kind]}</span> {refLabel(r)}</>}
          <button type="button" className="ml-1 rounded px-1 text-muted hover:text-danger" aria-label={`Remove ${refLabel(r)} from ${label.toLowerCase()}`}
            onClick={() => onRemove(r)}>×</button>
        </li>
      ))}
    </ul>
  );
}

/**
 * Creates or edits one rule. From the topology it opens with a source and
 * destination (a drawn connection); from the Rules view with anything.
 */
export function RuleDialog({ doc, services, hosts, index, initial, onSave, onDelete, onClose }: {
  doc: Doc; services: ServiceInfo[]; hosts: { id: string; name: string }[];
  index?: number; initial?: Partial<Rule>; onSave: (d: Doc) => void; onDelete?: () => void; onClose: () => void;
}) {
  const editing = index !== undefined ? doc.policy[index] : undefined;
  const [rule, setRule] = useState<Rule>(() => {
    const base: Rule = editing ?? { name: "", description: "", enabled: true, action: "allow", from: [], to: [], services: [], ...initial };
    if (!base.name && base.from.length === 1 && base.to.length === 1) {
      base.name = uniqueRuleName(doc, `${refLabel(base.from[0]!)}-to-${refLabel(base.to[0]!)}`.toLowerCase().replace(/[^a-z0-9._-]+/g, "-"));
    }
    return { ...base };
  });
  const [svcChoice, setSvcChoice] = useState("");
  const [protocol, setProtocol] = useState<"tcp" | "udp">("tcp");
  const [port, setPort] = useState("");
  const [customName, setCustomName] = useState("");
  const [work, setWork] = useState<Doc>(doc); // custom services created here
  const [error, setError] = useState("");
  const all = useMemo(() => [...services.filter((s) => s.builtin), ...work.services.map((s) => ({ ...s, builtin: false }))], [services, work]);

  const addService = () => {
    setError("");
    if (svcChoice === "__custom") {
      if (!validPorts(port)) {
        setError("Port must be 1–65535, a range such as 8000-8010, or a list such as 80,443.");
        return;
      }
      let { doc: d, name } = ensureCustomService(work, services, protocol, port);
      if (customName.trim() && d !== work) {
        const wanted = customName.trim().toLowerCase();
        if (!/^[a-z0-9][a-z0-9_-]{0,47}$/.test(wanted) || all.some((s) => s.name === wanted)) {
          setError("Service names use lowercase letters, digits, - and _, and must be new.");
          return;
        }
        d = { ...d, services: d.services.map((s) => (s.name === name ? { ...s, name: wanted } : s)) };
        name = wanted;
      }
      setWork(d);
      if (!rule.services.includes(name)) setRule({ ...rule, services: [...rule.services, name] });
      setPort("");
      setCustomName("");
      return;
    }
    if (svcChoice && !rule.services.includes(svcChoice)) setRule({ ...rule, services: [...rule.services, svcChoice] });
  };

  const save = () => {
    const name = rule.name.trim();
    if (!name) return setError("Give the rule a name.");
    if (doc.policy.some((r, i) => i !== index && r.name.toLowerCase() === name.toLowerCase())) return setError(`A rule named “${name}” exists.`);
    if (rule.from.length === 0 || rule.to.length === 0) return setError("Choose at least one source and one destination.");
    if (rule.services.length === 0) return setError("Choose at least one service.");
    const r = { ...rule, name };
    onSave(index !== undefined ? updateRule(work, index, r) : addRule(work, r));
  };

  const affected = affectedHosts(work, rule, hosts);
  // DROOL puts a rule on the hosts its destination names (a host, a pack,
  // Anywhere); addresses as destination (Internal networks, a network, a
  // CIDR) name none, even a host whose address they contain.
  const namesHost = (r: string) => ["host", "group", "any"].includes(parseRef(r).kind);
  const addressDest = rule.to.length > 0 && !rule.to.some(namesHost);
  const destText = rule.to.map(refLabel).join(", ");
  return (
    <Dialog title={editing ? `Edit rule ${editing.name}` : "New rule"} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <div className="grid gap-3 md:grid-cols-[1fr_16rem]">
          <Field label="Name"><Input data-autofocus value={rule.name} onChange={(e) => setRule({ ...rule, name: e.target.value })} /></Field>
          <fieldset>
            <legend className="mb-1.5 text-sm font-medium">Action</legend>
            <div className="flex gap-4">
              {(["allow", "deny"] as const).map((a) => (
                <label key={a} className="flex items-center gap-1.5">
                  <input type="radio" name="rule-action" checked={rule.action === a} onChange={() => setRule({ ...rule, action: a })} />
                  {a === "allow" ? "Allow" : "Deny (wins over allows)"}
                </label>
              ))}
            </div>
          </fieldset>
        </div>
        <section aria-label="Source" className="space-y-2 rounded-ui border border-line p-3">
          <h3 className="font-medium">Source <span className="font-normal text-muted">— who starts the connection</span></h3>
          <RefChips refs={rule.from} label="Source" onRemove={(r) => setRule({ ...rule, from: rule.from.filter((x) => x !== r) })} />
          <RefPicker doc={work} hosts={hosts} label="Source" exclude={rule.from} onAdd={(r) => setRule({ ...rule, from: [...rule.from, r] })} />
        </section>
        <section aria-label="Destination" className="space-y-2 rounded-ui border border-line p-3">
          <h3 className="font-medium">Destination <span className="font-normal text-muted">— who receives it</span></h3>
          <RefChips refs={rule.to} label="Destination" onRemove={(r) => setRule({ ...rule, to: rule.to.filter((x) => x !== r) })} />
          <RefPicker doc={work} hosts={hosts} label="Destination" exclude={rule.to} onAdd={(r) => setRule({ ...rule, to: [...rule.to, r] })} />
        </section>
        <section aria-label="Services" className="space-y-2 rounded-ui border border-line p-3">
          <h3 className="font-medium">Service</h3>
          {rule.services.length === 0 ? <p className="text-xs text-muted">No services yet.</p> : (
            <ul className="flex flex-wrap gap-1.5" aria-label="Chosen services">
              {rule.services.map((s) => {
                const info = all.find((x) => x.name === s);
                return (
                  <li key={s} className="inline-flex items-center gap-1 rounded bg-surface-2 px-2 py-0.5 text-xs">
                    <strong>{s}</strong> <span className="text-muted">{info ? entriesLabel(info.entries) : "unknown"}</span>
                    <button type="button" className="ml-1 rounded px-1 text-muted hover:text-danger" aria-label={`Remove service ${s}`}
                      onClick={() => setRule({ ...rule, services: rule.services.filter((x) => x !== s) })}>×</button>
                  </li>
                );
              })}
            </ul>
          )}
          <div className="flex flex-wrap items-end gap-2">
            <label className="flex flex-1 flex-col gap-1 text-xs">
              <span className="text-muted">Service</span>
              <Select aria-label="Service" value={svcChoice} onChange={(e) => setSvcChoice(e.target.value)}>
                <option value="">choose…</option>
                <option value="__custom">Custom (protocol and port)…</option>
                {all.map((s) => <option key={s.name} value={s.name} disabled={rule.services.includes(s.name)}>{s.name} — {entriesLabel(s.entries)}{s.builtin ? "" : " (custom)"}</option>)}
              </Select>
            </label>
            {svcChoice === "__custom" && (
              <>
                <label className="flex flex-col gap-1 text-xs">
                  <span className="text-muted">Protocol</span>
                  <Select aria-label="Protocol" value={protocol} onChange={(e) => setProtocol(e.target.value as "tcp" | "udp")} className="w-24">
                    <option value="tcp">TCP</option>
                    <option value="udp">UDP</option>
                  </Select>
                </label>
                <label className="flex flex-col gap-1 text-xs">
                  <span className="text-muted">Port</span>
                  <Input aria-label="Port" placeholder="8812, 8000-8010, 80,443" value={port} onChange={(e) => setPort(e.target.value)} className="w-40"
                    onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); addService(); } }} />
                </label>
                <label className="flex flex-col gap-1 text-xs">
                  <span className="text-muted">Name (optional)</span>
                  <Input aria-label="Custom service name" placeholder={port ? `${protocol}-${port}` : "app-8812"} value={customName} onChange={(e) => setCustomName(e.target.value)} className="w-36" />
                </label>
              </>
            )}
            <Button size="sm" onClick={addService} disabled={!svcChoice}>Add service</Button>
          </div>
        </section>
        <Field label="Description (optional)"><Input value={rule.description} onChange={(e) => setRule({ ...rule, description: e.target.value })} /></Field>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={rule.enabled} onChange={(e) => setRule({ ...rule, enabled: e.target.checked })} /> Enabled
        </label>
        {affected.length > 0 && (
          <p className="text-xs text-muted" title={affected.join(", ")}>
            Changes the firewall of {affected.length === 1 ? "" : `${affected.length} hosts: `}{affected.slice(0, 10).join(", ")}{affected.length > 10 ? `, and ${affected.length - 10} more` : ""}.
            {rule.action === "deny" && rule.from.some((f) => ["any", "internal"].includes(f) || f.startsWith("group:")) && " A deny also stops these sources from sending it (it applies on both ends)."}
          </p>
        )}
        {addressDest && (rule.from.some(namesHost) ? (
          <p className="text-xs text-muted" data-testid="rule-outbound-note">
            For {rule.from.filter(namesHost).map(refLabel).join(", ")}, this rule {rule.action === "deny" ? "denies" : "allows (where outbound traffic is restricted)"} outbound
            connections to {destText}. To control connections <em>to</em> a host, make that host the destination.
          </p>
        ) : (
          <Alert tone="warn">
            <span data-testid="rule-no-host">No host gets this rule: its destination, {destText}, is addresses, not a DROOL host, and DROOL puts a rule on the hosts
              its destination names. To {rule.action === "deny" ? "deny" : "allow"} these connections to a host, choose the host (or a pack, or Anywhere) as the destination.</span>
          </Alert>
        ))}
        {error && <Alert>{error}</Alert>}
        <div className="flex flex-wrap justify-between gap-2">
          <div>{onDelete && <Button variant="danger" onClick={onDelete}>Delete rule</Button>}</div>
          <div className="flex gap-2">
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" onClick={save}>{editing ? "Update rule" : "Add rule"}</Button>
          </div>
        </div>
        <p className="text-xs text-muted">Changes stay in the draft until you save. Nothing reaches a host until you apply.</p>
      </div>
    </Dialog>
  );
}
