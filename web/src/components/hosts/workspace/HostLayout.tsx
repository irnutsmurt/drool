import { useEffect, useState, type ReactNode } from "react";
import { Link, Outlet, useLocation, useNavigate, useOutletContext, useParams, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useHost, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { ManagementBadge } from "@/components/agents/ManagementBadge";
import { useHostAgent } from "@/components/agents/queries";
import { MaintenanceDialog } from "@/components/alerts/MaintenanceDialog";
import { useAlertSummary } from "@/components/alerts/queries";
import { untilText } from "@/components/alerts/ui";
import { HostReconciliationNotice } from "@/components/system/RestoreReconciliation";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { MenuButton, type MenuItem } from "@/components/ui/Menu";
import { LoadError, Loading } from "@/components/ui/States";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { HostStatusBadge } from "../HostStatusBadge";
import { TestConnectionDialog } from "../TestConnectionDialog";
import { RemoveHostDialog } from "../RemoveHostDialog";
import { ReachabilityBadge, osLabel } from "../health";
import { statusHelp } from "../statusHelp";
import { hostPath, sectionOf, visibleHostSections, type HostSection } from "./sections";

export interface HostWorkspace {
  host: Host;
}

/** The host whose workspace a section is rendered in. */
export const useHostWorkspace = () => useOutletContext<HostWorkspace>();

interface Crumb {
  label: ReactNode;
  to?: string;
}

/**
 * A host's workspace: every page about one host (/hosts/{id}/…) shares
 * this compact header — who the host is, how DROOL reaches it, whether it
 * is in trouble, and the common actions — and one row of local navigation,
 * so working on a machine never means losing it. The sections themselves
 * are the existing pages (monitoring, files, firewall…), rendered below.
 */
export function HostLayout() {
  const { id = "" } = useParams();
  const { pathname, search } = useLocation();
  const host = useHost(id);
  const base = `/hosts/${id}`;
  const rest = pathname.startsWith(base) ? pathname.slice(base.length) : "";
  const section = sectionOf(rest);

  if (host.error) {
    return (
      <>
        <Crumbs crumbs={[{ label: "Hosts", to: "/hosts" }, { label: "Host" }]} />
        <h1 className="mb-4 text-xl font-semibold">Host</h1>
        <LoadError what="the host" error={host.error} />
      </>
    );
  }
  const h = host.data;
  if (!h) return <><Crumbs crumbs={[{ label: "Hosts", to: "/hosts" }]} /><Loading what="the host" /></>;

  const crumbs: Crumb[] = [{ label: "Hosts", to: "/hosts" }];
  if (section.key === "overview") crumbs.push({ label: h.name });
  else {
    crumbs.push({ label: h.name, to: base });
    if (rest.startsWith("/files/edit")) {
      const file = new URLSearchParams(search).get("path") ?? "";
      const dir = file.slice(0, file.lastIndexOf("/")) || "/";
      crumbs.push({ label: "Files", to: hostPath(id, "files", `path=${encodeURIComponent(dir)}`) }, { label: file.split("/").pop() || file });
    } else crumbs.push({ label: section.label });
  }
  return (
    <div data-testid="host-workspace" data-host-id={h.id}>
      <HostHeader h={h} crumbs={crumbs} />
      <HostNotices h={h} section={section} />
      <HostSectionNav h={h} current={section} />
      <Outlet context={{ host: h } satisfies HostWorkspace} />
    </div>
  );
}

function Crumbs({ crumbs }: { crumbs: Crumb[] }) {
  return (
    <nav aria-label="Breadcrumb" className="mb-1 text-xs text-muted">
      <ol className="flex flex-wrap items-center gap-1">
        {crumbs.map((c, i) => (
          <li key={i} className="flex min-w-0 items-center gap-1">
            {i > 0 && <span aria-hidden="true">›</span>}
            {c.to ? <Link className="underline-offset-2 hover:text-fg hover:underline" to={c.to}>{c.label}</Link>
              : <span aria-current={i === crumbs.length - 1 ? "page" : undefined} className="truncate">{c.label}</span>}
          </li>
        ))}
      </ol>
    </nav>
  );
}

/** Name, state and the facts that tell hosts apart; Run command and
 * Install updates in sight, everything else under Actions. */
function HostHeader({ h, crumbs }: { h: Host; crumbs: Crumb[] }) {
  const { can } = useAuth();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [dialog, setDialog] = useState<"test" | "maintenance" | "remove" | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");
  const usable = h.status === "ready" || h.status === "unsupported";
  const managed = !h.jump_only;
  const canRun = can("jobs.execute");

  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/refresh", { params: { path: { id: h.id } } })),
    onSuccess: () => {
      setNote("Inventory and status are being collected now.");
      void qc.invalidateQueries({ queryKey: qk.host(h.id) });
      void qc.invalidateQueries({ queryKey: qk.hosts });
    },
  });
  useEffect(() => {
    if (!note) return;
    const t = setTimeout(() => setNote(null), 8000);
    return () => clearTimeout(t);
  }, [note]);

  const items: MenuItem[] = [];
  // Phones show only the primary action and this menu.
  if (canRun && managed) items.push({ label: "Install updates", to: `/jobs/new?hosts=${h.id}&op=system_update`, narrowOnly: true });
  if (canRun && managed) items.push({ label: "Reboot…", hint: "Opens the run with a preview first", to: `/jobs/new?hosts=${h.id}&op=reboot` });
  if (canRun && managed && h.pinned_key && sshHost) {
    items.push({ label: refresh.isPending ? "Refreshing…" : "Refresh inventory", hint: "Collect facts and status now", disabled: refresh.isPending, onSelect: () => refresh.mutate() });
  }
  if (canRun && sshHost) items.push({ label: "Test connection", hint: needsKey ? "Approve the host key first" : "SSH sign-in and sudo, step by step", disabled: needsKey, onSelect: () => setDialog("test") });
  if (can("maintenance.manage") && managed) items.push({ label: "Start maintenance…", hint: "Pause notifications during planned work", onSelect: () => setDialog("maintenance") });
  if (can("hosts.write")) {
    items.push({ label: "Edit host details", hint: "Name, address, sign-in, tags", to: hostPath(h.id, "management", "edit=1"), separated: true });
    items.push({ label: "Remove host…", danger: true, onSelect: () => setDialog("remove") });
  }

  return (
    <header className="mb-3" data-testid="host-header">
      <Crumbs crumbs={crumbs} />
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <div className="min-w-0 flex-1 basis-[24rem]">
          <h1 className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xl font-semibold">
            <span className="break-all">{h.name}</span>
            <HostStatusBadge status={h.status} />
          </h1>
          <HostFacts h={h} />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {note && <span role="status" className="text-xs text-muted">{note}</span>}
          {canRun && managed && usable && <Button variant="primary" onClick={() => navigate(`/jobs/new?hosts=${h.id}`)}>Run command</Button>}
          {canRun && sshHost && !usable && (
            <Button variant="primary" onClick={() => setDialog("test")} disabled={needsKey} title={needsKey ? "Approve the host key first (Overview)" : undefined}>Test connection</Button>
          )}
          {canRun && managed && <Button className="max-sm:hidden" onClick={() => navigate(`/jobs/new?hosts=${h.id}&op=system_update`)}>Install updates</Button>}
          {items.length > 0 && <MenuButton label="Actions" items={items} testId="host-actions" />}
        </div>
      </div>
      {refresh.error && <div className="mt-2"><Alert>{errorMessage(refresh.error)}</Alert></div>}
      {dialog === "test" && <TestConnectionDialog host={h} onClose={() => setDialog(null)} />}
      {dialog === "maintenance" && <MaintenanceDialog hostIds={[h.id]} onClose={() => setDialog(null)} />}
      {dialog === "remove" && (
        <RemoveHostDialog hostId={h.id} hostName={h.name} onClose={() => setDialog(null)}
          onRemoved={() => {
            void qc.invalidateQueries({ queryKey: qk.hosts });
            navigate("/hosts");
          }} />
      )}
    </header>
  );
}

/** One line of what tells this host apart and whether it's reachable:
 * OS, address, how DROOL manages it, reachability, open alerts and
 * maintenance. Old data never reads as healthy: reachability says
 * stale or not checked. */
function HostFacts({ h }: { h: Host }) {
  const { can } = useAuth();
  const agent = useHostAgent(h.id);
  const alerts = useAlertSummary(can("alerts.read"));
  const m = agent.data?.management ?? h.management;
  const a = alerts.data?.hosts[h.id];
  const open = a ? a.critical + a.warning : 0;
  const maint = alerts.data?.maintenance[h.id];
  const parts: ReactNode[] = [];
  if (!h.jump_only) parts.push(<span key="os">{osLabel(h)}</span>);
  else parts.push(<span key="jump">Jump host only</span>);
  parts.push(<span key="addr" className="font-mono">{h.address}{h.port !== 22 && `:${h.port}`}</span>);
  if (!h.jump_only) {
    parts.push(
      <span key="mgmt" className="inline-flex flex-wrap items-center gap-1.5">
        <span className="text-muted">Managed via</span>
        <ManagementBadge m={m} />
        {m.preferred === "agent" && m.fallback === "ssh" && m.active === "agent" && <span className="text-muted">with SSH fallback</span>}
      </span>,
    );
    parts.push(<span key="reach" className="inline-flex items-center gap-1.5"><span className="text-muted">Last check</span><ReachabilityBadge host={h} /></span>);
  }
  if (open > 0) {
    parts.push(
      <Link key="alerts" to={`/alerts?host_id=${h.id}`} className={cx("inline-flex items-center gap-1 font-medium underline-offset-2 hover:underline", a!.critical ? "text-danger" : "text-warn")}>
        <span aria-hidden="true">{a!.critical ? "✕" : "⚠"}</span>
        {open} open alert{open === 1 ? "" : "s"}{a!.critical ? ` (${a!.critical} critical)` : ""}
      </Link>,
    );
  }
  if (maint) parts.push(<Badge key="maint" tone="warn">In maintenance {untilText(maint.ends_at)}</Badge>);
  return (
    <div className="mt-1 flex flex-wrap items-center gap-x-5 gap-y-1 text-sm" data-testid="host-facts">
      {parts}
    </div>
  );
}

/** Warnings that matter wherever you are in the workspace: DROOL can't
 * reach or trust the host, or a restore blocked it. Kept visible on every
 * section — a smaller header never hides them; outside the Overview, in
 * one line with the way to the details. */
function HostNotices({ h, section }: { h: Host; section: HostSection }) {
  const help = statusHelp(h);
  const onOverview = section.key === "overview";
  const detail = h.status_detail ? ` (${h.status_detail})` : "";
  const overview = <Link className="underline underline-offset-2" to={`/hosts/${h.id}`}>Overview</Link>;
  const management = <Link className="underline underline-offset-2" to={hostPath(h.id, "management")}>Management</Link>;
  let line: ReactNode = null;
  if (help && !onOverview) {
    switch (h.status) {
      case "pending_hostkey":
        line = <>DROOL won&apos;t connect until you approve this host&apos;s SSH key: review it on the {overview}.</>;
        break;
      case "hostkey_mismatch":
        line = <>The host presented a different SSH key than the one you approved: DROOL is not connecting to it. Check it on the {overview}.</>;
        break;
      case "unreachable":
        line = <>Unreachable: the last attempt couldn&apos;t connect{detail}. DROOL keeps checking by itself.</>;
        break;
      case "auth_failed":
        line = <>Access denied: DROOL reached the host but couldn&apos;t sign in or use sudo{detail}. See {management}.</>;
        break;
      case "untested":
        line = <>Not tested yet: use Test connection (Actions) before working on this host.</>;
        break;
      default:
        line = help.text;
    }
  }
  return (
    <div className="space-y-2 empty:hidden [&:not(:empty)]:mb-3" data-testid="host-notices">
      <HostReconciliationNotice hostId={h.id} />
      {help && onOverview && !(h.status === "pending_hostkey" || h.status === "hostkey_mismatch") && <Alert tone={help.tone}>{help.text}</Alert>}
      {line && <Alert tone={help!.tone}>{line}</Alert>}
    </div>
  );
}

/** The workspace's own navigation: a row of links on wider screens, a
 * section selector on phones (eight links don't fit in one row there). */
function HostSectionNav({ h, current }: { h: Host; current: HostSection }) {
  const { can } = useAuth();
  const navigate = useNavigate();
  const sections = visibleHostSections(h, can);
  return (
    <>
      <nav aria-label={`${h.name}: sections`} className="mb-4 hidden overflow-x-auto md:block" data-testid="host-sections">
        <ul className="flex min-w-max gap-1 border-b border-line">
          {sections.map((s) => (
            <li key={s.key}>
              <Link to={hostPath(h.id, s.path)} aria-current={s.key === current.key ? "page" : undefined} title={s.description}
                className={cx("-mb-px inline-block whitespace-nowrap border-b-2 px-3 py-2 text-sm",
                  s.key === current.key ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")}>
                {s.label}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <div className="mb-4 md:hidden" data-testid="host-section-select">
        <label className="flex items-center gap-2 text-sm">
          <span className="shrink-0 text-muted">Section</span>
          <select value={current.key} onChange={(e) => {
            const s = sections.find((x) => x.key === e.target.value);
            if (s) navigate(hostPath(h.id, s.path));
          }} className="h-10 min-w-0 flex-1 rounded-ui border border-line bg-surface px-3 text-sm font-medium text-fg">
            {sections.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
          </select>
        </label>
      </div>
    </>
  );
}

/** Opening /hosts/{id}?…#management (old links) lands on Management. */
export function useLegacyHostAnchors(id: string) {
  const { hash } = useLocation();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  useEffect(() => {
    if (hash === "#management") navigate(hostPath(id, "management", params.toString()), { replace: true });
  }, [hash, id, navigate, params]);
}
