import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Field, Select, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatBytes, formatDateTime } from "@/lib/format";
import { FlowFilters } from "@/components/network/FlowFilters";
import { FlowTable } from "@/components/network/FlowTable";
import {
  filterFromParams, filterToParams, nk, useNetFacets, useNetSettings, useNetSummary, type NetFilter, type NetSettings,
} from "@/components/network/queries";

const tabs = [
  { id: "activity", label: "Connections" },
  { id: "settings", label: "Collection and enrichment" },
] as const;

/** Fleet-wide Network Activity: what hosts actually talk to. */
export function NetworkPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "settings" ? "settings" : "activity";
  const setTab = (t: string) => {
    const next = new URLSearchParams(params);
    next.set("tab", t);
    setParams(next, { replace: true });
  };
  return (
    <>
      <PageHeader title="Network activity"
        description="Connections DROOL has observed on your hosts, how the firewall policy classifies them, and where public addresses belong. Monitoring only: nothing here blocks traffic." />
      <div role="tablist" aria-label="Network views" className="mb-4 flex gap-1 border-b border-line">
        {tabs.map((t) => (
          <button key={t.id} role="tab" aria-selected={tab === t.id} onClick={() => setTab(t.id)}
            className={cx("-mb-px border-b-2 px-3 py-2 text-sm", tab === t.id ? "border-accent font-medium" : "border-transparent text-muted hover:text-fg")}>
            {t.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" aria-label={tabs.find((t) => t.id === tab)!.label}>
        {tab === "activity" ? <Activity /> : <SettingsTab />}
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
      {s && (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4" data-testid="network-summary">
          <Stat label="Connections (flows)" value={s.counts.flows} detail={`${s.counts.inbound} inbound · ${s.counts.outbound} outbound`} />
          <Stat label="External" value={s.counts.external} detail={`${s.counts.new_external_24h} new destination(s) in the last 24 hours`}
            onClick={() => setFilter({ new: "host", since: "24h", direction: "out" })} action="Show new destinations" />
          <Stat label="Unclassified" value={s.counts.unclassified} detail="no firewall policy describes them" tone={s.counts.unclassified ? "warn" : undefined}
            onClick={() => setFilter({ policy: "unclassified" })} action="Show unclassified" />
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
      No connections observed yet. Agent hosts report continuously; for SSH hosts, take a snapshot from the host&apos;s page
      (Network connections → Take snapshot).
    </span>
  );
}

function Stat({ label, value, detail, tone, onClick, action }: {
  label: string; value: number; detail: string; tone?: "warn" | "danger"; onClick?: () => void; action?: string;
}) {
  return (
    <div className="rounded-ui border border-line bg-surface p-3">
      <div className="text-xs text-muted">{label}</div>
      <div className={cx("text-2xl font-semibold tabular-nums", tone === "warn" && "text-warn", tone === "danger" && "text-danger")}>{value.toLocaleString()}</div>
      <div className="text-xs text-muted">{detail}</div>
      {onClick && value > 0 && <button type="button" className="mt-1 text-xs text-accent underline" onClick={onClick}>{action}</button>}
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
        {!s.enrichment && <> Country and network data are <strong>not available</strong>: no GeoIP/ASN database is installed (see Collection and enrichment).</>}
      </p>
    </div>
  );
}

function SettingsTab() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const q = useNetSettings();
  const canEdit = can("network.settings");
  const [form, setForm] = useState<NetSettings | null>(null);
  useEffect(() => {
    if (q.data && !form) setForm({ collection: q.data.collection, retention_days: q.data.retention_days, max_flows_per_host: q.data.max_flows_per_host, enrichment: q.data.enrichment });
  }, [q.data, form]);
  const save = useMutation({
    mutationFn: (b: NetSettings) => unwrap(api.PUT("/network/settings", { body: b })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
  const reload = useMutation({
    mutationFn: () => unwrap(api.POST("/network/enrichment/reload")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!q.data || !form) return <p className="text-sm text-muted">Loading…</p>;
  const st = reload.data ?? q.data.enrichment_status;
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <Card title="Collection">
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save.mutate(form); }}>
          <Field label="Continuous collection on agent hosts" hint="SSH-only hosts are never collected continuously; take snapshots from their pages.">
            <Select value={form.collection} disabled={!canEdit} onChange={(e) => setForm({ ...form, collection: e.target.value as NetSettings["collection"] })}>
              <option value="all">Every agent host</option>
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
            Show country and network owner for public addresses (local databases only)
          </label>
          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          {save.isSuccess && <Alert tone="ok">Saved.</Alert>}
          {canEdit ? <Button variant="primary" type="submit" disabled={save.isPending}>Save</Button>
            : <p className="text-xs text-muted">Only administrators can change these settings.</p>}
          {q.data && <p className="text-xs text-muted">Changes are recorded in the audit log.</p>}
        </form>
      </Card>
      <Card title="GeoIP and ASN enrichment" actions={canEdit ? <Button size="sm" onClick={() => reload.mutate()} disabled={reload.isPending}>Reload databases</Button> : undefined}>
        <div className="space-y-3 text-sm" data-testid="enrichment-status">
          <p>
            Country: {st.country ? <Badge tone="ok">✓ available</Badge> : <Badge tone="warn">not available</Badge>}{" "}
            Network (ASN): {st.asn ? <Badge tone="ok">✓ available</Badge> : <Badge tone="warn">not available</Badge>}
          </p>
          <p className="text-xs text-muted">
            DROOL looks public addresses up in MaxMind-format (<code>.mmdb</code>) files in <code className="break-all">{st.directory}</code>. It never sends
            addresses to an online service, and never looks up private, loopback, link-local, multicast or documentation addresses. Files are reloaded
            automatically when they change.
          </p>
          {st.files.length === 0 ? (
            <div className="rounded-ui border border-line bg-surface-2 p-3 text-xs">
              <p className="font-medium">No databases installed — monitoring works without them; country and network show as “Unknown”.</p>
              <p className="mt-1">To enable enrichment, put country and ASN databases in the directory above, for example:</p>
              <ul className="mt-1 list-disc space-y-0.5 pl-5">
                <li>MaxMind GeoLite2 Country and ASN (free account; keep them current with <code>geoipupdate</code>)</li>
                <li>DB-IP Lite country and ASN (monthly downloads, CC BY 4.0)</li>
                <li>IPinfo Lite (country and ASN in one file)</li>
              </ul>
              {st.error && <p className="mt-1 text-muted">({st.error})</p>}
            </div>
          ) : (
            <ul className="space-y-1 text-xs">
              {st.files.map((f) => (
                <li key={f.name} className="rounded-ui border border-line p-2">
                  <span className="font-mono">{f.name}</span> · {formatBytes(f.size)} · {f.database_type || "unknown type"}{f.build_date && <> · built {f.build_date}</>}
                  <div className="text-muted">provides: {f.provides.join(", ") || "nothing recognised"}{f.used.length > 0 && <> · used for {f.used.join(", ")}</>}</div>
                  {f.error && <div className="text-danger">✕ {f.error}</div>}
                </li>
              ))}
            </ul>
          )}
          <p className="text-xs text-muted">
            Loaded {formatDateTime(st.loaded_at)} · cache {st.cache.entries.toLocaleString()} / {st.cache.max.toLocaleString()} addresses
            ({st.cache.hits.toLocaleString()} hits, {st.cache.misses.toLocaleString()} lookups).
          </p>
          <p className="text-xs text-muted">GeoIP and ASN data are context, not a security verdict: they can be wrong or out of date, and DROOL never blocks or scores traffic by country or network.</p>
        </div>
      </Card>
    </div>
  );
}
