/**
 * The sidebar: direct destinations under non-clickable headings. A
 * returning user should recognise what they want without first having to
 * remember how DROOL groups things (recognition over recall).
 */
export interface NavItem {
  to: string;
  label: string;
  /** The conventional term for a themed label (Packs: Groups), shown next to it. */
  hint?: string;
  /** One sentence: hover/focus help for themed labels, and the search description. */
  description: string;
  /** Hide the item unless the user holds this permission. */
  permission?: string;
  /** Shows the open alert count. */
  alerts?: boolean;
  /** Whether a path belongs to this item (default: the item's path and below). */
  match?: (path: string) => boolean;
}

export interface NavSection {
  id: string;
  /** A visual heading only: not a route, not expandable. */
  heading?: string;
  items: NavItem[];
}

const under = (base: string) => (p: string) => p === base || p.startsWith(base + "/");

export const navSections: NavSection[] = [
  {
    id: "overview",
    items: [{ to: "/", label: "Overview", description: "Fleet triage: what needs attention right now.", match: (p) => p === "/" }],
  },
  {
    id: "fleet",
    heading: "Fleet",
    items: [
      { to: "/hosts", label: "Hosts", description: "The Linux machines DROOL manages.", permission: "hosts.read",
        match: (p) => under("/hosts")(p) || p.startsWith("/file-deployments/") },
      { to: "/groups", label: "Packs", hint: "Groups", description: "Groups of hosts used for targeting and inherited configuration.", permission: "hosts.read" },
      { to: "/agents", label: "Shepherds", hint: "DROOL agents", description: "DROOL agents installed on managed hosts.", permission: "agents.read" },
    ],
  },
  {
    id: "observe",
    heading: "Observe",
    items: [
      { to: "/monitoring", label: "Monitoring", description: "Health, metrics, services and hardware of every host.", permission: "monitoring.read" },
      { to: "/alerts", label: "Alerts", description: "Problems monitoring confirmed, who was told, and maintenance.", permission: "alerts.read", alerts: true },
      { to: "/network", label: "Network Activity", description: "What your hosts are talking to.", permission: "network.read" },
    ],
  },
  {
    id: "automate",
    heading: "Automate",
    items: [
      { to: "/jobs", label: "Runs", description: "Every job DROOL runs: commands, updates, deployments and corrections.", permission: "jobs.read",
        match: (p) => under("/jobs")(p) && !under("/jobs/schedules")(p) && !under("/jobs/workflows")(p) },
      { to: "/jobs/schedules", label: "Schedules", description: "Jobs and workflows that run at times you choose.", permission: "schedules.read" },
      { to: "/jobs/workflows", label: "Workflows", description: "Reusable lists of steps to run or schedule.", permission: "workflows.read" },
      { to: "/desired", label: "Desired State", description: "Services, packages and files that should stay as you defined them.", permission: "desired.read" },
    ],
  },
  {
    id: "protect",
    heading: "Protect",
    items: [
      { to: "/firewall", label: "Firewall", description: "What may talk to what, applied safely.", permission: "firewall.read" },
      { to: "/credentials", label: "Credentials", description: "SSH keys and passwords DROOL uses to reach hosts.", permission: "credentials.use" },
      { to: "/audit", label: "Audit Log", description: "Who changed what, and when.", permission: "audit.read" },
    ],
  },
  {
    id: "settings",
    heading: "Settings",
    items: [{ to: "/settings", label: "Settings", description: "Your account, API tokens, users, notifications and the controller." }],
  },
];

/** Whether `path` belongs to `item` (decides the highlighted sidebar entry). */
export function isActive(item: NavItem, path: string): boolean {
  return item.match ? item.match(path) : under(item.to)(path);
}

/** The sections and items a user may see (empty sections are dropped). */
export function visibleSections(can: (permission: string) => boolean): NavSection[] {
  return navSections
    .map((s) => ({ ...s, items: s.items.filter((i) => !i.permission || can(i.permission)) }))
    .filter((s) => s.items.length > 0);
}
