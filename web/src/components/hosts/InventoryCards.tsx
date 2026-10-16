import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { useHostFacts, type DiscoveredFacts, type Host } from "@/api/queries";
import { Badge } from "@/components/ui/Badge";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Field";
import { LoadError, Loading } from "@/components/ui/States";
import { Table } from "@/components/ui/Table";
import { cx } from "@/components/ui/cx";
import { formatAgo, formatBytes, formatDateTime, formatPct, formatUptime } from "@/lib/format";

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (<><dt className="text-muted">{label}</dt><dd className="min-w-0 break-words">{children}</dd></>);
}

const unknown = <span className="text-muted">unknown</span>;

/** Reachability, updates and reboot from the latest status check; CPU,
 * memory, disks and failed units too when the user can't see Monitoring
 * (which shows those in detail). */
export function StatusLines({ host }: { host: Host }) {
  const h = host.health;
  const mon = useAuth().can("monitoring.read");
  if (!h) return <p className="text-sm text-muted">Not checked yet. Status is collected automatically every few minutes.</p>;
  return (
    <div className="space-y-2">
      {h.stale && (
        <p className="text-sm text-warn">
          <span aria-hidden="true">⚠ </span>DROOL hasn't been able to check this host for a while (busy with jobs, or the host is unreachable). The
          values below are from the last successful check, {formatAgo(h.checked_at)}.
        </p>
      )}
      <dl className="grid grid-cols-[minmax(6.5rem,9rem)_1fr] gap-y-1.5 text-sm">
        <Row label="Last check">
          {formatAgo(h.checked_at)} {h.stale && <Badge tone="warn">stale</Badge>}
          {!h.reachable && <span className="ml-2 text-danger">failed: {h.last_error}</span>}
        </Row>
        <Row label="Uptime">{formatUptime(h.uptime_s)}</Row>
        {!mon && (<>
          <Row label="Load (1 min)">{h.load1 ?? "—"}</Row>
          <Row label="Memory used">{formatPct(h.mem_used_pct)}</Row>
          <Row label="Fullest disk">
            {h.max_fs_used_pct == null ? "—" : (
              <span className={cx(h.max_fs_used_pct >= 90 && "text-danger")}>{formatPct(h.max_fs_used_pct)} <span className="text-muted">{h.max_fs_mount}</span></span>
            )}
          </Row>
          <Row label="Failed units">
            {h.failed_units == null ? <span className="text-muted">n/a (systemd not running)</span>
              : h.failed_units === 0 ? "none" : <>{h.failed_unit_names.join(", ")} <span className="text-muted">(alerts only when monitored)</span></>}
          </Row>
        </>)}
        <Row label="Reboot">
          {h.reboot_required == null ? unknown : h.reboot_required ? <span className="text-warn">required{h.reboot_reason && ` (${h.reboot_reason})`}</span> : "not required"}
        </Row>
        <Row label="Updates">
          {h.updates_available == null ? <span className="text-muted">unknown (package cache not available yet)</span> : (
            <>{h.updates_available} pending{h.security_updates ? <span className="text-warn">, {h.security_updates} security</span> : h.security_updates === 0 ? ", none security" : ""}
              <span className="text-muted"> · checked {formatAgo(h.updates_checked_at)}</span></>
          )}
        </Row>
      </dl>
    </div>
  );
}

/** The host overview's system facts: the essentials, with filesystems,
 * addresses and interfaces one click away. */
export function SystemCard({ host }: { host: Host }) {
  const f = host.facts;
  const q = useHostFacts(host.id);
  const full = q.data?.facts as DiscoveredFacts | null | undefined;
  const addresses = q.data?.addresses ?? [];
  const n = (k: number, one: string, many: string) => `${k} ${k === 1 ? one : many}`;
  // Counts only once the inventory is here: loading isn't "0 filesystems".
  const summary = q.data ? `${n(full?.filesystems.length ?? 0, "filesystem", "filesystems")}, ${n(addresses.length, "address", "addresses")}`
    : q.error ? "couldn't load" : "loading…";
  return (
    <Card title="System">
      {!f ? (
        <p className="text-sm text-muted">Not discovered yet. Discovery runs automatically once the connection test passes.</p>
      ) : (
        <div className="space-y-3">
          <dl className="grid grid-cols-[minmax(6.5rem,9rem)_1fr] gap-y-1.5 text-sm">
            <Row label="OS">{f.distro_name || `${f.distro} ${f.distro_version}`} <span className="text-muted">({f.os_family})</span></Row>
            <Row label="Kernel">{f.kernel || unknown} <span className="text-muted">{f.arch}</span></Row>
            <Row label="Hostname">{f.hostname || unknown}</Row>
            <Row label="CPU">{f.cpu_count ?? "?"} × {full?.cpu_model || "CPU"}</Row>
            <Row label="Memory">{formatBytes(f.mem_bytes)}{full?.swap_bytes ? ` + ${formatBytes(full.swap_bytes)} swap` : ""}</Row>
            <Row label="Packages">{f.pkg_manager || unknown}</Row>
            <Row label="Init system">{f.init_system || unknown}</Row>
            {full?.virtualization && full.virtualization !== "none" && <Row label="Virtualization">{full.virtualization}</Row>}
            <Row label="Discovered">{formatDateTime(f.collected_at)}</Row>
          </dl>
          <details className="border-t border-line pt-3">
            <summary className="cursor-pointer text-sm font-medium">Filesystems, addresses and interfaces
              <span className="font-normal text-muted"> — {summary}</span></summary>
            {!q.data ? (
              <div className="mt-3">{q.error ? <LoadError what="the inventory" error={q.error} /> : <Loading what="the inventory" />}</div>
            ) : (
              <div className="mt-3 space-y-4">
                <p className="text-xs text-muted">Containers: {f.container_runtimes.length ? f.container_runtimes.join(", ") : "none found"}.
                  Addresses are informational: DROOL identifies hosts by their SSH key or Shepherd identity, not by IP address.</p>
                {full && full.filesystems.length > 0 && (
                  <Table caption="Filesystems" rows={full.filesystems} rowKey={(fs) => fs.mount}
                    columns={[
                      { header: "Mount", cell: (fs) => <span className="font-mono text-xs">{fs.mount}</span> },
                      { header: "Type", cell: (fs) => fs.type ?? "" },
                      { header: "Size", cell: (fs) => formatBytes(fs.size_bytes) },
                      { header: "Used", cell: (fs) => <span className={cx(fs.used_pct >= 90 && "text-danger")}>{formatPct(fs.used_pct)}</span> },
                    ]} />
                )}
                {/* Lines, not wide tables: this card is a third of the page. */}
                <section aria-label="Addresses">
                  <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">Addresses</h3>
                  {addresses.length === 0 ? <p className="text-sm text-muted">No addresses known.</p> : (
                    <ul className="divide-y divide-line text-sm">
                      {addresses.map((a) => (
                        <li key={a.address} className="flex flex-wrap items-center gap-x-2 gap-y-0.5 py-1.5">
                          <span className="font-mono text-xs [overflow-wrap:anywhere]">{a.address}</span>
                          <span className="text-muted">{a.interface || "—"}</span>
                          <Badge tone={a.source === "configured" ? "accent" : "neutral"}>{a.source}</Badge>
                          <span className="text-xs text-muted">seen {formatAgo(a.last_seen_at)}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
                {full && full.interfaces.length > 0 && (
                  <section aria-label="Interfaces">
                    <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">Interfaces</h3>
                    <ul className="divide-y divide-line text-sm">
                      {full.interfaces.map((i) => (
                        <li key={i.name} className="py-1.5">
                          <span className="flex flex-wrap items-center gap-x-2">
                            <span className="font-mono text-xs">{i.name}</span>
                            {i.state && <span className="text-muted">{i.state}</span>}
                            {i.mac && <span className="font-mono text-xs text-muted">{i.mac}</span>}
                          </span>
                          {i.addresses.length > 0 && (
                            <span className="block font-mono text-xs [overflow-wrap:anywhere]">{i.addresses.map((a) => `${a.address}/${a.prefix}`).join(", ")}</span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </section>
                )}
                {full && full.warnings.length > 0 && (
                  <div className="text-xs text-muted">
                    <div className="font-medium">Discovery notes</div>
                    <ul className="mt-1 list-disc pl-5">{full.warnings.map((w) => <li key={w}>{w}</li>)}</ul>
                  </div>
                )}
              </div>
            )}
          </details>
        </div>
      )}
    </Card>
  );
}

export function ServicesCard({ host }: { host: Host }) {
  const q = useHostFacts(host.id);
  const full = q.data?.facts as DiscoveredFacts | null | undefined;
  const [filter, setFilter] = useState("");
  const { can } = useAuth();
  if (!full) return null;
  // The service inventory, its states and history live in Monitoring (M11);
  // listening ports in Network (M10).
  if (can("monitoring.read") && can("network.read")) return null;
  const manage = can("jobs.execute") && full.systemd_running;
  const services = full.services.filter((s) => !filter || `${s.name} ${s.description ?? ""}`.toLowerCase().includes(filter.toLowerCase()));
  return (
    <Card title={can("network.read") ? "Services" : "Services and listening ports"}>
      <div className="grid gap-4 lg:grid-cols-2">
        {can("monitoring.read") ? (
          <p className="text-sm text-muted">
            Services, their states and history are under <Link className="text-accent underline" to={`/hosts/${host.id}/monitoring?tab=services`}>Monitoring → Services</Link>.
          </p>
        ) : <div className="space-y-2">
          <div className="flex items-center justify-between gap-3">
            <h3 className="text-sm font-medium">Running services ({full.services.length})</h3>
            {full.services.length > 8 && <Input className="h-8 max-w-[12rem]" placeholder="Filter…" value={filter} onChange={(e) => setFilter(e.target.value)} />}
          </div>
          {!full.systemd_running ? (
            <p className="text-sm text-muted">Unavailable: systemd is not running on this host.</p>
          ) : (
            <ul className="max-h-80 space-y-1 overflow-y-auto text-sm">
              {services.map((s) => (
                <li key={s.name} className="group flex items-baseline gap-1">
                  <span className="min-w-0">
                    <span className="font-mono text-xs">{s.name}</span>
                    {s.description && <span className="text-muted"> — {s.description}</span>}
                  </span>
                  {manage && (
                    <Link className="ml-auto shrink-0 text-xs text-accent opacity-60 group-hover:opacity-100"
                      to={`/jobs/new?hosts=${host.id}&op=service&service=${encodeURIComponent(s.name)}&action=restart`}>
                      Restart…
                    </Link>
                  )}
                </li>
              ))}
            </ul>
          )}
          {full.failed_units.length > 0 && (
            <p className="text-sm">
              Failed (a failed unit alerts only when its service is monitored):{" "}
              {full.failed_units.map((u, i) => (
                <span key={u}>
                  {i > 0 && ", "}
                  {manage ? <Link className="underline" to={`/jobs/new?hosts=${host.id}&op=service&service=${encodeURIComponent(u)}&action=restart`}>{u}</Link> : u}
                </span>
              ))}
            </p>
          )}
        </div>}
        <div className="space-y-2">
          <h3 className="text-sm font-medium">Listening ports ({full.ports.length})</h3>
          {can("network.read") ? (
            <p className="text-sm text-muted">
              Listening ports, with the connections observed arriving on them, are shown under{" "}
              <Link className="text-accent underline" to={`/hosts/${host.id}/network`}>the host's Network Activity page</Link>.
            </p>
          ) : (
            <>
              <Table
                rows={full.ports}
                rowKey={(p) => `${p.proto}|${p.address}|${p.port}`}
                empty="None reported."
                columns={[
                  { header: "Port", cell: (p) => `${p.port}/${p.proto}` },
                  { header: "Address", cell: (p) => <span className="font-mono text-xs">{p.address}</span> },
                  { header: "Process", cell: (p) => p.process ?? <span className="text-muted">—</span> },
                ]}
              />
              <p className="text-xs text-muted">Process names are shown only for processes the SSH user can see.</p>
            </>
          )}
        </div>
      </div>
    </Card>
  );
}
