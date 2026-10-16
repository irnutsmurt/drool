import { useEffect, useState } from "react";
import { Link } from "react-router";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { cx } from "@/components/ui/cx";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { errorMessage } from "@/lib/format";
import { ClassBadge, CountryLabel, DirectionLabel, Endpoint, NetworkLabel, PolicyBadge, ProcessLabel, RemoteLabel, ScopeBadge, scopeText, when } from "./labels";
import { useNetFlows, type NetEnrichState, type NetFilter, type NetFlow } from "./queries";
import { FlowInspector } from "./FlowInspector";
import { GeoAttribution } from "./Enrichment";

/** A phone's page of connections: a screenful or two, the rest a tap away. */
const PHONE_PAGE = 25;

/** Observed connections for a filter, paged on the server (cursor). Each
 * row opens the connection in an inspector beside the list; the first
 * cell is a button, so the table works from the keyboard. On a phone each
 * connection is a compact entry (direction, remote end, host, process,
 * what it is) instead of a card with every column. */
export function FlowTable({ filter, showHost = true, empty, pageSize = 50 }: {
  filter: NetFilter; showHost?: boolean; empty?: React.ReactNode; pageSize?: number;
}) {
  const phone = useMediaQuery("(max-width: 767.98px)");
  const size = phone ? Math.min(pageSize, PHONE_PAGE) : pageSize;
  const [cursors, setCursors] = useState<string[]>([]); // cursors of the pages before the current one
  const [cursor, setCursor] = useState<string | undefined>();
  const [open, setOpen] = useState<number>();
  const key = JSON.stringify(filter) + size;
  useEffect(() => {
    setCursors([]);
    setCursor(undefined);
  }, [key]);
  const q = useNetFlows({ ...filter, limit: size, cursor });
  const page = q.data;
  const rows = page?.items ?? [];
  const start = cursors.length * size;
  const none = q.isLoading ? "Loading…" : empty ?? "No connections match.";

  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  return (
    <div className="space-y-2" data-testid="flow-table">
      {phone ? (
        <ul className="divide-y divide-line" aria-label="Observed connections">
          {rows.length === 0 ? <li className="px-3 py-8 text-center text-sm text-muted">{none}</li>
            : rows.map((f) => <Compact key={f.id} f={f} showHost={showHost} active={open === f.id} onOpen={() => setOpen(f.id)} />)}
        </ul>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Observed connections</caption>
            <thead>
              <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
                <th scope="col" className="px-2 py-2 font-medium">Direction</th>
                {showHost && <th scope="col" className="px-2 py-2 font-medium">Host</th>}
                <th scope="col" className="px-2 py-2 font-medium">Process · local</th>
                <th scope="col" className="px-2 py-2 font-medium">Remote · service</th>
                <th scope="col" className="px-2 py-2 font-medium">Country · network (ASN)</th>
                <th scope="col" className="px-2 py-2 font-medium">Last seen</th>
                <th scope="col" className="px-2 py-2 text-right font-medium">Conn.</th>
                <th scope="col" className="px-2 py-2 font-medium">What it is · policy</th>
              </tr>
            </thead>
            <tbody>
              {rows.length === 0 ? (
                <tr><td colSpan={showHost ? 8 : 7} className="px-3 py-8 text-center text-muted">{none}</td></tr>
              ) : rows.map((f) => <Row key={f.id} f={f} showHost={showHost} state={page?.enrichment_state} active={open === f.id} onOpen={() => setOpen(f.id)} />)}
            </tbody>
          </table>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
        <GeoAttribution state={page?.enrichment_state} />
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
      {open !== undefined && <FlowInspector id={open} onClose={() => setOpen(undefined)} />}
    </div>
  );
}

function detailsLabel(f: NetFlow) {
  return `Details: ${f.direction === "in" ? "inbound" : "outbound"} ${f.protocol.toUpperCase()} ${f.direction === "in" ? "from" : "to"} ${f.remote_ip} port ${f.port}`;
}

function Row({ f, showHost, state, active, onOpen }: { f: NetFlow; showHost: boolean; state?: NetEnrichState; active: boolean; onOpen: () => void }) {
  return (
    <tr className={cx("cursor-pointer border-b border-line align-top last:border-0 hover:bg-surface-2", active && "bg-accent/10")} onClick={onOpen} data-flow-id={f.id}>
      <td className="px-2 py-2">
        <button type="button" className="text-left text-accent underline-offset-2 hover:underline focus:outline focus:outline-2 focus:outline-accent"
          onClick={(e) => { e.stopPropagation(); onOpen(); }} aria-label={detailsLabel(f)}>
          <DirectionLabel dir={f.direction} />
        </button>
        <div className="mt-0.5"><ScopeBadge flow={f} /></div>
      </td>
      {showHost && <td className="whitespace-nowrap px-2 py-2"><Link className="text-accent" to={`/hosts/${f.host.id}/network`} onClick={(e) => e.stopPropagation()}>{f.host.name}</Link></td>}
      <td className="px-2 py-2">
        <span className="flex flex-col gap-0.5"><ProcessLabel flow={f} /><Endpoint ip={f.local_ip || "?"} port={f.local_port} /></span>
      </td>
      <td className="px-2 py-2">
        <span className="flex flex-col gap-0.5"><RemoteLabel flow={f} /><span className="whitespace-nowrap text-xs">{f.protocol.toUpperCase()} {f.port}</span></span>
      </td>
      <td className="px-2 py-2"><span className="flex flex-col"><CountryLabel flow={f} state={state} /><NetworkLabel flow={f} state={state} /></span></td>
      <td className="whitespace-nowrap px-2 py-2 text-xs" title={`First seen ${when(f.first_seen)}`}>{when(f.last_seen)}</td>
      <td className="px-2 py-2 text-right tabular-nums">{f.count.toLocaleString()}</td>
      <td className="px-2 py-2"><span className="flex flex-col items-start gap-1"><ClassBadge flow={f} /><PolicyBadge flow={f} /></span></td>
    </tr>
  );
}

/** One connection on a phone: what it is in two lines, tap for the rest. */
function Compact({ f, showHost, active, onOpen }: { f: NetFlow; showHost: boolean; active: boolean; onOpen: () => void }) {
  const remote = f.peer_host?.name ?? f.remote_ip;
  return (
    <li data-flow-id={f.id} className={cx(active && "bg-accent/10")}>
      <button type="button" onClick={onOpen} aria-label={detailsLabel(f)}
        className="flex w-full flex-col gap-1 px-1 py-2.5 text-left hover:bg-surface-2 focus:outline focus:outline-2 focus:outline-accent">
        <span className="flex items-baseline gap-2">
          <span aria-hidden="true" className="text-muted">{f.direction === "in" ? "↘" : "↗"}</span>
          <span className="min-w-0 flex-1 truncate font-medium">{remote}<span className="font-normal text-muted"> · {f.protocol.toUpperCase()} {f.port}</span></span>
          <span className="shrink-0 text-xs text-muted">{when(f.last_seen)}</span>
        </span>
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
          <span>{f.direction === "in" ? "Inbound" : "Outbound"}{showHost && <> · {f.host.name}</>} · {f.process || "unknown process"}</span>
          <span>{(scopeText[f.scope] ?? scopeText.special!).label}</span>
          {f.count > 1 && <span>· {f.count.toLocaleString()}×</span>}
        </span>
        <span className="flex flex-wrap items-center gap-1"><ClassBadge flow={f} /><PolicyBadge flow={f} /></span>
      </button>
    </li>
  );
}
