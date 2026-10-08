import { NavLink, Outlet } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";

const tabs = [
  { to: "account", label: "Account" },
  { to: "tokens", label: "API tokens" },
  { to: "users", label: "Users", permission: "users.manage" },
  { to: "monitoring", label: "Monitoring", permission: "monitoring.read" },
  { to: "system", label: "System" },
];

export function SettingsLayout() {
  const { can } = useAuth();
  return (
    <>
      <PageHeader title="Settings" />
      <nav className="mb-6 flex gap-1 border-b border-line">
        {tabs
          .filter((t) => !t.permission || can(t.permission))
          .map((t) => (
            <NavLink
              key={t.to}
              to={t.to}
              className={({ isActive }) =>
                cx("-mb-px border-b-2 px-3 py-2 text-sm",
                  isActive ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")
              }
            >
              {t.label}
            </NavLink>
          ))}
      </nav>
      <Outlet />
    </>
  );
}
