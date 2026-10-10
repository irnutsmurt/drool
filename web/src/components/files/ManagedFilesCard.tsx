import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/Badge";
import { Card } from "@/components/ui/Card";
import { formatDateTime } from "@/lib/format";

/** Files DROOL has deployed to this host (file deployments, not Desired
 * State's managed files), with their latest deployment. */
export function ManagedFilesCard({ hostId }: { hostId: string }) {
  const q = useQuery({
    queryKey: ["managed-files", hostId],
    queryFn: () => unwrap(api.GET("/hosts/{id}/managed-files", { params: { path: { id: hostId } } })),
  });
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Card title="Deployed files" actions={<Link className="whitespace-nowrap text-sm text-accent" to={`/hosts/${hostId}/files`}>Open file manager <span aria-hidden="true">→</span></Link>}>
      <ul className="space-y-1.5 text-sm">
        {items.map((d) => (
          <li key={d.id} className="flex flex-wrap items-center gap-3">
            <Link className="font-mono text-accent" to={`/file-deployments/${d.id}`}>{d.path}</Link>
            <Badge tone={d.outcome === "installed" || d.outcome === "unchanged" ? "ok" : d.outcome === "removed" ? "neutral" : "danger"}>
              {d.kind === "rollback" ? `rolled back (${d.outcome})` : d.outcome}
            </Badge>
            <span className="ml-auto text-xs text-muted">{formatDateTime(d.created_at)}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}
