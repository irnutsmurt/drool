import { Outlet, useLocation } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { LocalNav } from "@/components/ui/LocalNav";
import { PageHeader } from "@/components/ui/PageHeader";

const tabs = [
  { to: "/settings", label: "All settings", exact: true },
  { to: "/settings/account", label: "Account" },
  { to: "/settings/tokens", label: "API tokens" },
  { to: "/settings/users", label: "Users", permission: "users.manage" },
  { to: "/settings/notifications", label: "Notifications", permission: "notifications.read" },
  { to: "/settings/system", label: "System" },
];

/** Settings: a directory of every place DROOL is configured, then your
 * account and the controller's own configuration. Settings that belong
 * to an area live with it (health checks under Monitoring); the directory
 * links there. */
export function SettingsLayout() {
  const { can } = useAuth();
  const { pathname } = useLocation();
  return (
    <>
      <PageHeader title="Settings" description="Every place DROOL is configured: yours, the people and access, notifications, the controller — and the settings that live with their area (monitoring, network, firewall, Shepherds)." />
      <LocalNav label="Settings" tabs={tabs.filter((t) => !("permission" in t) || !t.permission || can(t.permission))
        .map((t) => ({ to: t.to, label: t.label, current: "exact" in t && t.exact ? pathname === t.to || pathname === t.to + "/" : pathname.startsWith(t.to) }))} />
      <Outlet />
    </>
  );
}
