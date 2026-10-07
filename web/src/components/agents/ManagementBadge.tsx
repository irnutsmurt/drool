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

/** One-glance management path: "Agent", "Agent offline → SSH", "SSH". */
export function ManagementBadge({ m }: { m?: Management | null }) {
  if (!m) return null;
  if (m.preferred === "ssh") return <Badge>SSH</Badge>;
  if (m.blocked) return <Badge tone="danger">Agent: {stateLabels[m.agent_state] ?? m.agent_state}</Badge>;
  if (m.active === "agent") return <Badge tone="ok">Agent</Badge>;
  if (m.active === "ssh") return <Badge tone="warn">Agent {stateLabels[m.agent_state] ?? m.agent_state} → SSH</Badge>;
  return <Badge tone="danger">Agent {stateLabels[m.agent_state] ?? m.agent_state}</Badge>;
}

export { stateLabels };
