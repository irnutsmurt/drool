import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useGroups, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { ReachabilityBadge, osLabel } from "@/components/hosts/health";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button, primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { ManagementBadge } from "@/components/agents/ManagementBadge";
import { useFleet } from "@/components/monitoring/queries";
import { HealthBadge } from "@/components/monitoring/Status";
import { errorMessage } from "@/lib/format";
import { emptyText } from "@/components/ui/States";

export function HostsPage() {
  const { can } = useAuth();
  const hosts = useHosts();
  const groups = useGroups();
  // Health is monitoring's verdict (as on the Monitoring page), never the
  // reachability of the last status check; DROOL's connection is its own
  // column (Unreachable there), the last check is in each host's header.
  const fleet = useFleet({ category: "all", sort: "name", limit: 500 }, can("monitoring.read"));
  const health = new Map((fleet.data?.rows ?? []).map((r) => [r.host_id, r]));
  // The search box is in the address (?q=), so the navigation search's
  // "More hosts…" and a shared link open the same list.
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const setQ = (v: string) => setParams((p) => {
    if (v) p.set("q", v);
    else p.delete("q");
    return p;
  }, { replace: true });
  const [status, setStatus] = useState("");
  const [group, setGroup] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const navigate = useNavigate();
  const togglePick = (id: string) => setPicked((p) => {
    const n = new Set(p);
    if (n.has(id)) n.delete(id); else n.add(id);
    return n;
  });
  const rows = (hosts.data?.items ?? []).filter((h) => {
    const text = `${h.name} ${h.address} ${h.description} ${h.tags.join(" ")} ${osLabel(h)}`.toLowerCase();
    // Every word must appear, as in the navigation search.
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    return words.every((w) => text.includes(w)) && (!status || h.status === status) && (!group || h.group_ids.includes(group));
  });

  const addButton = can("hosts.write") && (
    <Link to="/hosts/new" className={primaryLink}>Add host</Link>
  );
  const header = <PageHeader title="Hosts" description="The Linux machines DROOL manages, over SSH or through a Shepherd (DROOL agent)." actions={addButton} />;

  if (hosts.data && hosts.data.items.length === 0) {
    return (
      <>
        {header}
        <EmptyState title="No hosts yet"
          body="Hosts are the Linux machines DROOL manages. Add one over SSH to see its health and updates, run commands on it and keep it the way you want; you can install a Shepherd (the DROOL agent) on it later for continuous monitoring."
          action={addButton || <span className="text-sm text-muted">Ask an administrator to add hosts.</span>} />
      </>
    );
  }

  return (
    <>
      {header}
      <div className="mb-4 flex flex-wrap gap-3">
        <Input className="max-w-xs" aria-label="Search hosts" placeholder="Search name, address, tag…" value={q} onChange={(e) => setQ(e.target.value)} />
        <Select className="max-w-[14rem]" aria-label="Connection" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">Any connection status</option>
          <option value="ready">Ready</option>
          <option value="pending_hostkey">Needs host key</option>
          <option value="untested">Untested</option>
          <option value="unreachable">Unreachable</option>
          <option value="hostkey_mismatch">Host key changed</option>
          <option value="auth_failed">Access denied</option>
          <option value="unsupported">Unsupported</option>
        </Select>
        <Select className="max-w-[12rem]" aria-label="Pack" value={group} onChange={(e) => setGroup(e.target.value)}>
          <option value="">All packs</option>
          {(groups.data?.items ?? []).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
        </Select>
      </div>
      {hosts.error && <Alert>{errorMessage(hosts.error)}</Alert>}
      {can("jobs.execute") && picked.size > 0 && (
        <div className="mb-3 flex items-center gap-3 text-sm">
          <span>{picked.size} selected</span>
          <Button size="sm" variant="primary" onClick={() => navigate(`/jobs/new?hosts=${[...picked].join(",")}`)}>
            Run command on selected
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())}>Clear</Button>
        </div>
      )}
      <Card>
        <Table<Host>
          rows={rows}
          rowKey={(h) => h.id}
          empty={emptyText("hosts", hosts, "No hosts match.", true)}
          columns={[
            ...(can("jobs.execute") ? [{
              header: "",
              className: "w-8 px-3 py-2",
              cell: (h: Host) => !h.jump_only && (
                <label className="inline-flex items-center gap-2">
                  <input type="checkbox" aria-label={`Select ${h.name}`} className="accent-[var(--ui-accent)]"
                    checked={picked.has(h.id)} onChange={() => togglePick(h.id)} />
                  <span aria-hidden="true" className="rt-card-only text-xs text-muted">Select</span>
                </label>
              ),
            }] : []),
            { header: "Name", cell: (h) => <Link className="font-medium text-accent" to={`/hosts/${h.id}`}>{h.name}</Link> },
            { header: "Address", cell: (h) => <span className="font-mono text-xs">{h.address}{h.port !== 22 && `:${h.port}`}</span> },
            { header: "OS", cell: (h) => <span className="text-muted">{osLabel(h)}</span> },
            // Without monitoring.read there is no health to show: the last
            // status check instead, under its own name.
            can("monitoring.read") ? {
              header: "Health",
              cell: (h: Host) => {
                if (h.jump_only) return <Badge>jump host</Badge>;
                const r = health.get(h.id);
                return r ? <HealthBadge hasData={r.source !== ""} stale={r.stale} issues={r.issues} worst={r.worst} /> : <span className="text-muted">—</span>;
              },
            } : { header: "Last check", cell: (h: Host) => (h.jump_only ? <Badge>jump host</Badge> : <ReachabilityBadge host={h} />) },
            { header: "Managed via", cell: (h) => (h.jump_only ? "" : <ManagementBadge m={h.management} />) },
            { header: "Connection", cell: (h) => <HostStatusBadge status={h.status} /> },
            {
              header: "Updates",
              cell: (h) => {
                const u = h.health?.updates_available;
                if (u == null) return <span className="text-muted">—</span>;
                const sec = h.health?.security_updates;
                return <span>{u}{sec ? <span className="text-warn"> ({sec} security)</span> : null}</span>;
              },
            },
            { header: "Reboot", cell: (h) => (h.health?.reboot_required ? <Badge tone="warn">required</Badge> : "") },
            { header: "Tags", cell: (h) => <div className="flex flex-wrap gap-1">{h.tags.map((t) => <Badge key={t}>{t}</Badge>)}</div> },
          ]}
        />
      </Card>
    </>
  );
}
