import { Link, useSearchParams } from "react-router";
import { PageHeader } from "@/components/ui/PageHeader";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Field";
import { plainLink } from "@/components/ui/links";
import { useHosts } from "@/api/queries";
import { AuditIntegrityCard, AuditSinkCard } from "@/components/audit/AuditIntegrity";
import { AuditTable } from "@/components/audit/AuditTable";

/** The audit log: who did what, when. Filtered by action prefix, and by
 * target (?target=, the way a host's Activity links here). */
export function AuditPage() {
  const [params, setParams] = useSearchParams();
  const action = params.get("action") ?? "";
  const target = params.get("target") ?? "";
  const hosts = useHosts();
  const targetHost = hosts.data?.items.find((h) => h.id === target);
  const set = (k: string, v: string) => setParams((p) => {
    if (v) p.set(k, v);
    else p.delete(k);
    return p;
  }, { replace: true });

  return (
    <>
      <PageHeader title="Audit log" description="Who did what, when: every security-relevant action. Records are chained by hashes, so changes to stored history are detectable." />
      <div className="mb-4 grid gap-4 lg:grid-cols-2">
        <AuditIntegrityCard />
        <AuditSinkCard />
      </div>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Input className="max-w-xs" aria-label="Filter by action" placeholder="Filter by action prefix, e.g. auth." value={action} onChange={(e) => set("action", e.target.value)} />
        {target && (
          <span className="text-sm">
            About {targetHost ? <Link className={plainLink} to={`/hosts/${target}/activity?view=audit`}>{targetHost.name}</Link> : <code className="font-mono text-xs">{target}</code>}
            {" · "}<button type="button" className="underline underline-offset-2 hover:text-fg" onClick={() => set("target", "")}>show every target</button>
          </span>
        )}
      </div>
      <Card>
        <AuditTable action={action} target={target} />
      </Card>
    </>
  );
}
