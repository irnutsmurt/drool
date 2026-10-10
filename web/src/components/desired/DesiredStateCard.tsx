import { Link } from "react-router";
import { Card } from "@/components/ui/Card";
import { errorMessage } from "@/lib/format";
import { describeKey, useDesiredHost } from "./queries";
import { DesiredStatusBadge, statusLabel } from "./ui";

/** A host's desired state on its overview page. */
export function DesiredStateCard({ hostId }: { hostId: string }) {
  const q = useDesiredHost(hostId);
  const h = q.data;
  const problems = (h?.resources ?? []).filter((r) => r.status !== "compliant").slice(0, 5);
  return (
    <Card title="Desired State" actions={<Link className="whitespace-nowrap text-sm text-accent" to={`/hosts/${hostId}/desired`}>View desired state <span aria-hidden="true">→</span></Link>}>
      {q.error ? <p className="text-sm text-danger">Couldn't load desired state: {errorMessage(q.error)}</p> : !h ? <p className="text-sm text-muted">Loading…</p> : h.counts.total === 0 ? (
        <p className="text-sm text-muted">No baseline applies to this host.</p>
      ) : (
        <div className="space-y-2 text-sm">
          <p className="flex flex-wrap items-center gap-2"><DesiredStatusBadge status={h.status} /> {h.counts.compliant} of {h.counts.total} resources compliant
            <span className="text-muted">· {h.baselines.map((b) => b.name).filter((v, i, a) => a.indexOf(v) === i).join(", ")}</span></p>
          {problems.length > 0 && (
            <ul className="space-y-0.5">
              {problems.map((r) => (
                <li key={r.key}><span className="font-mono text-xs">{describeKey(r.key)}</span> — {statusLabel(r.status).toLowerCase()}{r.current && r.status !== "unknown" ? `: ${r.current}` : r.note ? `: ${r.note}` : ""}</li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Card>
  );
}
