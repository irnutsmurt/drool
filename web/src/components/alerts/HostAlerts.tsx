import { useAuth } from "@/auth/AuthContext";
import { Alert as Notice } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { errorMessage } from "@/lib/format";
import { useAlertSummary, useEndMaintenance } from "./queries";
import { untilText } from "./ui";

/** "In maintenance until 18:00 (Kernel upgrade)" with an End button. */
export function HostMaintenanceBanner({ hostId }: { hostId: string }) {
  const { can } = useAuth();
  const sum = useAlertSummary(can("alerts.read"));
  const end = useEndMaintenance();
  const m = sum.data?.maintenance[hostId];
  if (!m) return null;
  return (
    <div className="mb-4">
      <Notice tone="warn">
        <span aria-hidden="true">⏸ </span>In maintenance {untilText(m.ends_at)}{m.reason ? ` — ${m.reason}` : ""}. Notifications are paused;
        monitoring and alerts continue.
        {can("maintenance.manage") && (
          <Button size="sm" className="ml-3" onClick={() => end.mutate(m.id)} disabled={end.isPending}>End maintenance</Button>
        )}
        {end.error && <span className="ml-2">{errorMessage(end.error)}</span>}
      </Notice>
    </div>
  );
}

