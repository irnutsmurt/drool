import { Link, useNavigate } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { errorMessage, formatBytes } from "@/lib/format";

interface FileState { exists?: boolean; sha256?: string; owner?: string; group?: string; mode?: string; size?: number; captured?: boolean; kept?: boolean }
export interface DeployResult {
  path: string;
  outcome: string;
  changed?: boolean;
  note?: string;
  before?: FileState;
  after?: FileState;
  validation?: { command: string; exit_code: number; ok: boolean } | null;
  service?: { unit: string; action: string; ok: boolean; error?: string } | null;
  deployment_id?: string;
  rollback_of?: string;
}

const outcomeText: Record<string, { text: string; tone: "ok" | "warn" | "danger" | "neutral" | "accent" }> = {
  installed: { text: "file installed", tone: "ok" },
  unchanged: { text: "unchanged", tone: "neutral" },
  removed: { text: "file removed", tone: "ok" },
  validation_failed: { text: "validation failed; file not changed", tone: "danger" },
  failed: { text: "not deployed", tone: "danger" },
};

function describeState(s?: FileState) {
  if (!s || s.exists === false) return <span className="text-muted">did not exist</span>;
  return (
    <span>
      <span className="font-mono text-xs">{s.sha256?.slice(0, 12)}</span>{" "}
      <span className="text-muted">{s.owner}:{s.group} {s.mode}{s.size != null && ` · ${formatBytes(s.size)}`}</span>
    </span>
  );
}

/** A file deployment's outcome on one host. The file's outcome and the
 * service action's are shown separately. */
export function FileDeployResult({ r, standalone }: { r: DeployResult; standalone?: boolean }) {
  const { can } = useAuth();
  const navigate = useNavigate();
  const o = outcomeText[r.outcome] ?? { text: r.outcome, tone: "neutral" as const };
  const rollback = useMutation({
    mutationFn: () => unwrap(api.POST("/file-deployments/{id}/rollback", { params: { path: { id: r.deployment_id! } } })),
    onSuccess: (j) => navigate(`/jobs/${j.id}`),
  });
  const canRollback = (r.outcome === "installed" || r.outcome === "removed") && !!r.deployment_id &&
    (r.before?.exists === false || r.before?.captured || r.before?.kept);
  return (
    <div className="mb-3 space-y-2 rounded-ui border border-line p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={o.tone}>{o.text}</Badge>
        {r.rollback_of && <Badge tone="accent">rollback</Badge>}
        <span className="font-mono text-xs">{r.path}</span>
        {r.note && <span className="text-xs text-muted">{r.note}</span>}
      </div>
      <dl className="grid grid-cols-[7rem_1fr] gap-y-1">
        <dt className="text-muted">Before</dt><dd>{describeState(r.before)}</dd>
        {r.after && (<><dt className="text-muted">After</dt><dd>{describeState(r.after)}</dd></>)}
        {r.validation && (
          <><dt className="text-muted">Validation</dt>
            <dd className={r.validation.ok ? "text-ok" : "text-danger"}>
              {r.validation.ok ? "passed" : `failed (exit ${r.validation.exit_code})`} <span className="font-mono text-xs text-muted">{r.validation.command}</span>
            </dd></>
        )}
        {r.service && (
          <><dt className="text-muted">Service</dt>
            <dd className={r.service.ok ? "text-ok" : "text-danger"}>
              {r.service.action} {r.service.unit}: {r.service.ok ? "done" : `failed — ${r.service.error}`}
            </dd></>
        )}
      </dl>
      {r.outcome === "installed" && r.service && !r.service.ok && (
        <Alert tone="warn">The new file is in place; only the {r.service.action} of {r.service.unit} failed. Fix the service, or roll back the file.</Alert>
      )}
      {r.before?.exists && !r.before.captured && !r.before.kept && r.outcome === "installed" && (
        <p className="text-xs text-muted">The previous version was too large to keep, so this change can't be rolled back automatically.</p>
      )}
      {rollback.error && <Alert>{errorMessage(rollback.error)}</Alert>}
      <div className="flex gap-2">
        {r.deployment_id && !standalone && <Link className="inline-flex h-7 items-center text-xs text-accent" to={`/file-deployments/${r.deployment_id}`}>Details and diff</Link>}
        {canRollback && can("files.deploy") && can("jobs.execute") && (
          <Button size="sm" disabled={rollback.isPending} onClick={() => {
            const what = r.before?.exists === false ? `remove ${r.path} (it didn't exist before)` : `put back the previous version of ${r.path}`;
            if (confirm(`Roll back: ${what}? This only happens if the file still has the content this deployment installed.`)) rollback.mutate();
          }}>Roll back</Button>
        )}
      </div>
    </div>
  );
}
