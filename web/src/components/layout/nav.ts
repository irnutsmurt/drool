export interface NavItem {
  to: string;
  label: string;
  /** Hide the item unless the user holds this permission. */
  permission?: string;
  /** Not implemented yet; shown as a placeholder with this milestone label. */
  milestone?: string;
}

export const navItems: NavItem[] = [
  { to: "/", label: "Dashboard" },
  { to: "/hosts", label: "Hosts", permission: "hosts.read" },
  { to: "/groups", label: "Groups", permission: "hosts.read" },
  { to: "/jobs", label: "Jobs", permission: "jobs.read", milestone: "M2" },
  { to: "/credentials", label: "Credentials", permission: "credentials.use" },
  { to: "/audit", label: "Audit log", permission: "audit.read" },
  { to: "/settings", label: "Settings" },
];
