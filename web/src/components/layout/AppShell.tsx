import { NavLink, Outlet } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@/auth/AuthContext";
import { api, unwrap } from "@/api/client";
import { Button } from "@/components/ui/Button";
import { Badge } from "@/components/ui/Badge";
import { cx } from "@/components/ui/cx";
import { navItems } from "./nav";
import { usePKI } from "@/components/agents/queries";
import { Link } from "react-router";
import { ThemeSwitcher } from "@/components/ui/ThemeSwitcher";
import { useAlertSummary } from "@/components/alerts/queries";
import { StatusIcon } from "@/components/monitoring/Status";

export function useSystemInfo() {
  return useQuery({ queryKey: ["system", "info"], queryFn: () => unwrap(api.GET("/system/info")), staleTime: 5 * 60_000 });
}

export function AppShell() {
  const { me, can, logout } = useAuth();
  const info = useSystemInfo();
  const pki = usePKI(can("agents.read"));
  const alerts = useAlertSummary(can("alerts.read"));
  const open = alerts.data ? alerts.data.active : 0;

  return (
    <div className="flex h-full">
      <aside className="flex w-56 shrink-0 flex-col border-r border-line bg-surface">
        <div className="px-4 py-4 text-base font-semibold">DROOL</div>
        <nav className="flex flex-1 flex-col gap-0.5 px-2">
          {navItems
            .filter((i) => !i.permission || can(i.permission))
            .map((i) => (
              <NavLink
                key={i.to}
                to={i.to}
                end={i.to === "/"}
                className={({ isActive }) =>
                  cx(
                    "flex items-center justify-between rounded-ui px-3 py-2 text-sm",
                    isActive ? "bg-surface-2 font-medium text-fg" : "text-muted hover:bg-surface-2 hover:text-fg",
                  )
                }
              >
                {i.label}
                {i.milestone && <Badge>{i.milestone}</Badge>}
                {i.alerts && open > 0 && (
                  <span className="inline-flex items-center gap-1 text-xs font-semibold tabular-nums"
                    aria-label={`${open} unacknowledged alert${open === 1 ? "" : "s"}${alerts.data!.active_critical ? `, ${alerts.data!.active_critical} critical` : ""}`}>
                    <StatusIcon status={alerts.data!.active_critical ? "critical" : "warning"} />{open}
                  </span>
                )}
              </NavLink>
            ))}
        </nav>
        <div className="border-t border-line px-4 py-3 text-xs text-muted">
          {info.data ? info.data.version.replace(/^v?/, "v") : ""}
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        {info.data?.insecure_plaintext && (
          <div role="alert" className="bg-danger px-4 py-2 text-sm text-danger-fg">
            Security warning: this controller serves plaintext HTTP on a network address. Credentials and
            sessions are exposed to anyone on the path.
          </div>
        )}
        {pki.data?.root_key_online && (
          <div role="alert" className="bg-warn-bg px-4 py-2 text-sm text-warn">
            Security: the Agent Root CA private key is stored on this controller. Export it to offline storage and remove it
            {can("agents.pki") && <> — <Link className="underline" to="/agents?tab=trust">Agents → Trust</Link></>}.
          </div>
        )}
        <header className="flex h-14 items-center justify-end gap-3 border-b border-line bg-surface px-6">
          <ThemeSwitcher compact />
          <span className="text-sm">
            {me?.user.username} <span className="text-muted">({me?.user.role})</span>
          </span>
          <Button size="sm" variant="ghost" onClick={() => void logout()}>
            Sign out
          </Button>
        </header>
        <main className="min-w-0 flex-1 overflow-y-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
