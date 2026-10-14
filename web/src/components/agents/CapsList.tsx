import { Badge } from "@/components/ui/Badge";
import type { AgentCaps } from "./queries";

/** What a capability set allows, in plain words, with root-equivalent flags. */
export function capRows(c: AgentCaps): { name: string; value: string; on: boolean; rootEq?: boolean; reserved?: boolean }[] {
  const files = !c.files.enabled ? "no" : c.files.allowed_paths?.length ? `only ${c.files.allowed_paths.join(", ")}` : "the whole filesystem";
  const services = !c.services.enabled ? "no" : c.services.allowed?.length ? `only ${c.services.allowed.join(", ")}` : "all services";
  const packages = !c.packages.enabled ? "no" : c.packages.configured_repositories_only ? "from configured repositories only" : "any (unrestricted)";
  return [
    { name: "Monitoring", value: c.monitoring ? "yes" : "no", on: c.monitoring },
    { name: "Services", value: services, on: c.services.enabled },
    { name: "Reboot", value: c.reboot ? "yes" : "no", on: c.reboot },
    { name: "Packages", value: packages, on: c.packages.enabled, rootEq: c.packages.enabled && !c.packages.configured_repositories_only },
    { name: "Files", value: files, on: c.files.enabled, rootEq: c.files.enabled && (!c.files.allowed_paths?.length || c.files.allowed_paths.includes("/")) },
    { name: "Shell (commands, scripts)", value: c.shell ? "yes" : "no", on: c.shell, rootEq: c.shell },
    { name: "Firewall", value: c.firewall ? "yes (policy transactions, address sets)" : "no", on: c.firewall },
    { name: "Users", value: c.users ? "yes" : "no", on: c.users, rootEq: c.users, reserved: true },
  ];
}

export function isRootEquivalent(c: AgentCaps): boolean {
  return capRows(c).some((r) => r.rootEq);
}

/** A Shepherd's capabilities as the API has them: empty until the Shepherd
 * reports them when it first connects, moments after enrolling. */
export type ReportedCaps = AgentCaps | Record<string, never>;

export function capsReported(c: ReportedCaps | null | undefined): c is AgentCaps {
  return !!c && "packages" in c && "services" in c && "files" in c;
}

export function CapsList({ caps, compact }: { caps: ReportedCaps; compact?: boolean }) {
  if (!capsReported(caps)) {
    return <p className={compact ? "text-xs text-muted" : "text-sm text-muted"}>Not reported yet: the Shepherd sends what it may do when it connects.</p>;
  }
  const rows = capRows(caps);
  return (
    <div>
      {isRootEquivalent(caps) && (
        <div className="mb-2"><Badge tone="danger">ROOT-EQUIVALENT</Badge>{" "}
          <span className="text-xs text-muted">The controller can do anything root can on this host.</span></div>
      )}
      <dl className={compact ? "grid grid-cols-[7rem_1fr] sm:grid-cols-[10rem_1fr] gap-y-0.5 text-xs" : "grid grid-cols-[7rem_1fr] sm:grid-cols-[11rem_1fr] gap-y-1 text-sm"}>
        {rows.map((r) => (
          <div key={r.name} className="contents">
            <dt className="text-muted">{r.name}</dt>
            <dd className={r.on ? "" : "text-muted"}>
              {r.value}
              {r.rootEq && <span className="ml-2"><Badge tone="danger">root-equivalent</Badge></span>}
              {r.reserved && <span className="ml-2 text-xs text-muted">(reserved: no DROOL feature uses it yet)</span>}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
