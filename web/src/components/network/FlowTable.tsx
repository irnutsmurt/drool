import { useEffect, useState } from "react";
import { Link } from "react-router";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { errorMessage } from "@/lib/format";
import { CountryLabel, DirectionLabel, Endpoint, NetworkLabel, PolicyBadge, ProcessLabel, RemoteLabel, ScopeBadge, when } from "./labels";
import { useNetFlows, type NetFilter, type NetFlow } from "./queries";
import { FlowDialog } from "./FlowDialog";

/** Observed connections for a filter, paged on the server (cursor). Each
 * row opens the connection's details; the first cell is a button, so the
 * table works from the keyboard. */
export function FlowTable({ filter, showHost = true, empty, pageSize = 50 }: {
  filter: NetFilter; showHost?: boolean; empty?: React.ReactNode; pageSize?: number;
}) {
  const [cursors, setCursors] = useState<string[]>([]); // cursors of the pages before the current one
  const [cursor, setCursor] = useState<string | undefined>();
  const [open, setOpen] = useState<number>();
  const key = JSON.stringify(filter);
  useEffect(() => {
    setCursors([]);
    setCursor(undefined);
  }, [key]);
  const q = useNetFlows({ ...filter, limit: pageSize, cursor });
  const page = q.data;
  const rows = page?.items ?? [];
  const start = cursors.length * pageSize;

  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  return (
    <div className="space-y-2" data-testid="flow-table">
      <div className="overflow-x-auto">
        <table className="rtable w-full text-left text-sm">
          <caption className="sr-only">Observed connections</caption>
          <thead>
            <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
              <th scope="col" className="px-2 py-2 font-medium">Direction</th>
              {showHost && <th scope="col" className="px-2 py-2 font-medium">Host</th>}
              <th scope="col" className="px-2 py-2 font-medium">Process / service</th>
              <th scope="col" className="px-2 py-2 font-medium">Local</th>
              <th scope="col" className="px-2 py-2 font-medium">Remote</th>
              <th scope="col" className="px-2 py-2 font-medium">Protocol</th>
              <th scope="col" className="px-2 py-2 font-medium">Country · network (ASN)</th>
              <th scope="col" className="px-2 py-2 font-medium">First · last seen</th>
              <th scope="col" className="px-2 py-2 text-right font-medium">Conn.</th>
              <th scope="col" className="px-2 py-2 font-medium">Policy status</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr><td colSpan={showHost ? 10 : 9} className="rt-empty px-3 py-8 text-center text-muted">{q.isLoading ? "Loading…" : empty ?? "No connections match."}</td></tr>
            ) : rows.map((f) => <Row key={f.id} f={f} showHost={showHost} onOpen={() => setOpen(f.id)} />)}
          </tbody>
        </table>
      </div>
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
        <span aria-live="polite">
          {rows.length > 0 && <>Showing {start + 1}–{start + rows.length}{page?.total != null ? <> of {page.total}</> : page?.next_cursor ? <> (more)</> : null}</>}
          {page?.truncated && <> · the search stopped after scanning {page.scanned} rows; continue to search further</>}
        </span>
        <span className="ml-auto flex gap-2">
          <Button size="sm" variant="ghost" disabled={cursors.length === 0} onClick={() => {
            const prev = [...cursors];
            const c = prev.pop();
            setCursors(prev);
            setCursor(c === "" ? undefined : c);
          }}>Previous</Button>
          <Button size="sm" variant="ghost" disabled={!page?.next_cursor} onClick={() => {
            setCursors([...cursors, cursor ?? ""]);
            setCursor(page!.next_cursor);
          }}>Next</Button>
        </span>
      </div>
      {open !== undefined && <FlowDialog id={open} onClose={() => setOpen(undefined)} />}
    </div>
  );
}

function Row({ f, showHost, onOpen }: { f: NetFlow; showHost: boolean; onOpen: () => void }) {
  return (
    <tr className="cursor-pointer border-b border-line align-top last:border-0 hover:bg-surface-2" onClick={onOpen} data-flow-id={f.id}>
      <td className="px-2 py-2" data-primary="">
        <button type="button" className="text-left text-accent underline-offset-2 hover:underline focus:outline focus:outline-2 focus:outline-accent"
          onClick={(e) => { e.stopPropagation(); onOpen(); }}
          aria-label={`Details: ${f.direction === "in" ? "inbound" : "outbound"} ${f.protocol.toUpperCase()} ${f.direction === "in" ? "from" : "to"} ${f.remote_ip} port ${f.port}`}>
          <DirectionLabel dir={f.direction} />
        </button>
        <div className="mt-0.5"><ScopeBadge flow={f} /></div>
      </td>
      {showHost && <td data-label="Host" className="whitespace-nowrap px-2 py-2"><Link className="text-accent" to={`/hosts/${f.host.id}`} onClick={(e) => e.stopPropagation()}>{f.host.name}</Link></td>}
      <td data-label="Process / service" className="px-2 py-2"><ProcessLabel flow={f} /></td>
      <td data-label="Local" className="px-2 py-2"><Endpoint ip={f.local_ip || "?"} port={f.local_port} /></td>
      <td data-label="Remote" className="px-2 py-2"><RemoteLabel flow={f} /></td>
      <td data-label="Protocol" className="whitespace-nowrap px-2 py-2 text-xs">{f.protocol.toUpperCase()} {f.port}</td>
      <td data-label="Country · network" className="px-2 py-2"><span className="flex flex-col"><CountryLabel flow={f} /><NetworkLabel flow={f} /></span></td>
      <td data-label="First · last seen" className="whitespace-nowrap px-2 py-2 text-xs"><span className="flex flex-col"><span>{when(f.first_seen)}</span><span className="text-muted">{when(f.last_seen)}</span></span></td>
      <td data-label="Connections" className="px-2 py-2 text-right tabular-nums">{f.count.toLocaleString()}</td>
      <td data-label="Policy status" className="px-2 py-2"><PolicyBadge flow={f} /></td>
    </tr>
  );
}
