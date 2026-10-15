import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useGroups, useHosts } from "@/api/queries";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { nk, type NetFlowDetail, type NetRule, type NetRuleInput } from "./queries";

/** The match and the words of a classification, as the form holds them. */
type Form = {
  label: string;
  note: string;
  expected: boolean;
  hosts: string; // "" any host | "host:ID" | "pack:ID"
  direction: "" | "in" | "out";
  protocol: "" | "tcp" | "udp";
  port: string;
  remote: "any" | "cidr" | "host" | "pack";
  remoteValue: string; // the address or CIDR, the host id or the pack id
};

function fromRule(r: NetRule): Form {
  return {
    label: r.label, note: r.note, expected: r.expected,
    hosts: r.host_id ? `host:${r.host_id}` : r.host_group_id ? `pack:${r.host_group_id}` : "",
    direction: r.direction ?? "", protocol: r.protocol ?? "", port: r.port ? String(r.port) : "",
    remote: r.remote_host_id ? "host" : r.remote_group_id ? "pack" : r.remote_cidr ? "cidr" : "any",
    remoteValue: r.remote_host_id ?? r.remote_group_id ?? r.remote_cidr ?? "",
  };
}

/** Exactly this connection: its host, direction, protocol, port and
 * remote address. */
function fromFlow(f: NetFlowDetail): Form {
  return {
    label: "", note: "", expected: false, hosts: `host:${f.host.id}`, direction: f.direction, protocol: f.protocol,
    port: String(f.port), remote: "cidr", remoteValue: f.remote_ip,
  };
}

const sameMatch = (a: Form, b: Form) =>
  a.hosts === b.hosts && a.direction === b.direction && a.protocol === b.protocol && a.port === b.port && a.remote === b.remote &&
  a.remoteValue.trim() === b.remoteValue.trim();

function toInput(f: Form, origin: "rule" | "flow"): NetRuleInput {
  const [kind, id] = f.hosts.split(":");
  return {
    label: f.label, note: f.note, expected: f.expected, origin,
    host_id: kind === "host" ? id : "", host_group_id: kind === "pack" ? id : "",
    direction: f.direction, protocol: f.protocol, port: f.port ? Number(f.port) : 0,
    remote_cidr: f.remote === "cidr" ? f.remoteValue.trim() : "",
    remote_host_id: f.remote === "host" ? f.remoteValue : "",
    remote_group_id: f.remote === "pack" ? f.remoteValue : "",
  };
}

/** Say what recurring traffic is. From a connection, the form starts as
 * exactly that connection (a label for it alone); widening the hosts, the
 * port or the remote side classifies similar traffic too. Labels only:
 * nothing is enforced, no firewall rule comes from it. */
export function ClassifyDialog({ flow, rule, onClose }: { flow?: NetFlowDetail; rule?: NetRule; onClose: () => void }) {
  const qc = useQueryClient();
  const hosts = useHosts();
  const packs = useGroups();
  const start = rule ? fromRule(rule) : flow ? fromFlow(flow) : fromRule({ label: "", note: "", expected: false } as NetRule);
  const [f, setF] = useState<Form>(start);
  const set = (p: Partial<Form>) => setF({ ...f, ...p });
  const exact = flow && !rule ? fromFlow(flow) : rule?.origin === "flow" ? fromRule(rule) : undefined;
  const origin = exact && sameMatch(f, exact) ? "flow" : "rule";
  const save = useMutation({
    mutationFn: () => {
      const body = toInput(f, origin);
      return rule
        ? unwrap(api.PUT("/network/classifications/{id}", { params: { path: { id: rule.id } }, body }))
        : unwrap(api.POST("/network/classifications", { body }));
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: nk.all });
      onClose();
    },
  });
  const hostList = hosts.data?.items ?? [];
  const packList = packs.data?.items ?? [];
  return (
    <Dialog title={rule ? "Edit classification" : "Classify traffic"} onClose={onClose}>
      <form className="space-y-3" data-testid="classify-form" onSubmit={(e) => { e.preventDefault(); save.mutate(); }}>
        <Field label="Label" hint="What this traffic is, for example “NFS media storage” or “Backups to the NAS”.">
          <Input data-autofocus required maxLength={120} value={f.label} onChange={(e) => set({ label: e.target.value })} />
        </Field>
        <Field label="Hosts">
          <Select value={f.hosts} onChange={(e) => set({ hosts: e.target.value })}>
            <option value="">any host</option>
            {packList.length > 0 && <optgroup label="Packs">{packList.map((p) => <option key={p.id} value={`pack:${p.id}`}>Pack: {p.name}</option>)}</optgroup>}
            <optgroup label="Hosts">{hostList.map((h) => <option key={h.id} value={`host:${h.id}`}>{h.name}</option>)}</optgroup>
          </Select>
        </Field>
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Direction">
            <Select value={f.direction} onChange={(e) => set({ direction: e.target.value as Form["direction"] })}>
              <option value="">either</option><option value="out">outbound</option><option value="in">inbound</option>
            </Select>
          </Field>
          <Field label="Protocol">
            <Select value={f.protocol} onChange={(e) => set({ protocol: e.target.value as Form["protocol"] })}>
              <option value="">TCP or UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option>
            </Select>
          </Field>
          <Field label="Service port">
            <Input type="number" min={1} max={65535} inputMode="numeric" placeholder="any" value={f.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
        </div>
        <Field label="Remote side">
          <div className="flex flex-col gap-2 sm:flex-row">
            <Select className="sm:!w-56" value={f.remote} onChange={(e) => set({ remote: e.target.value as Form["remote"], remoteValue: e.target.value === "cidr" && flow ? flow.remote_ip : "" })}>
              <option value="any">anywhere</option>
              <option value="cidr">an address or range (CIDR)</option>
              <option value="host">a DROOL host</option>
              <option value="pack">a Pack&apos;s hosts</option>
            </Select>
            {f.remote === "cidr" && <Input aria-label="Address or CIDR" placeholder="192.168.1.0/24" value={f.remoteValue} onChange={(e) => set({ remoteValue: e.target.value })} />}
            {f.remote === "host" && (
              <Select aria-label="Remote host" value={f.remoteValue} onChange={(e) => set({ remoteValue: e.target.value })}>
                <option value="">choose a host</option>
                {hostList.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
              </Select>
            )}
            {f.remote === "pack" && (
              <Select aria-label="Remote Pack" value={f.remoteValue} onChange={(e) => set({ remoteValue: e.target.value })}>
                <option value="">choose a Pack</option>
                {packList.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </Select>
            )}
          </div>
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={f.expected} onChange={(e) => set({ expected: e.target.checked })} />
          This traffic is expected
        </label>
        <Field label="Note (optional)">
          <Input maxLength={500} value={f.note} onChange={(e) => set({ note: e.target.value })} />
        </Field>
        <p className="text-xs text-muted" data-testid="classify-scope">
          {origin === "flow"
            ? "As it stands, this labels this connection only. Widen the hosts, the port or the remote side to classify similar traffic too."
            : "This classifies every connection that matches, already observed or observed later. The most specific classification wins."}
          {" "}Labels only: nothing is blocked or allowed, and no firewall rule is created. DROOL&apos;s own traffic keeps its DROOL classification.
        </p>
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={save.isPending || !f.label.trim()}>Save classification</Button>
        </div>
      </form>
    </Dialog>
  );
}
