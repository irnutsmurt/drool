import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useGroups, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { ReachabilityBadge, osLabel } from "@/components/hosts/health";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { ManagementBadge } from "@/components/agents/ManagementBadge";
import { errorMessage } from "@/lib/format";

export function HostsPage() {
  const { can } = useAuth();
  const hosts = useHosts();
  const groups = useGroups();
  const [q, setQ] = useState("");
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
    return (!q || text.includes(q.toLowerCase())) && (!status || h.status === status) && (!group || h.group_ids.includes(group));
  });

  const addButton = can("hosts.write") && (
    <Link to="/hosts/new" className="inline-flex h-9 items-center rounded-ui bg-accent px-3.5 text-sm font-medium text-accent-fg hover:opacity-90">
      Add host
    </Link>
  );

  if (hosts.data && hosts.data.items.length === 0) {
    return (
      <>
        <PageHeader title="Hosts" />
        <EmptyState title="No hosts yet" body="Add your first Linux host to manage it over SSH." action={addButton} />
      </>
    );
  }

  return (
    <>
      <PageHeader title="Hosts" actions={addButton} />
      <div className="mb-4 flex flex-wrap gap-3">
        <Input className="max-w-xs" placeholder="Search name, address, tag…" value={q} onChange={(e) => setQ(e.target.value)} />
        <Select className="max-w-[12rem]" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">All statuses</option>
          <option value="ready">Ready</option>
          <option value="pending_hostkey">Needs host key</option>
          <option value="untested">Untested</option>
          <option value="unreachable">Unreachable</option>
          <option value="hostkey_mismatch">Host key changed</option>
          <option value="auth_failed">Access denied</option>
          <option value="unsupported">Unsupported</option>
        </Select>
        <Select className="max-w-[12rem]" value={group} onChange={(e) => setGroup(e.target.value)}>
          <option value="">All groups</option>
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
          empty={hosts.isPending ? "Loading…" : "No hosts match."}
          columns={[
            ...(can("jobs.execute") ? [{
              header: "",
              className: "w-8 px-3 py-2",
              cell: (h: Host) => !h.jump_only && (
                <input type="checkbox" aria-label={`Select ${h.name}`} className="accent-[var(--ui-accent)]"
                  checked={picked.has(h.id)} onChange={() => togglePick(h.id)} />
              ),
            }] : []),
            { header: "Name", cell: (h) => <Link className="font-medium text-accent" to={`/hosts/${h.id}`}>{h.name}</Link> },
            { header: "Address", cell: (h) => <span className="font-mono text-xs">{h.address}{h.port !== 22 && `:${h.port}`}</span> },
            { header: "OS", cell: (h) => <span className="text-muted">{osLabel(h)}</span> },
            { header: "Health", cell: (h) => (h.jump_only ? <Badge>jump host</Badge> : <ReachabilityBadge host={h} />) },
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
