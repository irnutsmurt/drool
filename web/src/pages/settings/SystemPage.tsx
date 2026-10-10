import { useSystemInfo } from "@/components/layout/AppShell";
import { Card } from "@/components/ui/Card";
import { Table } from "@/components/ui/Table";
import { Badge } from "@/components/ui/Badge";
import { Alert } from "@/components/ui/Alert";
import { RestoreReconciliationCard } from "@/components/system/RestoreReconciliation";
import { errorMessage } from "@/lib/format";
import type { Schemas } from "@/api/client";

type ConfigValue = Schemas["ConfigValue"];
const sourceTone = { default: "neutral", file: "accent", env: "warn" } as const;

export function SystemPage() {
  const info = useSystemInfo();
  if (info.error) return <Alert>{errorMessage(info.error)}</Alert>;
  const d = info.data;
  return (
    <div className="grid gap-4">
      <Card title="Controller">
        <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[10rem_1fr] gap-y-2 text-sm">
          <dt className="text-muted">Version</dt><dd>{d?.version} ({d?.commit})</dd>
          <dt className="text-muted">Built</dt><dd>{d?.build_date}</dd>
          <dt className="text-muted">Go</dt><dd>{d?.go_version}</dd>
          <dt className="text-muted">Schema version</dt><dd>{d?.schema_version}</dd>
          <dt className="text-muted">TLS mode</dt><dd>{d?.tls_mode}</dd>
          {d?.config_file !== undefined && (<><dt className="text-muted">Config file</dt><dd>{d.config_file || "none"}</dd></>)}
        </dl>
      </Card>
      <RestoreReconciliationCard />
      <Card title="Backup and restore">
        <div className="space-y-3 text-sm">
          <p>
            Backups are made from the command line and are safe to take while DROOL runs. They contain the database (hosts,
            host keys, users, encrypted credentials, job history, audit log), the TLS certificate and the config file.
          </p>
          <div>
            <div className="text-xs font-medium uppercase text-muted">Docker</div>
            <code className="block font-mono text-xs">docker compose exec -T drool drool backup -o - &gt; drool-backup.tar.gz</code>
          </div>
          <div>
            <div className="text-xs font-medium uppercase text-muted">Native</div>
            <code className="block font-mono text-xs">sudo -u drool drool backup</code>
          </div>
          <p className="text-muted">
            Stored credentials are encrypted with the master key, which is not in the backup unless you add
            <code className="mx-1 font-mono text-xs">--include-master-key</code>. Without it, keep a copy of the master key file
            somewhere safe and separate, or the credentials can't be restored. Restoring needs DROOL stopped:
            <code className="mx-1 font-mono text-xs">drool restore FILE</code> (see the README).
          </p>
          <p className="text-muted">
            <code className="mr-1 font-mono text-xs">drool backup verify FILE</code> checks that a backup can be restored without restoring it.
            Before a newer DROOL upgrades the database, it backs it up by itself to <code className="font-mono text-xs">backups/drool-pre-upgrade-…</code>
            in the data directory (the newest three are kept).
          </p>
        </div>
      </Card>
      {d?.config && (
        <Card title="Effective configuration">
          <Table<ConfigValue>
            rows={d.config}
            rowKey={(c) => c.key}
            columns={[
              { header: "Key", cell: (c) => <code className="font-mono text-xs">{c.key}</code> },
              { header: "Value", cell: (c) => <code className="font-mono text-xs">{JSON.stringify(c.value)}</code> },
              { header: "Source", cell: (c) => <Badge tone={sourceTone[c.source]}>{c.source}</Badge> },
              { header: "Override with", cell: (c) => <code className="font-mono text-xs text-muted">{c.env_var}</code> },
            ]}
          />
        </Card>
      )}
    </div>
  );
}
