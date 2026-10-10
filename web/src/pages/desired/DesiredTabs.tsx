import { useLocation } from "react-router";
import { LocalNav } from "@/components/ui/LocalNav";

/** Overview, Baselines and History of desired state. */
export function DesiredTabs() {
  const { pathname } = useLocation();
  return (
    <LocalNav label="Desired State" tabs={[
      { to: "/desired", label: "Overview", current: pathname === "/desired" },
      { to: "/desired/baselines", label: "Baselines", current: pathname.startsWith("/desired/baselines") },
      { to: "/desired/history", label: "History", current: pathname.startsWith("/desired/history") },
    ]} />
  );
}
