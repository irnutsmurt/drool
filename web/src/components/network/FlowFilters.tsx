import { useEffect, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/Button";
import { Input, Select } from "@/components/ui/Field";
import type { NetFacets, NetFilter } from "./queries";

const periods = [
  { v: "", l: "any time" }, { v: "1h", l: "last hour" }, { v: "24h", l: "last 24 hours" }, { v: "7d", l: "last 7 days" }, { v: "30d", l: "last 30 days" },
];

function Labeled({ label, children, wide, full }: { label: string; children: React.ReactNode; wide?: boolean; full?: boolean }) {
  return (
    <label className={`flex flex-col gap-0.5 text-xs ${wide ? "min-w-48" : "min-w-32"} ${full ? "w-full sm:w-auto" : ""}`}>
      <span className="text-muted">{label}</span>
      {children}
    </label>
  );
}

/** A text or number filter that keeps its own value while the user types
 * and applies it after a pause (or Enter / leaving the field): every
 * filter round-trips through the URL and the server, and a controlled
 * input fed back from there would drop fast keystrokes. */
function LazyInput({ value, onCommit, ...rest }: { value: string; onCommit: (v: string) => void } & Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">) {
  const [v, setV] = useState(value);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const last = useRef(value);
  useEffect(() => {
    if (value !== last.current) {
      last.current = value;
      setV(value);
    }
  }, [value]);
  const commit = (x: string) => {
    clearTimeout(timer.current);
    if (x !== last.current) {
      last.current = x;
      onCommit(x);
    }
  };
  useEffect(() => () => clearTimeout(timer.current), []);
  return (
    <Input {...rest} value={v}
      onChange={(e) => {
        const x = e.target.value;
        setV(x);
        clearTimeout(timer.current);
        timer.current = setTimeout(() => commit(x), 450);
      }}
      onBlur={() => commit(v)}
      onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); commit(v); } }} />
  );
}

/** Quick questions, as filter presets: the common ones next to the
 * search, the rest with the other filters. */
export const presets: { id: string; label: string; f: NetFilter; common?: boolean }[] = [
  { id: "external", label: "External", f: { scope: "external" }, common: true },
  { id: "unknown", label: "Unknown traffic", f: { class: "unknown" }, common: true },
  { id: "unclassified", label: "Not in policy", f: { policy: "unclassified" }, common: true },
  { id: "new", label: "New external destinations (24 h)", f: { new: "host", since: "24h", direction: "out" }, common: true },
  { id: "denied", label: "Denied or would be denied", f: { policy: "denied" } },
  { id: "drool", label: "DROOL's own traffic", f: { class: "drool" } },
  { id: "inbound", label: "Inbound", f: { direction: "in" } },
];

const isOn = (value: NetFilter, f: NetFilter) => Object.entries(f).every(([k, v]) => (value as Record<string, unknown>)[k] === v);

/** What each filter is called, for the summary of the ones in effect. */
const names: Partial<Record<keyof NetFilter, string>> = {
  group: "Pack", scope: "Remote address", protocol: "Protocol", port: "Port", local_port: "Local port", remote_port: "Remote port", process: "Process",
  service: "Service", country: "Country", asn: "Network", org: "Organization", peer: "Address", policy: "Policy status", class: "What it is",
  source: "Observed by", first_seen_after: "First seen", last_seen_after: "Last seen", new: "New destinations", sort: "Sort",
};

/** The filters in effect outside the search row (shown as removable chips
 * when "More filters" is closed, so a filter is never in effect unseen). */
export function advancedFilters(value: NetFilter): (keyof NetFilter)[] {
  const common = presets.filter((p) => p.common).filter((p) => isOn(value, p.f)).flatMap((p) => Object.keys(p.f));
  return (Object.keys(value) as (keyof NetFilter)[]).filter((k) => !["q", "host", "direction", "cursor", "limit", "since"].includes(k) && !common.includes(k));
}

/** The filter bar. Every filter runs on the server. The search, the host,
 * the direction and four common questions are always there; the other
 * filters are under "More filters", which says how many are in effect. */
export function FlowFilters({ value, onChange, facets, fleet = true }: {
  value: NetFilter; onChange: (f: NetFilter) => void; facets?: NetFacets; fleet?: boolean;
}) {
  const advanced = advancedFilters(value);
  const [more, setMore] = useState(false);
  const id = useId();
  // The latest filter, for text fields that apply after a pause: a select
  // changed in the meantime must not be undone.
  const latest = useRef(value);
  latest.current = value;
  const set = (k: keyof NetFilter, v: string | number | undefined) => {
    const next: NetFilter = { ...latest.current };
    if (v === "" || v === undefined || (typeof v === "number" && Number.isNaN(v))) delete next[k];
    else (next as Record<string, unknown>)[k] = v;
    onChange(next);
  };
  const num = (v: string) => (v === "" ? undefined : Number(v));
  const chip = (p: (typeof presets)[number]) => {
    const active = isOn(value, p.f);
    return (
      <button key={p.id} type="button" aria-pressed={active}
        onClick={() => onChange(active ? Object.fromEntries(Object.entries(value).filter(([k]) => !(k in p.f))) as NetFilter : { ...value, ...p.f })}
        className={`rounded-full border px-2.5 py-1 text-xs ${active ? "border-accent bg-accent text-accent-fg" : "border-line bg-surface hover:bg-surface-2"}`}>
        {p.label}
      </button>
    );
  };
  const valueText = (k: keyof NetFilter) => {
    const v = value[k];
    if (k === "country") return facets?.countries.find((c) => c.value === v)?.label ?? String(v);
    if (k === "asn") return facets?.networks.find((n) => n.value === v)?.label ?? String(v);
    if (k === "group") return facets?.groups.find((g) => g.value === v)?.label ?? String(v);
    if (k === "new") return `${v === "fleet" ? "new to every host" : "first seen by the host"}, last ${value.since ?? "24h"}`;
    return String(v);
  };
  return (
    <div className="space-y-2" role="search" aria-label="Filter connections">
      <div className="flex flex-wrap items-end gap-2">
        <Labeled label="Search" wide full>
          <LazyInput id={`${id}-q`} type="search" placeholder="address, host, process, network…" value={value.q ?? ""} onCommit={(x) => set("q", x.trim())} className="h-8 text-xs md:w-72" />
        </Labeled>
        {fleet && (
          <Labeled label="Host">
            <Select className="h-8 text-xs" value={value.host?.[0] ?? ""} onChange={(e) => onChange({ ...value, host: e.target.value ? [e.target.value] : undefined })}>
              <option value="">all hosts</option>
              {facets?.hosts.map((h) => <option key={h.value} value={h.value}>{h.label} ({h.count})</option>)}
            </Select>
          </Labeled>
        )}
        <Labeled label="Direction">
          <Select className="h-8 text-xs" value={value.direction ?? ""} onChange={(e) => set("direction", e.target.value)}>
            <option value="">both</option><option value="in">inbound</option><option value="out">outbound</option>
          </Select>
        </Labeled>
        <div className="flex flex-wrap items-center gap-1.5 pb-0.5" role="group" aria-label="Quick filters">
          {presets.filter((p) => p.common).map(chip)}
        </div>
        <span className="flex items-center gap-1 pb-0.5">
          <Button size="sm" variant="ghost" aria-expanded={more} aria-controls={`${id}-more`} onClick={() => setMore(!more)}>
            {more ? "Fewer filters" : `More filters${advanced.length ? ` (${advanced.length} in effect)` : ""}`}
          </Button>
          {Object.keys(value).length > 0 && <Button size="sm" variant="ghost" onClick={() => onChange(fleet ? {} : { host: value.host })}>Clear filters</Button>}
        </span>
      </div>
      {!more && advanced.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label="Other filters in effect">
          {advanced.map((k) => (
            <li key={k}>
              <button type="button" onClick={() => onChange(Object.fromEntries(Object.entries(value).filter(([x]) => x !== k && !(k === "new" && x === "since"))) as NetFilter)}
                className="inline-flex items-center gap-1 rounded-full border border-line bg-surface-2 px-2 py-0.5 text-xs hover:border-accent"
                aria-label={`Remove the filter ${names[k] ?? k}: ${valueText(k)}`}>
                <span className="text-muted">{names[k] ?? k}:</span> {valueText(k)} <span aria-hidden="true">✕</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {more && (
        <div id={`${id}-more`} className="space-y-3 rounded-ui border border-line bg-surface-2/40 p-3">
          <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="More quick filters">
            {presets.filter((p) => !p.common).map(chip)}
          </div>
          <div className="flex flex-wrap items-end gap-2">
            {fleet && (
              <Labeled label="Pack (host group)">
                <Select className="h-8 text-xs" value={value.group ?? ""} onChange={(e) => set("group", e.target.value)}>
                  <option value="">any pack</option>
                  {facets?.groups.map((g) => <option key={g.value} value={g.value}>{g.label}</option>)}
                </Select>
              </Labeled>
            )}
            <Labeled label="Remote address">
              <Select className="h-8 text-xs" value={value.scope ?? ""} onChange={(e) => set("scope", e.target.value)}>
                <option value="">internal and external</option><option value="internal">internal only</option><option value="external">external only</option><option value="special">special (link-local, …)</option>
              </Select>
            </Labeled>
            <Labeled label="Protocol">
              <Select className="h-8 text-xs" value={value.protocol ?? ""} onChange={(e) => set("protocol", e.target.value)}>
                <option value="">TCP and UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option>
              </Select>
            </Labeled>
            <Labeled label="Port">
              <LazyInput className="h-8 !w-24 text-xs" type="number" min={1} max={65535} inputMode="numeric" value={value.port ? String(value.port) : ""} onCommit={(x) => set("port", num(x))} />
            </Labeled>
            <Labeled label="Process">
              <Select className="h-8 text-xs" value={value.process ?? ""} onChange={(e) => set("process", e.target.value)}>
                <option value="">any process</option>
                {facets?.processes.map((p) => <option key={p.value} value={p.value}>{p.label} ({p.count})</option>)}
              </Select>
            </Labeled>
            <Labeled label="Service">
              <Select className="h-8 text-xs" value={value.service ?? ""} onChange={(e) => set("service", e.target.value)}>
                <option value="">any service</option>
                {facets?.services.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
              </Select>
            </Labeled>
            <Labeled label="Country">
              <Select className="h-8 text-xs" value={value.country ?? ""} onChange={(e) => set("country", e.target.value)}>
                <option value="">any country</option>
                {facets?.countries.map((c) => <option key={c.value} value={c.value}>{c.label} ({c.value})</option>)}
              </Select>
            </Labeled>
            <Labeled label="Network (ASN)" wide>
              <Select className="h-8 text-xs" value={value.asn ?? ""} onChange={(e) => set("asn", e.target.value)}>
                <option value="">any network</option>
                {facets?.networks.map((n) => <option key={n.value} value={n.value}>{n.label}</option>)}
              </Select>
            </Labeled>
            <Labeled label="What it is" wide>
              <Select className="h-8 text-xs" value={value.class ?? ""} onChange={(e) => set("class", e.target.value)}>
                <option value="">anything</option>
                <option value="drool">⛨ DROOL management (DROOL&apos;s own traffic)</option>
                <option value="approved">✓ classified as expected</option>
                <option value="user">◆ classified</option>
                <option value="service">≡ known service (by its port)</option>
                <option value="unknown">? unknown</option>
                <option value="classified">anything classified (not unknown)</option>
              </Select>
            </Labeled>
            <Labeled label="Policy status" wide>
              <Select className="h-8 text-xs" value={value.policy ?? ""} onChange={(e) => set("policy", e.target.value)}>
                <option value="">any status</option>
                <option value="covered">✓ covered by policy</option>
                <option value="management">⛨ protected management path</option>
                <option value="unclassified">? not in policy</option>
                <option value="denied">✕ denied or would be denied</option>
              </Select>
            </Labeled>
            <Labeled label="Last seen">
              <Select className="h-8 text-xs" value={value.last_seen_after ?? ""} onChange={(e) => set("last_seen_after", e.target.value)}>
                {periods.map((p) => <option key={p.v} value={p.v}>{p.l}</option>)}
              </Select>
            </Labeled>
            <Labeled label="New external destinations" wide>
              <Select className="h-8 text-xs" value={value.new ? `${value.new}|${value.since ?? "24h"}` : ""} onChange={(e) => {
                const [n, s] = e.target.value.split("|");
                const next = { ...value };
                delete next.new; delete next.since;
                onChange(n ? { ...next, new: n as NetFilter["new"], since: s } : next);
              }}>
                <option value="">show all</option>
                <option value="host|24h">first seen by the host, last 24 hours</option>
                <option value="host|7d">first seen by the host, last 7 days</option>
                <option value="fleet|24h">new to every host, last 24 hours</option>
                <option value="fleet|7d">new to every host, last 7 days</option>
              </Select>
            </Labeled>
            <Labeled label="Local port (inbound service)">
              <LazyInput className="h-8 !w-28 text-xs" type="number" min={1} max={65535} value={value.local_port ? String(value.local_port) : ""} onCommit={(x) => set("local_port", num(x))} />
            </Labeled>
            <Labeled label="Remote port (outbound service)">
              <LazyInput className="h-8 !w-28 text-xs" type="number" min={1} max={65535} value={value.remote_port ? String(value.remote_port) : ""} onCommit={(x) => set("remote_port", num(x))} />
            </Labeled>
            <Labeled label="Remote address or CIDR" wide>
              <LazyInput className="h-8 text-xs" placeholder="142.250.0.0/15" value={value.peer ?? ""} onCommit={(x) => set("peer", x.trim())} />
            </Labeled>
            <Labeled label="Network organization" wide>
              <LazyInput className="h-8 text-xs" placeholder="Google" value={value.org ?? ""} onCommit={(x) => set("org", x)} />
            </Labeled>
            <Labeled label="Observed by">
              <Select className="h-8 text-xs" value={value.source ?? ""} onChange={(e) => set("source", e.target.value)}>
                <option value="">any collection</option><option value="conntrack">continuous: conntrack</option><option value="sockets">continuous: socket sampling</option><option value="snapshot">snapshot</option>
              </Select>
            </Labeled>
            <Labeled label="First seen">
              <Select className="h-8 text-xs" value={value.first_seen_after ?? ""} onChange={(e) => set("first_seen_after", e.target.value)}>
                {periods.map((p) => <option key={p.v} value={p.v}>{p.l}</option>)}
              </Select>
            </Labeled>
            <Labeled label="Sort by">
              <Select className="h-8 text-xs" value={value.sort ?? ""} onChange={(e) => set("sort", e.target.value)}>
                <option value="">last seen</option><option value="first_seen">first seen</option><option value="count">connections</option>
              </Select>
            </Labeled>
          </div>
        </div>
      )}
    </div>
  );
}
