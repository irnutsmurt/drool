import { Link, useLocation } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { cx } from "@/components/ui/cx";

/** Runs, Schedules and Workflows: one area, no new sidebar items. */
export function JobsTabs() {
  const { can } = useAuth();
  const { pathname } = useLocation();
  const tabs = [
    { to: "/jobs", label: "Runs" },
    ...(can("schedules.read") ? [{ to: "/jobs/schedules", label: "Schedules" }] : []),
    ...(can("workflows.read") ? [{ to: "/jobs/workflows", label: "Workflows" }] : []),
  ];
  if (tabs.length === 1) return null;
  return (
    <nav aria-label="Jobs" className="mb-4 flex flex-wrap gap-1 border-b border-line">
      {tabs.map((t) => {
        const current = t.to === "/jobs" ? pathname === "/jobs" : pathname.startsWith(t.to);
        return (
          <Link key={t.to} to={t.to} aria-current={current ? "page" : undefined}
            className={cx("-mb-px border-b-2 px-3 py-2 text-sm", current ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")}>
            {t.label}
          </Link>
        );
      })}
    </nav>
  );
}
