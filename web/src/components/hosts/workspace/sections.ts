import type { Host } from "@/api/queries";

/** One area of a host's workspace (/hosts/{id}/…). */
export interface HostSection {
  key: string;
  label: string;
  /** Below /hosts/{id}; "" is the Overview. */
  path: string;
  /** Needed to open it; hidden otherwise. */
  permission: string;
  /** Only for hosts DROOL manages (not jump-only hosts). */
  managedOnly?: boolean;
  /** One line: what this area holds (the section selector's help). */
  description: string;
}

/** A host's workspace, in the order its navigation shows them: what the
 * host is like now first, then what can be done to it, then its history. */
export const hostSections: HostSection[] = [
  { key: "overview", label: "Overview", path: "", permission: "hosts.read",
    description: "Health, warnings, open alerts and recent activity at a glance." },
  { key: "monitoring", label: "Monitoring", path: "monitoring", permission: "monitoring.read", managedOnly: true,
    description: "Metrics, services, storage health, sensors, health settings and collection." },
  { key: "files", label: "Files", path: "files", permission: "files.read", managedOnly: true,
    description: "Browse and edit files on the host." },
  { key: "network", label: "Network", path: "network", permission: "network.read", managedOnly: true,
    description: "Connections this host makes and accepts." },
  { key: "firewall", label: "Firewall", path: "firewall", permission: "firewall.read",
    description: "The policy in effect here, its enforcement and history." },
  { key: "desired", label: "Desired State", path: "desired", permission: "desired.read", managedOnly: true,
    description: "What should stay as defined here: drift, conflicts, restore." },
  { key: "management", label: "Management", path: "management", permission: "hosts.read",
    description: "How DROOL reaches the host: SSH, Shepherd, Packs, removal." },
  { key: "activity", label: "Activity", path: "activity", permission: "jobs.read",
    description: "Runs on this host, schedules that may target it, audit events." },
];

/** The sections this user may open for this host. */
export function visibleHostSections(host: Pick<Host, "jump_only">, can: (p: string) => boolean): HostSection[] {
  return hostSections.filter((s) => can(s.permission) && !(s.managedOnly && host.jump_only));
}

/** The section a path below /hosts/{id} belongs to ("" → overview). */
export function sectionOf(rest: string): HostSection {
  const first = rest.replace(/^\/+/, "").split("/")[0] ?? "";
  return hostSections.find((s) => s.path === first && s.path !== "") ?? hostSections[0]!;
}

/** The address of a host's section. */
export const hostPath = (id: string, section = "", query = "") =>
  `/hosts/${id}${section ? `/${section}` : ""}${query ? `?${query}` : ""}`;
