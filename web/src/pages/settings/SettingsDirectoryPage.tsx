import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { ArrowRightIcon } from "@/components/ui/icons";

interface Entry {
  title: string;
  to: string;
  description: string;
  /** Every permission needed to open it. */
  permissions?: string[];
  /** Where it lives, when not under Settings. */
  where?: string;
}

interface Group {
  title: string;
  entries: Entry[];
}

/** Every setting, by what it is about. Settings that belong to an area
 * stay with it (health checks under Monitoring, enrichment under Network
 * Activity…): this page is the way to all of them, not a second editor. */
const groups: Group[] = [
  {
    title: "Your account",
    entries: [
      { title: "Account", to: "/settings/account", description: "Your password, two-factor authentication and appearance (light, dark, system)." },
      { title: "API tokens", to: "/settings/tokens", description: "Tokens for scripts and integrations to use the DROOL API as you." },
    ],
  },
  {
    title: "People and access",
    entries: [
      { title: "Users and roles", to: "/settings/users", description: "Who can sign in, and what each role may do.", permissions: ["users.manage"] },
      { title: "Credentials", to: "/credentials", where: "Protect", description: "SSH keys and passwords DROOL uses to reach hosts, and DROOL's SSH certificate authority.", permissions: ["credentials.use"] },
      { title: "Shepherd trust (PKI)", to: "/agents?tab=trust", where: "Shepherds", description: "The Agent Root CA, its intermediates and the trust manifest Shepherds check.", permissions: ["agents.read"] },
      { title: "Shepherd enrollment", to: "/agents?tab=enroll", where: "Shepherds", description: "Single-use tokens and installers for new Shepherds.", permissions: ["agents.enroll"] },
    ],
  },
  {
    title: "Alerts and notifications",
    entries: [
      { title: "Notification destinations and routing", to: "/settings/notifications", description: "Email, Discord, Slack, Telegram and webhooks: who is told about which alerts.", permissions: ["notifications.read"] },
      { title: "Maintenance windows", to: "/alerts?view=maintenance", where: "Alerts", description: "Pause notifications for hosts during planned work, now or on a schedule.", permissions: ["alerts.read"] },
    ],
  },
  {
    title: "Monitoring",
    entries: [
      { title: "Health checks", to: "/monitoring/settings", where: "Monitoring", description: "What counts as a problem — CPU, memory, disk space, services, temperatures — for every host, a Pack, a host or one device.", permissions: ["monitoring.read"] },
      { title: "Monitoring collection", to: "/monitoring/settings?advanced=1", where: "Monitoring → Health checks → Advanced", description: "How often hosts are observed, history retention, the Shepherd resource budget, SSH polling and helpers.", permissions: ["monitoring.read"] },
    ],
  },
  {
    title: "Network and firewall",
    entries: [
      { title: "Connection collection and GeoIP/ASN", to: "/network?tab=settings", where: "Network Activity", description: "Which hosts report their connections, how long they are kept, and country and network-owner lookups.", permissions: ["network.read"] },
      { title: "Firewall settings", to: "/firewall?tab=settings", where: "Firewall", description: "Which address ranges count as internal, and how long hosts wait to confirm a policy change before rolling it back.", permissions: ["firewall.read"] },
    ],
  },
  {
    title: "Shepherds (DROOL agents)",
    entries: [
      { title: "Shepherd updates", to: "/agents?tab=updates", where: "Shepherds", description: "Automatic updates and the update channel; rollout settings and custom releases under Advanced settings.", permissions: ["agents.read"] },
      { title: "Controller address", to: "/agents?tab=endpoints", where: "Shepherds", description: "The address Shepherds use to reach this controller.", permissions: ["agents.read"] },
    ],
  },
  {
    title: "This controller",
    entries: [
      { title: "System", to: "/settings/system", description: "Version, effective configuration, backup and restore." },
      { title: "Secure your installation", to: "/settings/system#secure", description: "What DROOL's security setup looks like and what to do about it.", permissions: ["settings.read"] },
      { title: "Audit Log", to: "/audit", where: "Protect", description: "Who did what, when; its integrity checks and its remote copy.", permissions: ["audit.read"] },
    ],
  },
];

/** Settings: every place DROOL is configured, grouped by what it is about,
 * each opening the page that edits it. */
export function SettingsDirectoryPage() {
  const { can } = useAuth();
  const shown = groups
    .map((g) => ({ ...g, entries: g.entries.filter((e) => (e.permissions ?? []).every((p) => can(p))) }))
    .filter((g) => g.entries.length > 0);
  return (
    <div className="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3" data-testid="settings-directory">
      {shown.map((g) => (
        <section key={g.title} className="rounded-ui border border-line bg-surface">
          <h2 className="border-b border-line px-4 py-2.5 text-sm font-semibold">{g.title}</h2>
          <ul className="divide-y divide-line">
            {g.entries.map((e) => (
              <li key={e.to}>
                <Link to={e.to} className="group flex items-start gap-3 px-4 py-3 hover:bg-surface-2">
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-baseline gap-x-2">
                      <span className="font-medium text-fg group-hover:underline">{e.title}</span>
                      {e.where && <span className="text-xs text-muted">in {e.where}</span>}
                    </span>
                    <span className="block text-xs text-muted">{e.description}</span>
                  </span>
                  <ArrowRightIcon className="mt-1 shrink-0 text-muted" aria-hidden="true" />
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
