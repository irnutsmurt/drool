import { Badge } from "@/components/ui/Badge";
import type { Management } from "./queries";

const stateLabels: Record<string, string> = {
  connected: "connected",
  not_connected: "not connected",
  not_enrolled: "not enrolled",
  incompatible: "incompatible version",
  epoch_mismatch: "epoch mismatch",
  quarantined: "identity conflict",
  revoked: "revoked",
  disabled: "endpoint disabled",
};

/** One-glance management path: "Shepherd" (the DROOL agent), "Shepherd
 * not connected → SSH", "SSH". */
export function ManagementBadge({ m }: { m?: Management | null }) {
  if (!m) return null;
  const badge = (tone: "neutral" | "ok" | "warn" | "danger", text: string) => (
    <span title={m.preferred === "agent" ? "Shepherd = the DROOL agent on the host" : undefined}><Badge tone={tone}>{text}</Badge></span>
  );
  if (m.preferred === "ssh") return <Badge>SSH</Badge>;
  if (m.blocked) return badge("danger", `Shepherd: ${stateLabels[m.agent_state] ?? m.agent_state}`);
  if (m.active === "agent") return badge("ok", "Shepherd");
  if (m.active === "ssh") return badge("warn", `Shepherd ${stateLabels[m.agent_state] ?? m.agent_state} → SSH`);
  return badge("danger", `Shepherd ${stateLabels[m.agent_state] ?? m.agent_state}`);
}

export { stateLabels };
