import type { Schemas } from "@/api/client";
import { Badge } from "@/components/ui/Badge";
import { cx } from "@/components/ui/cx";
import { FileDeployResult, type DeployResult } from "@/components/files/FileDeployResult";

type Change = Schemas["PackageChange"];
type Plan = Schemas["PackagePlan"];

export function ChangeList({ items, highlight }: { items: Change[]; highlight?: Set<string> }) {
  return (
    <ul className="mt-0.5 max-h-60 overflow-y-auto font-mono text-xs">
      {items.map((c) => (
        <li key={c.name} className={cx(highlight?.has(c.name) && "font-semibold")}>
          {c.name}
          {(c.from || c.to) && (
            <span className="text-muted"> {c.from && c.to ? `${c.from} → ${c.to}` : c.from || c.to}</span>
          )}
        </li>
      ))}
    </ul>
  );
}

/** One-line description of a step for the job page. */
export function describeStep(s: Schemas["Operation"]): string {
  switch (s.type) {
    case "command":
      return s.command;
    case "package":
      return `${{ install: "Install", remove: "Remove", upgrade: "Upgrade" }[s.action]} packages: ${s.names.join(" ")}` +
        (s.allow_removals ? " · other removals allowed" : "");
    case "system_update":
      return [
        { upgrade: "System update (upgrade)", full_upgrade: "System update (full upgrade)", security: "Security updates only" }[s.mode ?? "upgrade"],
        s.refresh === false ? "no metadata refresh" : "refresh metadata first",
        { never: "no reboot", if_required: "reboot if required", always: "always reboot" }[s.reboot ?? "never"],
        s.allow_removals ? "removals allowed" : "",
      ].filter(Boolean).join(" · ");
    case "service": {
      const parts = [];
      if (s.state) parts.push({ running: "start", stopped: "stop", restarted: "restart", reloaded: "reload" }[s.state]);
      if (s.enabled != null) parts.push(s.enabled ? "enable at boot" : "disable at boot");
      return `Service ${s.name}: ${parts.join(", ")}`;
    }
    case "reboot":
      return s.wait === false ? "Reboot (don't wait)" : `Reboot and wait up to ${s.timeout_s ?? 900}s for the host`;
    case "wait":
      return `Wait ${secs(s.seconds)}`;
    case "verify_host":
      return `Verify the host is reachable (keep trying up to ${secs(s.timeout_s ?? 300)})`;
    case "verify_services":
      return `Verify services are running: ${(s.services ?? []).join(", ") || "(none named yet)"}`;
    case "verify_health":
      return `Verify health: no active ${s.policy === "no_warning" ? "Warning or Critical" : "Critical"} issues`;
    case "ssh_ca_trust":
      return s.action === "remove" ? "Remove trust in DROOL's SSH certificate authority"
        : "Trust DROOL's SSH certificate authority (install in sshd, then check with a certificate on a fresh connection)";
    case "file_deploy":
      if (s.rollback_of) return s.remove ? `Roll back ${s.path} (remove it)` : `Roll back ${s.path} to its previous version`;
      return [`Deploy ${s.path}`, s.elevated ? "as root" : "", s.owner || s.group ? `owner ${s.owner ?? "keep"}:${s.group ?? "keep"}` : "",
        s.mode ? `mode ${s.mode}` : "", s.validate ? `validate: ${s.validate}` : "",
        s.service ? `then ${s.service.action} ${s.service.name}` : "", s.overwrite === "create_only" ? "only if missing" : ""]
        .filter(Boolean).join(" · ");
    default:
      return "";
  }
}

interface UnitState { load: string; active: string; sub: string; unit_file: string }
interface RebootResult { boot_id_before?: string; boot_id_after?: string; down_seconds?: number; total_seconds?: number }

const secs = (n?: number) => (n == null ? "" : n < 90 ? `${Math.round(n)}s` : n % 60 === 0 ? `${n / 60} min`
  : `${Math.floor(n / 60)}m ${Math.round(n % 60)}s`);

function RebootSummary({ r }: { r: RebootResult }) {
  return (
    <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[9rem_1fr] gap-y-1 text-sm">
      <dt className="text-muted">Boot id before</dt><dd className="font-mono text-xs">{r.boot_id_before ?? "—"}</dd>
      <dt className="text-muted">Boot id after</dt><dd className="font-mono text-xs">{r.boot_id_after ?? "— (not confirmed)"}</dd>
      {r.total_seconds != null && (<><dt className="text-muted">Back after</dt><dd>{secs(r.total_seconds)}{r.down_seconds != null && ` (unreachable ${secs(r.down_seconds)})`}</dd></>)}
    </dl>
  );
}

/** Summary of a typed step's result (package plan, service state, reboot). */
export function StepResult({ step }: { step: Schemas["JobStep"] }) {
  const r = step.result as Record<string, unknown> | undefined;
  if (!r || Object.keys(r).length === 0) return null;
  if (step.type === "file_deploy") return <FileDeployResult r={r as unknown as DeployResult} />;
  const plan = r.plan as Plan | undefined;
  const changes = r.changes as Change[] | undefined;
  const before = r.before as UnitState | undefined;
  const after = r.after as UnitState | undefined;
  const reboot = r.reboot as RebootResult | undefined;
  const services = r.services as Record<string, string> | undefined;
  const changedBadge = step.changed == null ? null : step.changed
    ? <Badge tone="accent">changed</Badge> : <Badge>no change</Badge>;

  return (
    <div className="mb-3 space-y-3 rounded-ui border border-line p-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {changedBadge}
        {typeof r.package_manager === "string" && <span className="text-muted">{r.package_manager}</span>}
        {typeof r.unit === "string" && <span className="font-mono text-xs">{r.unit}</span>}
        {r.reboot_required === true && <Badge tone="warn">reboot required</Badge>}
        {typeof r.reboot_reason === "string" && r.reboot_reason && <span className="text-xs text-muted">{r.reboot_reason}</span>}
      </div>
      {plan && (
        <div className="grid gap-3 text-sm sm:grid-cols-2">
          {plan.remove.length > 0 && <div className="text-danger"><div className="text-xs font-medium uppercase">Removed ({plan.remove.length})</div><ChangeList items={plan.remove} /></div>}
          {plan.install.length > 0 && <div><div className="text-xs font-medium uppercase text-muted">Installed ({plan.install.length})</div><ChangeList items={plan.install} /></div>}
          {plan.upgrade.length > 0 && <div><div className="text-xs font-medium uppercase text-muted">Upgraded ({plan.upgrade.length})</div><ChangeList items={plan.upgrade} /></div>}
          {plan.downgrade.length > 0 && <div><div className="text-xs font-medium uppercase text-muted">Downgraded ({plan.downgrade.length})</div><ChangeList items={plan.downgrade} /></div>}
          {plan.remove.length + plan.install.length + plan.upgrade.length + plan.downgrade.length === 0 && <p className="text-muted">Nothing to do.</p>}
        </div>
      )}
      {changes && changes.length > 0 && !plan && <ChangeList items={changes} />}
      {before && (
        <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[9rem_1fr] gap-y-1 text-sm">
          <dt className="text-muted">Before</dt><dd>{before.active} ({before.sub}), {before.unit_file || "—"}</dd>
          {after && (<><dt className="text-muted">After</dt><dd>{after.active} ({after.sub}), {after.unit_file || "—"}</dd></>)}
          {Array.isArray(r.actions) && r.actions.length > 0 && (<><dt className="text-muted">Ran</dt><dd className="font-mono text-xs">{(r.actions as string[]).map((a) => `systemctl ${a}`).join("; ")}</dd></>)}
        </dl>
      )}
      {reboot && <RebootSummary r={reboot} />}
      {services && Object.keys(services).length > 0 && (
        <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[12rem_1fr] gap-y-1 text-sm">
          {Object.entries(services).map(([name, st]) => (
            <div key={name} className="contents">
              <dt className="font-mono text-xs">{name}</dt>
              <dd className={st.startsWith("active") ? "text-ok" : "text-danger"}>{st}</dd>
            </div>
          ))}
        </dl>
      )}
      {typeof r.detail === "string" && <p className="text-sm text-muted">{r.detail}</p>}
    </div>
  );
}
