import { Outlet, useLocation } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { LocalNav } from "@/components/ui/LocalNav";
import { PageHeader } from "@/components/ui/PageHeader";

const tabs = [
  { to: "/settings/account", label: "Account" },
  { to: "/settings/tokens", label: "API tokens" },
  { to: "/settings/users", label: "Users", permission: "users.manage" },
  { to: "/settings/notifications", label: "Notifications", permission: "notifications.read" },
  { to: "/settings/system", label: "System" },
];

/** Your account and the controller's own configuration. Settings that
 * belong to an area live with it (health checks under Monitoring). */
export function SettingsLayout() {
  const { can } = useAuth();
  const { pathname } = useLocation();
  return (
    <>
      <PageHeader title="Settings" description="Your account, API tokens, users, notification destinations and the controller." />
      <LocalNav label="Settings" tabs={tabs.filter((t) => !t.permission || can(t.permission)).map((t) => ({ ...t, current: pathname.startsWith(t.to) }))} />
      <Outlet />
    </>
  );
}
