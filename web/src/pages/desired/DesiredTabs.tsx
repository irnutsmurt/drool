import { Link, useLocation } from "react-router";
import { cx } from "@/components/ui/cx";

/** Overview, Baselines and History of desired state. */
export function DesiredTabs() {
  const { pathname } = useLocation();
  const tabs = [
    { to: "/desired", label: "Overview" },
    { to: "/desired/baselines", label: "Baselines" },
    { to: "/desired/history", label: "History" },
  ];
  return (
    <nav aria-label="Desired state" className="mb-4 flex flex-wrap gap-1 border-b border-line">
      {tabs.map((t) => {
        const current = t.to === "/desired" ? pathname === "/desired" : pathname.startsWith(t.to);
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
