import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { ApplyDialog, modeInfo, statusLabels, TxList } from "@/components/firewall/parts";
import { fk, useFwGroups, useFwHosts, useFwSettings, useFwTransactions, type FwSimResult } from "@/components/firewall/queries";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button, secondaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { cx } from "@/components/ui/cx";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { ReadOnlyNote } from "@/components/ui/ReadOnlyNote";
import { DesignerPage } from "@/components/firewall/designer/DesignerPage";
import { Table } from "@/components/ui/Table";
import { errorMessage } from "@/lib/format";

const tabs = [
  { id: "hosts", label: "Hosts" },
  { id: "designer", label: "Policy" },
  { id: "simulate", label: "Can A reach B?" },
  { id: "transactions", label: "Transactions" },
  { id: "settings", label: "Settings" },
] as const;

export function FirewallPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  let tab = params.get("tab") ?? "hosts";
  // Policies and services are edited in the designer now (M8 links still work).
  if (tab === "policies" || tab === "services") tab = "designer";
  return (
    <>
      <PageHeader title="Firewall"
        description={<>What may talk to what, applied to plain-nftables hosts with <span className="font-medium text-fg">automatic rollback protection</span>: if a fresh
          management connection can&apos;t verify a change, the host reverts it by itself.</>}
        actions={can("network.read") && <Link to="/network" className={secondaryLink}>Observed traffic</Link>} />
      <div role="tablist" aria-label="Firewall sections" className="-mx-1 mb-4 flex gap-1 overflow-x-auto border-b border-line px-1">
        {tabs.map((t) => (
          <button key={t.id} role="tab" aria-selected={tab === t.id} onClick={() => setParams({ tab: t.id })}
            className={cx("-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm", tab === t.id ? "border-accent font-medium" : "border-transparent text-muted hover:text-fg")}>
            {t.label}
          </button>
        ))}
      </div>
      {tab === "hosts" && <HostsTab />}
      {tab === "designer" && <DesignerPage />}
      {tab === "simulate" && <SimulateTab />}
      {tab === "transactions" && <TransactionsTab />}
      {tab === "settings" && <SettingsTab />}
    </>
  );
}

function HostsTab() {
  const hosts = useFwHosts();
  const { can } = useAuth();
  const [sel, setSel] = useState<string[]>([]);
  const [applying, setApplying] = useState(false);
  const items = hosts.data?.items ?? [];
  const prop = hosts.data?.propagation;
  const toggle = (id: string) => setSel(sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id]);
  return (
    <div className="space-y-4">
      {prop && prop.enforced > 0 && prop.current < prop.enforced && (
        <Alert tone="warn">
          Policy propagation incomplete: {prop.current}/{prop.enforced} enforced hosts current
          {(prop.stale ?? []).length > 0 && <>; address updates in flight on {(prop.stale ?? []).join(", ")}</>}
          {(prop.rules_changed ?? []).length > 0 && <>; policy changes not applied on {(prop.rules_changed ?? []).join(", ")}</>}.
        </Alert>
      )}
      <Card title="Hosts" actions={can("firewall.write") && sel.length > 0 && <Button size="sm" variant="primary" onClick={() => setApplying(true)}>Apply to {sel.length} host(s)…</Button>}>
        <Table
          rows={items}
          rowKey={(h) => h.host_id}
          empty={hosts.isPending ? "Loading…" : "No hosts yet. Each host you add appears here with its firewall mode (Off until you choose) and status."}
          columns={[
            { header: "", cell: (h) => can("firewall.write") && h.mode === "enforce" ? <input type="checkbox" aria-label={`Select ${h.host_name}`} checked={sel.includes(h.host_id)} onChange={() => toggle(h.host_id)} /> : null },
            { header: "Host", cell: (h) => <Link className="font-medium underline" to={`/hosts/${h.host_id}/firewall`}>{h.host_name}</Link> },
            { header: "Mode", cell: (h) => modeInfo[h.mode]?.label ?? h.mode },
            { header: "Environment", cell: (h) => !h.checked ? <span className="text-muted">not checked</span>
              : h.supported ? <Badge tone="ok">supported</Badge> : <span title={h.reason}><Badge tone="danger">refused</Badge> <span className="text-xs text-muted">{h.reason}</span></span> },
            { header: "Status", cell: (h) => <Badge tone={statusLabels[h.status].tone}>{statusLabels[h.status].text}</Badge> },
            { header: "Revision", cell: (h) => (h.enforced ? h.revision : "—") },
            { header: "Path", cell: (h) => <span>{h.transport === "agent" ? "Shepherd" : h.transport} <span className="text-xs text-muted">{h.learning === "continuous" ? "· learning" : h.learning === "snapshot" ? "· snapshots" : ""}</span></span> },
            ...(can("network.read") ? [{ header: "Traffic", cell: (h: (typeof items)[number]) => <Link className="text-sm underline" to={`/hosts/${h.host_id}/network`}>Observed</Link> }] : []),
          ]}
        />
      </Card>
      {applying && <ApplyDialog hostIds={sel} names={items.filter((h) => sel.includes(h.host_id)).map((h) => h.host_name)} onClose={() => { setApplying(false); setSel([]); }} />}
    </div>
  );
}

function SimulateTab() {
  const hosts = useHosts();
  const [from, setFrom] = useState({ host: "", address: "" });
  const [to, setTo] = useState({ host: "", address: "" });
  const [proto, setProto] = useState<"tcp" | "udp">("tcp");
  const [port, setPort] = useState("22");
  const sim = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/simulate", { body: {
      from_host: from.host || undefined, from_address: from.host ? undefined : from.address,
      to_host: to.host || undefined, to_address: to.host ? undefined : to.address, protocol: proto, port: Number(port),
    } })),
  });
  const side = (v: typeof from, set: (x: typeof from) => void, label: string) => (
    <div className="space-y-1">
      <Field label={label}>
        <Select value={v.host} onChange={(e) => set({ ...v, host: e.target.value })}>
          <option value="">an address…</option>
          {(hosts.data?.items ?? []).map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
        </Select>
      </Field>
      {!v.host && <Input value={v.address} onChange={(e) => set({ ...v, address: e.target.value })} placeholder="203.0.113.7" />}
    </div>
  );
  const r: FwSimResult | undefined = sim.data;
  return (
    <Card title="Can A reach B?">
      <div className="grid gap-3 md:grid-cols-[1fr_1fr_7rem_7rem_auto] md:items-end">
        {side(from, setFrom, "From")}
        {side(to, setTo, "To")}
        <Field label="Protocol"><Select value={proto} onChange={(e) => setProto(e.target.value as "tcp" | "udp")}><option>tcp</option><option>udp</option></Select></Field>
        <Field label="Port"><Input type="number" value={port} onChange={(e) => setPort(e.target.value)} /></Field>
        <Button variant="primary" disabled={sim.isPending} onClick={() => sim.mutate()}>Ask</Button>
      </div>
      {sim.error && <div className="mt-3"><Alert>{errorMessage(sim.error)}</Alert></div>}
      {r && (
        <div className="mt-4 space-y-2 text-sm">
          <div className="flex items-center gap-3">
            <span className={cx("text-2xl font-semibold", r.answer === "YES" ? "text-ok" : r.answer === "NO" ? "text-danger" : "text-warn")}>{r.answer}</span>
            <span>{r.from} → {r.to} {proto.toUpperCase()}/{port}</span>
          </div>
          <ul className="space-y-1">
            {r.hops.map((h) => (
              <li key={h.host + h.direction} className="rounded-ui border border-line p-2">
                <strong>{h.host}</strong> ({h.direction === "out" ? "leaving" : "arriving"}):{" "}
                <Badge tone={h.verdict === "accept" ? "ok" : "danger"}>{h.verdict === "accept" ? "allowed" : "blocked"}</Badge>{" "}
                {h.reason.kind === "management" && <Badge tone="accent">management path</Badge>} {h.reason.text}
                {h.rule && <span className="block text-xs text-muted">matching rule: {h.rule}</span>}
                {(h.notes ?? []).map((n) => <span key={n} className="block text-xs text-warn">{n}</span>)}
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted">Evaluated: {r.pairs.join(", ")}. Simulation uses the policy DROOL wants; notes show where hosts haven't applied it yet.</p>
        </div>
      )}
    </Card>
  );
}

function TransactionsTab() {
  const txs = useFwTransactions();
  const hosts = useHosts();
  const name = (id: string) => hosts.data?.items.find((h) => h.id === id)?.name ?? id;
  return <Card title="Transactions"><TxList txs={txs.data?.items ?? []} hostName={name} /></Card>;
}

function SettingsTab() {
  const s = useFwSettings();
  const groups = useFwGroups();
  const { can } = useAuth();
  const qc = useQueryClient();
  const [ranges, setRanges] = useState<string | null>(null);
  const [timeout, setTimeout_] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ id: string; name: string; value: string } | null>(null);
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/firewall/settings", { body: {
      internal_ranges: (ranges ?? s.data!.internal_ranges.join("\n")).split(/\s+/).filter(Boolean),
      default_timeout_s: Number(timeout ?? s.data!.default_timeout_s),
    } })),
    onSuccess: () => { setRanges(null); setTimeout_(null); void qc.invalidateQueries({ queryKey: fk.all }); },
  });
  const setGroup = useMutation({
    mutationFn: (v: { id: string; t: number }) => unwrap(api.PUT("/firewall/groups/{id}", { params: { path: { id: v.id } }, body: { rollback_timeout_s: v.t } })),
    onSuccess: () => {
      setEditing(null);
      void qc.invalidateQueries({ queryKey: fk.groups });
    },
  });
  if (!s.data) return <p className="text-sm text-muted">Loading…</p>;
  return (
    <>
    {!can("firewall.write") && <ReadOnlyNote>changing firewall settings needs the firewall.write permission (administrators).</ReadOnlyNote>}
    <div className="grid gap-4 lg:grid-cols-2">
      <Card title="Internal networks">
        <p className="mb-2 text-sm text-muted">The <strong>internet</strong> target is everything outside these ranges. Add routed LANs, VLANs and VPNs here.</p>
        <textarea className="h-48 w-full rounded-ui border border-line bg-surface p-2 font-mono text-xs" disabled={!can("firewall.write")}
          value={ranges ?? s.data.internal_ranges.join("\n")} onChange={(e) => setRanges(e.target.value)} />
        <Field label="Default rollback timeout (seconds)" hint="How long a host waits for the commit over a new connection before restoring its previous policy.">
          <Input type="number" min={30} disabled={!can("firewall.write")} value={timeout ?? String(s.data.default_timeout_s)} onChange={(e) => setTimeout_(e.target.value)} />
        </Field>
        {save.error && <div className="mt-2"><Alert>{errorMessage(save.error)}</Alert></div>}
        {can("firewall.write") && (ranges !== null || timeout !== null) && <Button className="mt-2" variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>Save</Button>}
      </Card>
      <Card title="Rollback timeout per pack">
        <p className="mb-2 text-sm text-muted">A host's own timeout wins; otherwise the shortest of its packs'; otherwise the default.</p>
        <Table
          rows={groups.data?.items ?? []}
          rowKey={(g) => g.id}
          columns={[
            { header: "Pack", cell: (g) => g.name },
            { header: "Timeout", cell: (g) => (g.rollback_timeout_s ? `${g.rollback_timeout_s}s` : <span className="text-muted">default</span>) },
            { header: "", cell: (g) => can("firewall.write") && (
              <Button size="sm" variant="ghost" aria-label={`Change the rollback timeout for ${g.name}`} onClick={() => {
                setGroup.reset();
                setEditing({ id: g.id, name: g.name, value: g.rollback_timeout_s ? String(g.rollback_timeout_s) : "" });
              }}>Change</Button>
            ) },
          ]}
        />
      </Card>
    </div>
    {editing && (
      <Dialog title={`Rollback timeout for ${editing.name}`} onClose={() => setEditing(null)} testId="pack-timeout-dialog"
        footer={
          <>
            <Button onClick={() => setEditing(null)}>Cancel</Button>
            <Button variant="primary" disabled={setGroup.isPending || (editing.value.trim() !== "" && !(Number(editing.value) > 0))}
              onClick={() => setGroup.mutate({ id: editing.id, t: editing.value.trim() ? Number(editing.value) : 0 })}>
              {setGroup.isPending ? "Saving…" : "Save"}
            </Button>
          </>
        }>
        <form className="space-y-3" onSubmit={(e) => {
          e.preventDefault();
          if (editing.value.trim() === "" || Number(editing.value) > 0) setGroup.mutate({ id: editing.id, t: editing.value.trim() ? Number(editing.value) : 0 });
        }}>
          <Field label="Seconds" hint={`Empty: the default (${s.data.default_timeout_s} seconds). A host's own timeout still wins.`}>
            <Input type="number" min={30} data-autofocus value={editing.value} onChange={(e) => setEditing({ ...editing, value: e.target.value })} />
          </Field>
          {setGroup.error && <Alert>{errorMessage(setGroup.error)}</Alert>}
        </form>
      </Dialog>
    )}
    </>
  );
}

