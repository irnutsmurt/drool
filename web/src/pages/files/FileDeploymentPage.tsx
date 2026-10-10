import { Link, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { DiffView } from "@/components/files/DiffView";
import { FileDeployResult, type DeployResult } from "@/components/files/FileDeployResult";
import { Alert } from "@/components/ui/Alert";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { errorMessage, formatDateTime } from "@/lib/format";

/** One host's deployment of a file: what changed, the diff, rollback. */
export function FileDeploymentPage() {
  const { id = "" } = useParams();
  const d = useQuery({
    queryKey: ["file-deployment", id],
    queryFn: () => unwrap(api.GET("/file-deployments/{id}", { params: { path: { id } } })),
  });
  const history = useQuery({
    queryKey: ["file-deployments", d.data?.host_id, d.data?.path],
    queryFn: () => unwrap(api.GET("/file-deployments", { params: { query: { host: d.data!.host_id, path: d.data!.path, limit: 20 } } })),
    enabled: !!d.data,
  });
  if (d.error) return <Alert>{errorMessage(d.error)}</Alert>;
  const x = d.data;
  if (!x) return <p className="text-muted">Loading…</p>;
  const result: DeployResult = {
    path: x.path, outcome: x.outcome, deployment_id: x.id, rollback_of: x.rollback_of,
    before: x.before as DeployResult["before"], after: x.after as DeployResult["after"],
    validation: (x.validation as DeployResult["validation"])?.command ? x.validation as DeployResult["validation"] : null,
    service: (x.service as DeployResult["service"])?.unit ? x.service as DeployResult["service"] : null,
  };
  return (
    <>
      <PageHeader crumbs={[{ label: "Hosts", to: "/hosts" }, { label: x.host_name, to: `/hosts/${x.host_id}` }]} title={x.path}
        description={<span>{x.kind === "rollback" ? "Rollback" : "Deployment"} on <Link className="text-accent" to={`/hosts/${x.host_id}`}>{x.host_name}</Link> · {formatDateTime(x.created_at)}
          {x.job_id && <> · <Link className="text-accent" to={`/jobs/${x.job_id}`}>run</Link></>}</span>} />
      <div className="grid gap-4">
        <FileDeployResult r={result} standalone />
        {x.error && <Alert>{x.error}</Alert>}
        <Card title="Changes">
          <DiffView diff={x.diff} note={x.diff_note || (x.outcome === "unchanged" ? "Nothing changed." : undefined)} />
          {x.before && (x.before as { kept?: boolean }).kept && (x.before as { sha256?: string }).sha256 && (
            <a className="mt-2 inline-block text-xs text-accent" download
              href={`/api/v1/artifacts/${(x.before as { sha256: string }).sha256}/content`}>Download the previous version</a>
          )}
        </Card>
        <Card title={`History of ${x.path} on ${x.host_name}`}>
          <ul className="space-y-1 text-sm">
            {(history.data?.items ?? []).map((h) => (
              <li key={h.id} className="flex flex-wrap gap-3">
                <Link className={h.id === x.id ? "font-semibold" : "text-accent"} to={`/file-deployments/${h.id}`}>{formatDateTime(h.created_at)}</Link>
                <span>{h.kind}</span>
                <span className="text-muted">{h.outcome}</span>
                <span className="font-mono text-xs text-muted">{(h.after as { sha256?: string }).sha256?.slice(0, 12)}</span>
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </>
  );
}
