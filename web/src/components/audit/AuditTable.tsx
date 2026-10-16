import { useInfiniteQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatDateTime } from "@/lib/format";

type Event = Schemas["AuditEvent"];

const resultTone = { success: "ok", failure: "danger", denied: "warn" } as const;

/** Audit events, newest first, a page at a time from the server: all of
 * them, those whose action starts with a prefix, or those about one
 * target (a host). */
export function AuditTable({ action, target, pageSize = 50, empty }: { action?: string; target?: string; pageSize?: number; empty?: string }) {
  const q = useInfiniteQuery({
    queryKey: ["audit", action ?? "", target ?? "", pageSize],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/audit", { params: { query: { limit: pageSize, cursor: pageParam || undefined, action: action || undefined, target: target || undefined } } })),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="space-y-3" data-testid="audit-table">
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      <Table<Event>
        rows={rows}
        rowKey={(e) => e.id}
        empty={q.isPending ? "Loading…" : empty ?? "No audit events."}
        columns={[
          { header: "#", cell: (e) => <span className="font-mono text-xs text-muted" title={e.hash ? `chain hash ${e.hash}` : "from before the hash chain"}>{e.seq ?? "—"}</span> },
          { header: "Time", cell: (e) => <span className="whitespace-nowrap">{formatDateTime(e.ts)}</span> },
          { header: "Actor", cell: (e) => e.actor.name || e.actor.type },
          { header: "Action", primary: true, cell: (e) => <code className="font-mono text-xs">{e.action}</code> },
          ...(target ? [] : [{ header: "Target", cell: (e: Event) => (e.target.id
            ? <span className="block max-w-[13rem] [overflow-wrap:anywhere]">{e.target.type} <span className="font-mono text-xs">{e.target.id}</span></span>
            : "—") }]),
          { header: "Result", cell: (e) => <Badge tone={resultTone[e.result]}>{e.result}</Badge> },
          { header: "Source", cell: (e) => e.source_ip || "—" },
          {
            header: "Details",
            cell: (e) => (
              <code className="block max-w-[14rem] truncate font-mono text-xs text-muted" title={JSON.stringify(e.details)}>
                {Object.keys(e.details).length ? JSON.stringify(e.details) : ""}
              </code>
            ),
          },
        ]}
      />
      {(q.hasNextPage || rows.length > 0) && (
        <div className="flex flex-wrap items-center gap-3 text-xs text-muted">
          <span aria-live="polite">{rows.length} event{rows.length === 1 ? "" : "s"} shown{q.hasNextPage ? "; older ones on request" : ""}</span>
          {q.hasNextPage && (
            <Button size="sm" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>
              {q.isFetchingNextPage ? "Loading…" : "Show more"}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
