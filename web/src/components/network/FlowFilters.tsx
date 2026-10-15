import { useEffect, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/Button";
import { Input, Select } from "@/components/ui/Field";
import type { NetFacets, NetFilter } from "./queries";

const periods = [
  { v: "", l: "any time" }, { v: "1h", l: "last hour" }, { v: "24h", l: "last 24 hours" }, { v: "7d", l: "last 7 days" }, { v: "30d", l: "last 30 days" },
];

function Labeled({ label, children, wide }: { label: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <label className={`flex flex-col gap-0.5 text-xs ${wide ? "min-w-48" : "min-w-32"}`}>
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

/** Quick questions, as filter presets. */
export const presets: { id: string; label: string; f: NetFilter }[] = [
  { id: "external", label: "External connections", f: { scope: "external" } },
  { id: "unknown", label: "Unknown traffic", f: { class: "unknown" } },
  { id: "unclassified", label: "Not in policy", f: { policy: "unclassified" } },
  { id: "new", label: "New external destinations (24 h)", f: { new: "host", since: "24h", direction: "out" } },
  { id: "denied", label: "Denied or would be denied", f: { policy: "denied" } },
  { id: "drool", label: "DROOL's own traffic", f: { class: "drool" } },
  { id: "inbound", label: "Inbound", f: { direction: "in" } },
];

/** The filter bar. Every filter runs on the server. */
export function FlowFilters({ value, onChange, facets, fleet = true }: {
  value: NetFilter; onChange: (f: NetFilter) => void; facets?: NetFacets; fleet?: boolean;
}) {
  const [more, setMore] = useState(Boolean(value.local_port || value.remote_port || value.org || value.peer || value.source || value.first_seen_after || value.sort));
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
  return (
    <div className="space-y-2" role="search" aria-label="Filter connections">
      <div className="flex flex-wrap gap-1.5" aria-label="Quick filters">
        {presets.map((p) => {
          const active = Object.entries(p.f).every(([k, v]) => (value as Record<string, unknown>)[k] === v);
          return (
            <button key={p.id} type="button" aria-pressed={active}
              onClick={() => onChange(active ? Object.fromEntries(Object.entries(value).filter(([k]) => !(k in p.f))) as NetFilter : { ...value, ...p.f })}
              className={`rounded-full border px-2.5 py-0.5 text-xs ${active ? "border-accent bg-accent text-accent-fg" : "border-line bg-surface hover:bg-surface-2"}`}>
              {p.label}
            </button>
          );
        })}
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <Labeled label="Search" wide>
          <LazyInput id={`${id}-q`} type="search" placeholder="address, host, process, network…" value={value.q ?? ""} onCommit={(x) => set("q", x.trim())} className="h-8 text-xs" />
        </Labeled>
        {fleet && (
          <Labeled label="Host">
            <Select className="h-8 text-xs" value={value.host?.[0] ?? ""} onChange={(e) => onChange({ ...value, host: e.target.value ? [e.target.value] : undefined })}>
              <option value="">all hosts</option>
              {facets?.hosts.map((h) => <option key={h.value} value={h.value}>{h.label} ({h.count})</option>)}
            </Select>
          </Labeled>
        )}
        {fleet && (
          <Labeled label="Pack (host group)">
            <Select className="h-8 text-xs" value={value.group ?? ""} onChange={(e) => set("group", e.target.value)}>
              <option value="">any pack</option>
              {facets?.groups.map((g) => <option key={g.value} value={g.value}>{g.label}</option>)}
            </Select>
          </Labeled>
        )}
        <Labeled label="Direction">
          <Select className="h-8 text-xs" value={value.direction ?? ""} onChange={(e) => set("direction", e.target.value)}>
            <option value="">both</option><option value="in">inbound</option><option value="out">outbound</option>
          </Select>
        </Labeled>
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
        <Button size="sm" variant="ghost" aria-expanded={more} onClick={() => setMore(!more)}>{more ? "Fewer filters" : "More filters"}</Button>
        {Object.keys(value).length > 0 && <Button size="sm" variant="ghost" onClick={() => onChange(fleet ? {} : { host: value.host })}>Clear</Button>}
      </div>
      {more && (
        <div className="flex flex-wrap items-end gap-2">
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
      )}
    </div>
  );
}
