import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { useBaselines, type Baseline } from "@/components/desired/queries";
import { errorMessage } from "@/lib/format";
import { DesiredTabs } from "./DesiredTabs";
import { emptyText } from "@/components/ui/States";

export function appliesText(b: Pick<Baseline, "assignments">): string {
  if (b.assignments.length === 0) return "Not assigned";
  return b.assignments.map((a) => (a.scope === "global" ? "Everywhere" : a.scope === "group" ? `pack ${a.name}` : a.name)).join(", ");
}

export function resourcesText(b: Pick<Baseline, "resources">): string {
  const n = { service: 0, package: 0, file: 0 } as Record<string, number>;
  for (const r of b.resources) n[r.kind] = (n[r.kind] ?? 0) + 1;
  const parts = [n.service && `${n.service} service${n.service === 1 ? "" : "s"}`, n.package && `${n.package} package${n.package === 1 ? "" : "s"}`,
    n.file && `${n.file} file${n.file === 1 ? "" : "s"}`].filter(Boolean);
  return parts.join(", ") || "Empty";
}

/** Baselines: named collections of typed desired resources. */
export function BaselinesPage() {
  const { can } = useAuth();
  const [archived, setArchived] = useState(false);
  const list = useBaselines(archived);
  const items = list.data?.items ?? [];
  return (
    <>
      <PageHeader title="Desired State" description="Baselines say what machines should look like: services that should run, packages that should (or shouldn't) be installed, and files that should match exactly."
        actions={can("desired.manage") && <Link to="/desired/baselines/new"><Button variant="primary">Create baseline</Button></Link>} />
      <DesiredTabs />
      {list.error && <Alert>{errorMessage(list.error)}</Alert>}
      {!list.isPending && items.length === 0 && !archived ? (
        <EmptyState title="No baselines yet"
          body="A baseline is a named list of services, packages and files with the state they should have, applied everywhere, to packs (host groups), or to single hosts."
          action={can("desired.manage") ? <Link to="/desired/baselines/new"><Button variant="primary">Create baseline</Button></Link> : undefined} />
      ) : (
        <Card>
          <div className="mb-3"><Checkbox checked={archived} onChange={setArchived} label="Show archived baselines" /></div>
          <Table<Baseline>
            rows={items}
            rowKey={(b) => b.id}
            empty={emptyText("baselines", list, "No baselines.", true)}
            columns={[
              {
                header: "Baseline",
                cell: (b) => (
                  <div>
                    <Link className={plainLink} to={`/desired/baselines/${b.id}`}>{b.name}</Link>
                    {b.archived && <span className="ml-2"><Badge>archived</Badge></span>}
                    {b.description && <div className="text-xs text-muted">{b.description}</div>}
                  </div>
                ),
              },
              { header: "Applies to", cell: (b) => <span className="text-sm">{appliesText(b)}</span> },
              { header: "Resources", cell: (b) => <span className="text-sm text-muted">{resourcesText(b)}</span> },
              {
                header: "Compliance",
                cell: (b) => {
                  const c = b.counts;
                  const bad = c.drift + c.failed + c.conflict;
                  return (
                    <span className="flex flex-wrap gap-1 text-sm">
                      {bad === 0 && c.unknown === 0 ? <span className="text-muted">{c.hosts} host{c.hosts === 1 ? "" : "s"}, all compliant</span> : (
                        <>
                          {c.drift > 0 && <Badge tone="warn">{c.drift} drift</Badge>}
                          {c.conflict > 0 && <Badge tone="danger">{c.conflict} conflict{c.conflict === 1 ? "" : "s"}</Badge>}
                          {c.failed > 0 && <Badge tone="danger">{c.failed} failed</Badge>}
                          {c.unknown > 0 && <Badge>{c.unknown} unknown</Badge>}
                          <span className="text-xs text-muted">on {c.hosts} host{c.hosts === 1 ? "" : "s"}</span>
                        </>
                      )}
                    </span>
                  );
                },
              },
              {
                header: "Automatic restore",
                cell: (b) => b.auto.attention ? <Badge tone="warn">Paused — needs attention</Badge> : b.auto.enabled ? <Badge tone="accent">On</Badge> : <span className="text-muted">Off</span>,
              },
              { header: "Revision", cell: (b) => <span className="text-muted">{b.revision}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
