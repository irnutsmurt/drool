import type { HostKey } from "@/api/queries";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";

/** Shows host keys; offered keys get a "Trust" action. */
export function HostKeyList({ keys, onApprove, busy, expected }: {
  keys: HostKey[];
  onApprove?: (k: HostKey) => void;
  busy?: boolean;
  /** Fingerprint the user expects; matching keys are highlighted. */
  expected?: string;
}) {
  if (keys.length === 0) return <p className="text-sm text-muted">No keys recorded yet.</p>;
  return (
    <ul className="divide-y divide-line rounded-ui border border-line">
      {keys.map((k) => {
        const matches = expected && expected.trim() === k.fingerprint;
        return (
          <li key={k.id} className="flex flex-wrap items-center gap-3 px-3 py-2">
            <div className="min-w-0 flex-1">
              <div className="text-xs text-muted">{k.type}</div>
              <code className="break-all font-mono text-xs">{k.fingerprint}</code>
            </div>
            {matches && <Badge tone="ok">matches expected</Badge>}
            {k.status === "pinned" && <Badge tone="ok">trusted</Badge>}
            {k.status === "retired" && <Badge>retired</Badge>}
            {k.status === "offered" && onApprove && (
              <Button size="sm" variant={matches ? "primary" : "secondary"} disabled={busy} onClick={() => onApprove(k)}>
                Trust this key
              </Button>
            )}
          </li>
        );
      })}
    </ul>
  );
}
