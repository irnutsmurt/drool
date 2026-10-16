import { useEffect, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button, secondaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Field, Select, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatDateTime } from "@/lib/format";
import { FlowFilters } from "@/components/network/FlowFilters";
import { FlowTable } from "@/components/network/FlowTable";
import { ClassifyDialog } from "@/components/network/ClassifyDialog";
import { EnrichmentCard } from "@/components/network/Enrichment";
import {
  filterFromParams, filterToParams, nk, useNetFacets, useNetRules, useNetSettings, useNetSummary, type NetEnrichState, type NetFilter, type NetRule, type NetSettings,
} from "@/components/network/queries";
import { plainLink } from "@/components/ui/links";
import { Dialog } from "@/components/ui/Dialog";

const tabs = [
  { id: "activity", label: "Connections" },
  { id: "classifications", label: "Classifications" },
  { id: "settings", label: "Collection and enrichment" },
] as const;

/** Fleet-wide Network Activity: what hosts actually talk to. */
export function NetworkPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = tabs.find((t) => t.id === params.get("tab"))?.id ?? "activity";
  const setTab = (t: string) => {
    const next = new URLSearchParams(params);
    next.set("tab", t);
    setParams(next, { replace: true });
  };
  return (
    <>
      <PageHeader title="Network Activity"
        description="What your hosts are talking to: connections DROOL has observed, what they are, what the firewall policy says about them, and where public addresses belong. Monitoring only: nothing here blocks traffic."
        actions={can("firewall.read") && (
          <>
            <Link to="/firewall?tab=designer" className={secondaryLink}>Firewall policy</Link>
            <Link to="/firewall?tab=simulate" className={secondaryLink}>Can A reach B?</Link>
          </>
        )} />
      <div role="tablist" aria-label="Network views" className="mb-4 flex gap-1 overflow-x-auto border-b border-line">
        {tabs.map((t) => (
          <button key={t.id} role="tab" aria-selected={tab === t.id} onClick={() => setTab(t.id)}
            className={cx("-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm", tab === t.id ? "border-accent font-medium" : "border-transparent text-muted hover:text-fg")}>
            {t.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" aria-label={tabs.find((t) => t.id === tab)!.label}>
        {tab === "activity" ? <Activity /> : tab === "classifications" ? <ClassificationsTab /> : <SettingsTab />}
      </div>
    </>
  );
}

function Activity() {
  const [params, setParams] = useSearchParams();
  const filter = filterFromParams(params);
  const setFilter = (f: NetFilter) => setParams(filterToParams(f, params), { replace: true });
  const sum = useNetSummary(filter);
  const facets = useNetFacets();
  const s = sum.data;
  return (
    <div className="space-y-4">
      {s && <EnrichmentNote state={s.enrichment_state} />}
      {s && (
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4" data-testid="network-summary">
          <Stat label="Connections (flows)" value={s.counts.flows} detail={`${s.counts.inbound} inbound · ${s.counts.outbound} outbound`} />
          <Stat label="External" value={s.counts.external} detail={`${s.counts.new_external_24h} new destination(s) in the last 24 hours`}
            onClick={() => setFilter({ new: "host", since: "24h", direction: "out" })} action="Show new destinations" />
          <Stat label="Not in policy" value={s.counts.unclassified} detail="no firewall policy describes them" tone={s.counts.unclassified ? "warn" : undefined}
            onClick={() => setFilter({ policy: "unclassified" })} action="Show them" />
          <Stat label="Denied or would be denied" value={s.counts.denied} detail="by a deny policy or an enforced default" tone={s.counts.denied ? "danger" : undefined}
            onClick={() => setFilter({ policy: "denied" })} action="Show" />
        </div>
      )}
      {s && <Fidelity s={s} />}
      <Card>
        <FlowFilters value={filter} onChange={setFilter} facets={facets.data} />
        <div className="mt-3">
          <FlowTable filter={filter} empty={Object.keys(filter).length ? "No connections match these filters." : <NoData />} />
        </div>
      </Card>
    </div>
  );
}

function NoData() {
  return (
    <span>
      No connections observed yet. Hosts with a Shepherd report continuously; for SSH hosts, take a snapshot on the host&apos;s
      Network Activity page (Take snapshot).
    </span>
  );
}

function Stat({ label, value, detail, tone, onClick, action }: {
  label: string; value: number; detail: string; tone?: "warn" | "danger"; onClick?: () => void; action?: string;
}) {
  return (
    <div className="rounded-ui border border-line bg-surface px-3 py-2">
      <div className="flex items-baseline gap-2">
        <span className={cx("text-xl font-semibold tabular-nums", tone === "warn" && "text-warn", tone === "danger" && "text-danger")}>{value.toLocaleString()}</span>
        <span className="text-sm">{label}</span>
      </div>
      <div className="text-xs text-muted">{detail}{onClick && value > 0 && <> · <button type="button" className="text-accent underline" onClick={onClick}>{action}</button></>}</div>
    </div>
  );
}

function Fidelity({ s }: { s: NonNullable<ReturnType<typeof useNetSummary>["data"]> }) {
  const h = s.hosts;
  return (
    <div className="rounded-ui border border-line bg-surface px-3 py-2 text-xs" data-testid="network-fidelity">
      <span className="font-medium">How hosts are observed:</span>{" "}
      <span className="inline-flex flex-wrap gap-1.5 align-middle">
        <Badge tone="ok">Continuous: conntrack · {h.conntrack}</Badge>
        <Badge tone="accent">Continuous: socket sampling · {h.sockets}</Badge>
        <Badge>Snapshot only: SSH · {h.snapshot}</Badge>
        {h.no_snapshot > 0 && <Badge tone="warn">SSH, no snapshot yet · {h.no_snapshot}</Badge>}
        {h.off > 0 && <Badge tone="warn">Not collecting · {h.off}</Badge>}
      </span>
      <p className="mt-1 text-muted">
        conntrack sees every new connection; socket sampling (every 30 s) misses connections that open and close in between; SSH snapshots show only what is open at that moment.
      </p>
    </div>
  );
}

/** One explanation, at the top, when country or network-owner data isn't
 * looked up at all (lookups off, or no database of that kind), so the rows
 * can show "—" instead of an "unknown" that would be untrue: with a
 * database loaded, "unknown" means the database has no answer. */
export function EnrichmentNote({ state }: { state: NetEnrichState }) {
  const link = <Link className={plainLink} to="/network?tab=settings">Configure enrichment →</Link>;
  const settings = <Link className={plainLink} to="/network?tab=settings">Collection and enrichment →</Link>;
  let text: ReactNode = null;
  const preparing = state.country === "preparing" || state.asn === "preparing";
  const missing = state.country === "unavailable" || state.asn === "unavailable";
  if (state.country === "off" && state.asn === "off") {
    text = <>Country and network-owner lookups are turned off. {settings}</>;
  } else if (preparing && !missing) {
    // The first download, not a problem to solve: no warning.
    return (
      <p className="px-1 text-xs text-muted" role="status" data-testid="enrichment-note" data-state="preparing">
        Preparing country and network-owner data: DROOL is downloading its databases for the first time. They appear here when ready.
      </p>
    );
  } else if (missing && state.downloads) {
    const which = state.country === "unavailable" && state.asn === "unavailable" ? "Country and network-owner information is"
      : state.country === "unavailable" ? "Country information is" : "Network-owner information is";
    text = <>{which} temporarily unavailable: DROOL couldn&apos;t download its database{state.download_error ? <> ({state.download_error})</> : null}. It tries again automatically. {settings}</>;
  } else if (state.country === "unavailable" && state.asn === "unavailable") {
    text = <>Country and network-owner information is unavailable because GeoIP/ASN databases are not configured. {link}</>;
  } else if (state.country === "unavailable") {
    text = <>Country information is unavailable because no GeoIP country database is configured; network owners are shown. {link}</>;
  } else if (state.asn === "unavailable") {
    text = <>Network-owner information is unavailable because no ASN database is configured; countries are shown. {link}</>;
  }
  if (!text) return null;
  return <p className="rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="note" data-testid="enrichment-note">{text}</p>;
}

/** The classifications: what recurring traffic is, whether it's expected. */
function ClassificationsTab() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const q = useNetRules();
  const canEdit = can("network.classify");
  const [edit, setEdit] = useState<NetRule | "new" | null>(null);
  const [del, setDel] = useState<NetRule | null>(null);
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/network/classifications/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: nk.all });
      setDel(null);
    },
  });
  const rules = q.data?.items ?? [];
  return (
    <Card title="Classifications" actions={canEdit ? <Button size="sm" variant="primary" onClick={() => setEdit("new")}>Add classification…</Button> : undefined}>
      <div className="space-y-3 text-sm" data-testid="classifications">
        <p className="text-xs text-muted">
          Say what recurring traffic is (&ldquo;NFS media storage&rdquo;) and whether it is expected. A classification matches connections by host or Pack,
          direction, protocol, port and remote side, and applies to traffic already observed and observed later; the most specific one wins. DROOL
          recognizes its own management traffic from evidence (where its SSH connections come from, where Shepherds connect to), never from a port
          alone. Labels only: nothing is blocked or allowed, and no firewall rule is created. You can also classify a connection from its details.
        </p>
        <p className="text-xs"><Link className="text-accent underline" to="/network?class=unknown">Show unknown traffic</Link></p>
        {q.error && <Alert>{errorMessage(q.error)}</Alert>}
        <div className="overflow-x-auto">
          <table className="rtable w-full text-left text-sm">
            <caption className="sr-only">Classifications</caption>
            <thead>
              <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
                <th scope="col" className="px-2 py-2 font-medium">What it is</th>
                <th scope="col" className="px-2 py-2 font-medium">Matches</th>
                <th scope="col" className="px-2 py-2 font-medium">Expected</th>
                <th scope="col" className="px-2 py-2 font-medium">Made from</th>
                <th scope="col" className="px-2 py-2 font-medium">Changed</th>
                {canEdit && <th scope="col" className="px-2 py-2"><span className="sr-only">Actions</span></th>}
              </tr>
            </thead>
            <tbody>
              {rules.length === 0 ? (
                <tr><td colSpan={canEdit ? 6 : 5} className="rt-empty px-3 py-6 text-center text-muted">{q.isLoading ? "Loading…" : "No classifications yet."}</td></tr>
              ) : rules.map((r) => (
                <tr key={r.id} className="border-b border-line align-top last:border-0" data-rule-id={r.id}>
                  <td className="px-2 py-2" data-primary=""><span className="font-medium">{r.label}</span>{r.note && <div className="text-xs text-muted">{r.note}</div>}</td>
                  <td data-label="Matches" className="px-2 py-2 text-xs">{r.described}</td>
                  <td data-label="Expected" className="px-2 py-2">{r.expected ? <Badge tone="ok"><span aria-hidden className="mr-1">✓</span>Expected</Badge> : <span className="text-xs text-muted">—</span>}</td>
                  <td data-label="Made from" className="px-2 py-2 text-xs">{r.origin === "flow" ? "one connection" : "a classification"}</td>
                  <td data-label="Changed" className="whitespace-nowrap px-2 py-2 text-xs">{formatDateTime(r.updated_at)}</td>
                  {canEdit && (
                    <td className="whitespace-nowrap px-2 py-2 text-right">
                      <Button size="sm" variant="ghost" onClick={() => setEdit(r)} aria-label={`Edit ${r.label}`}>Edit</Button>
                      <Button size="sm" variant="ghost" onClick={() => setDel(r)} aria-label={`Delete ${r.label}`}>Delete</Button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!canEdit && <p className="text-xs text-muted" role="note">Read-only: classifying traffic needs the network.classify permission (administrators and operators).</p>}
      </div>
      {edit && <ClassifyDialog rule={edit === "new" ? undefined : edit} onClose={() => setEdit(null)} />}
      {del && (
        <Dialog title={`Delete “${del.label}”?`} onClose={() => setDel(null)}>
          <div className="space-y-3 text-sm">
            <p>The traffic it matches ({del.described}) goes back to how it was classified before: another classification, its port&apos;s name, or unknown.</p>
            {remove.error && <Alert>{errorMessage(remove.error)}</Alert>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setDel(null)}>Cancel</Button>
              <Button variant="danger" disabled={remove.isPending} onClick={() => remove.mutate(del.id)}>Delete classification</Button>
            </div>
          </div>
        </Dialog>
      )}
    </Card>
  );
}

function SettingsTab() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const q = useNetSettings();
  const canEdit = can("network.settings");
  const [form, setForm] = useState<NetSettings | null>(null);
  useEffect(() => {
    if (q.data && !form) setForm({ collection: q.data.collection, retention_days: q.data.retention_days, max_flows_per_host: q.data.max_flows_per_host, enrichment: q.data.enrichment,
      geoip_downloads: q.data.geoip_downloads });
  }, [q.data, form]);
  const save = useMutation({
    mutationFn: (b: NetSettings) => unwrap(api.PUT("/network/settings", { body: b })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!q.data || !form) return <p className="text-sm text-muted">Loading…</p>;
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <Card title="Collection">
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save.mutate(form); }}>
          <Field label="Continuous collection on hosts with a Shepherd" hint="SSH-only hosts are never collected continuously; take snapshots on their Network Activity pages.">
            <Select value={form.collection} disabled={!canEdit} onChange={(e) => setForm({ ...form, collection: e.target.value as NetSettings["collection"] })}>
              <option value="all">Every host with a Shepherd</option>
              <option value="firewall">Only hosts whose firewall mode isn&apos;t Off</option>
              <option value="off">Off</option>
            </Select>
          </Field>
          <Field label="Keep observations for (days)" hint="Connections not seen for this long are removed. Pruning runs every 10 minutes in small batches.">
            <Input type="number" min={1} max={365} value={form.retention_days} disabled={!canEdit} onChange={(e) => setForm({ ...form, retention_days: Number(e.target.value) })} />
          </Field>
          <Field label="At most this many connections per host" hint="The least recently seen go first. A flow is one host, direction, protocol, local address, service port and remote address — not one row per connection.">
            <Input type="number" min={100} max={100000} value={form.max_flows_per_host} disabled={!canEdit} onChange={(e) => setForm({ ...form, max_flows_per_host: Number(e.target.value) })} />
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={form.enrichment} disabled={!canEdit} onChange={(e) => setForm({ ...form, enrichment: e.target.checked })} />
            Show country and network owner for public addresses (looked up on this controller)
          </label>
          {q.data.enrichment_status.downloads && (
            <label className="ml-6 flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-0.5" checked={!!form.geoip_downloads} disabled={!canEdit || !form.enrichment}
                onChange={(e) => setForm({ ...form, geoip_downloads: e.target.checked })} />
              <span>Download and update the databases automatically
                <span className="block text-xs text-muted">DB-IP Lite country and ASN (CC BY 4.0), checked daily. Only the database files are downloaded; your own databases are used instead where you provide them.</span>
              </span>
            </label>
          )}
          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          {save.isSuccess && <Alert tone="ok">Saved.</Alert>}
          {canEdit ? <Button variant="primary" type="submit" disabled={save.isPending}>Save</Button>
            : <p className="text-xs text-muted" role="note">Read-only: changing collection and enrichment needs the network.settings permission (administrators).</p>}
          {q.data && <p className="text-xs text-muted">Changes are recorded in the audit log.</p>}
        </form>
      </Card>
      <EnrichmentCard st={q.data.enrichment_status} canEdit={canEdit} />
    </div>
  );
}
