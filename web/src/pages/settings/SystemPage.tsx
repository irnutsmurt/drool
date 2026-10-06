import { useSystemInfo } from "@/components/layout/AppShell";
import { Card } from "@/components/ui/Card";
import { Table } from "@/components/ui/Table";
import { Badge } from "@/components/ui/Badge";
import { Alert } from "@/components/ui/Alert";
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
        <dl className="grid grid-cols-[10rem_1fr] gap-y-2 text-sm">
          <dt className="text-muted">Version</dt><dd>{d?.version} ({d?.commit})</dd>
          <dt className="text-muted">Built</dt><dd>{d?.build_date}</dd>
          <dt className="text-muted">Go</dt><dd>{d?.go_version}</dd>
          <dt className="text-muted">Schema version</dt><dd>{d?.schema_version}</dd>
          <dt className="text-muted">TLS mode</dt><dd>{d?.tls_mode}</dd>
          {d?.config_file !== undefined && (<><dt className="text-muted">Config file</dt><dd>{d.config_file || "none"}</dd></>)}
        </dl>
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
