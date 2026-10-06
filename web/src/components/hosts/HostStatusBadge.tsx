import { Badge } from "@/components/ui/Badge";
import type { Host } from "@/api/queries";

const labels: Record<Host["status"], { text: string; tone: "ok" | "warn" | "danger" | "neutral" | "accent" }> = {
  pending_hostkey: { text: "Needs host key", tone: "warn" },
  untested: { text: "Untested", tone: "neutral" },
  ready: { text: "Ready", tone: "ok" },
  unreachable: { text: "Unreachable", tone: "danger" },
  hostkey_mismatch: { text: "Host key changed", tone: "danger" },
  auth_failed: { text: "Access denied", tone: "danger" },
  unsupported: { text: "Unsupported", tone: "warn" },
  disabled: { text: "Disabled", tone: "neutral" },
};

export function HostStatusBadge({ status }: { status: Host["status"] }) {
  const l = labels[status];
  return <Badge tone={l.tone}>{l.text}</Badge>;
}
