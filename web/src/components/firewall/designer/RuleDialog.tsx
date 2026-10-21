import { useMemo, useState } from "react";
import { Dialog } from "@/components/ui/Dialog";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { ComboPicker, type PickOption } from "@/components/ui/ComboPicker";
import { Field, Input } from "@/components/ui/Field";
import { Alert } from "@/components/ui/Alert";
import { cx } from "@/components/ui/cx";
import {
  addressChoice, addRule, affectedHosts, allServices, ensureCustomService, entriesLabel, kindLabel, parseRef, portChoices, refChoices, refLabel,
  serviceLabel, suggestRuleName, updateRule, type Doc, type Rule, type ServiceInfo,
} from "./model";

/** A host gets the rule when its destination names it (a host, a Pack,
 * Anywhere); an address destination puts it only on the hosts its source
 * names (as an outbound rule). */
const namesHost = (r: string) => ["host", "group", "any"].includes(parseRef(r).kind);

const chipKind = (r: string) => {
  const k = parseRef(r).kind;
  return k === "cidr" ? "Address" : k === "host" || k === "group" || k === "network" ? kindLabel[k] : undefined;
};

/**
 * Creates or edits one rule as a sentence: Allow or Deny, from a source,
 * to a destination, on a service — each chosen in one step from a search
 * over every kind of choice (hosts, Packs, networks, Internal networks, an
 * address typed in; named services or a port typed in). The name follows
 * from the choices unless someone types one (Name, description and status).
 *
 * The dialog says which hosts the rule changes, and when it would change
 * none, says so and asks for "anyway" before keeping it. "Save and review…"
 * saves the policy and goes straight on to the review of the affected
 * hosts; "Add to draft" keeps it unsaved with the draft's other changes.
 */
export function RuleDialog({ doc, services, hosts, index, initial, draftChanged, onSave, onSaveAndReview, onDelete, onClose }: {
  doc: Doc; services: ServiceInfo[]; hosts: { id: string; name: string }[];
  index?: number; initial?: Partial<Rule>;
  /** The draft has other unsaved changes (saving saves them too). */
  draftChanged?: boolean;
  /** Add to (or update) the draft without saving. */
  onSave: (d: Doc) => void;
  /** Save the policy with this rule, then review applying it. */
  onSaveAndReview?: (d: Doc) => void;
  onDelete?: () => void; onClose: () => void;
}) {
  const editing = index !== undefined ? doc.policy[index] : undefined;
  const [rule, setRule] = useState<Rule>(() => ({ ...(editing ?? { name: "", description: "", enabled: true, action: "allow", from: [], to: [], services: [] }), ...(editing ? {} : initial) }));
  // The name follows the choices until someone types one; an existing
  // rule keeps its name.
  const [nameTouched, setNameTouched] = useState(!!editing || !!initial?.name);
  const [work, setWork] = useState<Doc>(doc); // custom services created here
  const [error, setError] = useState("");
  const all = useMemo(() => allServices(work, services), [services, work]);
  const name = nameTouched ? rule.name : suggestRuleName(work, rule, index);

  const sourceChoices = useMemo(() => refChoices(work, hosts, "source"), [work, hosts]);
  const destChoices = useMemo(() => refChoices(work, hosts, "destination"), [work, hosts]);
  const serviceChoices: PickOption[] = useMemo(() => all.map((s) => ({
    value: s.name, label: s.name, group: s.builtin ? "Services" : "Your services", detail: entriesLabel(s.entries).trim(), search: s.description,
  })), [all]);
  const suggestPort = (q: string): PickOption[] => portChoices(q).map(({ protocol, ports }) => {
    const same = all.find((s) => s.entries.length === 1 && s.entries[0]!.protocol === protocol && s.entries[0]!.ports === ports);
    return same ? { value: same.name, label: same.name, group: "Port", detail: `${protocol.toUpperCase()} ${ports}` }
      : { value: `port:${protocol}:${ports}`, label: `${protocol.toUpperCase()} ${ports}`, group: "Port", detail: "a custom service" };
  });
  const addService = (v: string) => {
    let svc = v;
    if (v.startsWith("port:")) {
      const [, protocol, ports] = v.split(":") as [string, "tcp" | "udp", string];
      const r = ensureCustomService(work, services, protocol, ports);
      setWork(r.doc);
      svc = r.name;
    }
    if (!rule.services.includes(svc)) setRule((cur) => ({ ...cur, services: [...cur.services, svc] }));
  };

  const complete = rule.from.length > 0 && rule.to.length > 0 && rule.services.length > 0;
  const affected = affectedHosts(work, rule, hosts);
  const addressDest = rule.to.length > 0 && !rule.to.some(namesHost);
  const noHost = complete && addressDest && !rule.from.some(namesHost);
  const destText = rule.to.map(refLabel).join(", ");

  const finish = (save: (d: Doc) => void) => {
    const n = name.trim();
    if (!n) return setError("Give the rule a name (Name, description and status).");
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/.test(n)) return setError("A rule name uses letters, digits, “.”, “_” and “-” (at most 63), starting with a letter or digit.");
    if (doc.policy.some((r, i) => i !== index && r.name.toLowerCase() === n.toLowerCase())) return setError(`A rule named “${n}” exists: choose another name.`);
    if (rule.from.length === 0) return setError("Choose a source: who starts the connection.");
    if (rule.to.length === 0) return setError("Choose a destination: the host or hosts that receive it.");
    if (rule.services.length === 0) return setError("Choose a service, or type a port.");
    const r = { ...rule, name: n };
    save(index !== undefined ? updateRule(work, index, r) : addRule(work, r));
  };

  const services_ = rule.services.map((s) => serviceLabel(s, all)).join(", ");
  const sentence = !complete ? "Choose a source, a destination and a service."
    : `${rule.action === "deny" ? "Denies" : "Allows"} ${services_} from ${rule.from.map(refLabel).join(", ")} to ${destText}.`;
  return (
    <Dialog title={editing ? `Edit rule ${editing.name}` : "New rule"} onClose={onClose} wide testId="rule-dialog"
      footer={
        <>
          {onDelete && <Button variant="danger" className="mr-auto" onClick={onDelete}>Delete rule</Button>}
          <Button onClick={onClose}>Cancel</Button>
          <Button variant={onSaveAndReview ? "secondary" : "primary"} onClick={() => finish(onSave)}
            title="Keep it in the draft with your other changes; nothing is saved yet">
            {editing ? "Update draft" : "Add to draft"}{noHost ? " anyway" : ""}
          </Button>
          {onSaveAndReview && (
            <Button variant="primary" onClick={() => finish(onSaveAndReview)}>{noHost ? "Save anyway" : "Save and review…"}</Button>
          )}
        </>
      }>
      <div className="space-y-4 text-sm">
        <fieldset>
          <legend className="mb-1.5 text-sm font-medium">Action</legend>
          <div className="inline-flex overflow-hidden rounded-ui border border-line">
            {(["allow", "deny"] as const).map((a) => (
              <label key={a} className={cx("flex cursor-pointer items-center gap-1.5 px-3 py-1.5 pointer-coarse:min-h-11",
                rule.action === a ? (a === "deny" ? "bg-danger-bg font-medium" : "bg-surface-2 font-medium") : "bg-surface")}>
                <input type="radio" name="rule-action" className="accent-[var(--ui-accent)]" checked={rule.action === a} onChange={() => setRule({ ...rule, action: a })} />
                {a === "allow" ? "Allow" : "Deny"}
              </label>
            ))}
          </div>
          <p className="mt-1 text-xs text-muted">{rule.action === "deny" ? "Blocks the connection, even where another rule allows it." : "Lets the connection through."}</p>
        </fieldset>
        <ComboPicker label="Source" description="who starts the connection" values={rule.from} options={sourceChoices} testId="rule-source"
          autoFocus={rule.from.length === 0} chipLabel={refLabel} chipKind={chipKind} suggest={(q) => addressChoice(q, "source")}
          placeholder="Internal networks, a host, a Pack, a network, or an address"
          emptyText={(q) => <>Nothing matches “{q}”. Type an address such as 192.168.1.0/24, or name a network under Networks.</>}
          onAdd={(r) => setRule((cur) => ({ ...cur, from: [...cur.from, r] }))} onRemove={(r) => setRule((cur) => ({ ...cur, from: cur.from.filter((x) => x !== r) }))} />
        <ComboPicker label="Destination" description="the host or hosts that receive it" values={rule.to} options={destChoices} testId="rule-destination"
          autoFocus={rule.from.length > 0 && rule.to.length === 0} chipLabel={refLabel} chipKind={chipKind} suggest={(q) => addressChoice(q, "destination")}
          placeholder="A host or a Pack"
          emptyText={(q) => <>Nothing matches “{q}”. Choose the host or Pack that should accept the connection.</>}
          onAdd={(r) => setRule((cur) => ({ ...cur, to: [...cur.to, r] }))} onRemove={(r) => setRule((cur) => ({ ...cur, to: cur.to.filter((x) => x !== r) }))} />
        <ComboPicker label="Service" description="what it connects to" values={rule.services} options={serviceChoices} testId="rule-service"
          autoFocus={rule.from.length > 0 && rule.to.length > 0 && rule.services.length === 0}
          chipLabel={(s) => serviceLabel(s, all)} suggest={suggestPort} placeholder="ssh, https, smb… or a port such as 8812, udp 53"
          emptyText={(q) => <>No service matches “{q}”. Type a port such as 8812, udp 53 or 8000-8010.</>}
          onAdd={addService} onRemove={(s) => setRule((cur) => ({ ...cur, services: cur.services.filter((x) => x !== s) }))} />

        <div className="space-y-1.5 rounded-ui border border-line p-3" data-testid="rule-summary">
          <p className={complete ? "font-medium" : "text-muted"}>{sentence}</p>
          {affected.length > 0 && (
            <p className="text-xs text-muted" title={affected.join(", ")} data-testid="rule-affected">
              Changes the firewall of {affected.length === 1 ? "" : `${affected.length} hosts: `}{affected.slice(0, 10).join(", ")}{affected.length > 10 ? `, and ${affected.length - 10} more` : ""}.
              {rule.action === "deny" && rule.from.some((f) => ["any", "internal"].includes(f) || f.startsWith("group:")) && " A deny also stops these sources from sending it (it applies on both ends)."}
            </p>
          )}
          {addressDest && rule.from.some(namesHost) && (
            <p className="text-xs text-muted" data-testid="rule-outbound-note">
              For {rule.from.filter(namesHost).map(refLabel).join(", ")}, this rule {rule.action === "deny" ? "denies" : "allows (where outbound traffic is restricted)"} outbound
              connections to {destText}. To control connections <em>to</em> a host, make that host the destination.
            </p>
          )}
          {noHost && (
            <Alert tone="warn">
              <span data-testid="rule-no-host">No host gets this rule: its destination, {destText}, is addresses, not a DROOL host, and DROOL puts a rule on the hosts
                its destination names. To {rule.action === "deny" ? "deny" : "allow"} these connections to a host, choose the host (or a Pack, or Anywhere) as the destination.</span>
            </Alert>
          )}
        </div>

        <details className="rounded-ui border border-line p-3" data-testid="rule-advanced">
          <summary className="cursor-pointer font-medium">Name, description and status</summary>
          <div className="mt-3 space-y-3">
            <Field label="Name" hint={nameTouched ? undefined : "Follows from the choices above until you change it."}>
              <Input value={name} onChange={(e) => { setNameTouched(true); setRule({ ...rule, name: e.target.value }); }} />
            </Field>
            <Field label="Description (optional)"><Input value={rule.description} onChange={(e) => setRule({ ...rule, description: e.target.value })} /></Field>
            <Checkbox checked={rule.enabled} onChange={(v) => setRule({ ...rule, enabled: v })} label="Enabled" hint="A disabled rule stays in the policy and does nothing." />
          </div>
        </details>
        {error && <Alert>{error}</Alert>}
        <p className="text-xs text-muted">
          {onSaveAndReview ? <>Save and review saves the policy{draftChanged ? " with the draft's other unsaved changes" : ""}, then shows what applying it changes on each host.
            Nothing reaches a host until you apply.</> : "Changes stay in the draft until you save. Nothing reaches a host until you apply."}
        </p>
      </div>
    </Dialog>
  );
}
