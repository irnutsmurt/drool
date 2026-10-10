import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { PageHeader } from "@/components/ui/PageHeader";
import { Card } from "@/components/ui/Card";
import { Table } from "@/components/ui/Table";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Field";
import { Alert } from "@/components/ui/Alert";
import { errorMessage, formatDateTime } from "@/lib/format";
import { AuditIntegrityCard, AuditSinkCard } from "@/components/audit/AuditIntegrity";

type Event = Schemas["AuditEvent"];

const resultTone = { success: "ok", failure: "danger", denied: "warn" } as const;

export function AuditPage() {
  const [action, setAction] = useState("");
  const q = useInfiniteQuery({
    queryKey: ["audit", action],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/audit", { params: { query: { limit: 50, cursor: pageParam || undefined, action: action || undefined } } })),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <>
      <PageHeader title="Audit log" description="Who did what, when: every security-relevant action. Records are chained by hashes, so changes to stored history are detectable." />
      <div className="mb-4 grid gap-4 lg:grid-cols-2">
        <AuditIntegrityCard />
        <AuditSinkCard />
      </div>
      <div className="mb-4 max-w-xs">
        <Input placeholder="Filter by action prefix, e.g. auth." value={action} onChange={(e) => setAction(e.target.value)} />
      </div>
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      <Card>
        <Table<Event>
          rows={rows}
          rowKey={(e) => e.id}
          empty={q.isPending ? "Loading…" : "No audit events."}
          columns={[
            { header: "#", cell: (e) => <span className="font-mono text-xs text-muted" title={e.hash ? `chain hash ${e.hash}` : "from before the hash chain"}>{e.seq ?? "—"}</span> },
            { header: "Time", cell: (e) => <span className="whitespace-nowrap">{formatDateTime(e.ts)}</span> },
            { header: "Actor", cell: (e) => e.actor.name || e.actor.type },
            { header: "Action", cell: (e) => <code className="font-mono text-xs">{e.action}</code> },
            { header: "Target", cell: (e) => (e.target.id ? `${e.target.type} ${e.target.id}` : "—") },
            { header: "Result", cell: (e) => <Badge tone={resultTone[e.result]}>{e.result}</Badge> },
            { header: "Source", cell: (e) => e.source_ip || "—" },
            {
              header: "Details",
              cell: (e) => (
                <code className="block max-w-sm truncate font-mono text-xs text-muted" title={JSON.stringify(e.details)}>
                  {Object.keys(e.details).length ? JSON.stringify(e.details) : ""}
                </code>
              ),
            },
          ]}
        />
        {q.hasNextPage && (
          <div className="mt-4 text-center">
            <Button size="sm" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>
              Load more
            </Button>
          </div>
        )}
      </Card>
    </>
  );
}
