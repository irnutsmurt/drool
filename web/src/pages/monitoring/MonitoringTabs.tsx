import { useLocation } from "react-router";
import { LocalNav } from "@/components/ui/LocalNav";

/** Monitoring: the fleet, and the settings that decide what is healthy. */
export function MonitoringTabs() {
  const { pathname } = useLocation();
  return (
    <LocalNav label="Monitoring" tabs={[
      { to: "/monitoring", label: "Fleet health", current: pathname === "/monitoring" },
      { to: "/monitoring/settings", label: "Health checks and settings", current: pathname === "/monitoring/settings" },
    ]} />
  );
}
