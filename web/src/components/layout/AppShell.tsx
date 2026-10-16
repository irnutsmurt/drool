import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link, Outlet, useLocation } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Button } from "@/components/ui/Button";
import { ConfirmHost } from "@/components/ui/ConfirmDialog";
import { cx } from "@/components/ui/cx";
import {
  BellIcon, ChevronDownIcon, ChipIcon, ClockIcon, CloseIcon, GearIcon, HomeIcon, KeyIcon, LogIcon, MenuIcon, NetworkIcon, PackIcon, PlayIcon, PulseIcon,
  SearchIcon, ServerIcon, ShieldIcon, StepsIcon, TargetIcon, WallIcon,
} from "@/components/ui/icons";
import { ThemeSwitcher } from "@/components/ui/ThemeSwitcher";
import { useAlertSummary } from "@/components/alerts/queries";
import { StatusIcon } from "@/components/monitoring/Status";
import { isActive, visibleSections, type NavIcon, type NavItem } from "./nav";
import { CommandPalette, shortcutLabel, typingIn } from "./CommandPalette";
import { useSecurityItems, useSystemInfo, type SecurityItem } from "./security";

export { useSystemInfo } from "./security";

const focusable = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function AppShell() {
  const { me, logout } = useAuth();
  const info = useSystemInfo();
  const location = useLocation();
  const [navOpen, setNavOpen] = useState(false);
  const [palette, setPalette] = useState(false);
  const closePalette = useCallback(() => setPalette(false), []);

  // Navigating closes the narrow-screen menu.
  useEffect(() => setNavOpen(false), [location.pathname, location.search]);

  // Ctrl+K / ⌘K anywhere; "/" when not typing in a field.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((p) => !p);
      } else if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey && !typingIn(e.target)
        && !document.querySelector('[role="dialog"][aria-modal="true"]')) {
        e.preventDefault();
        setPalette(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="flex h-full">
      <a href="#main" className="sr-only z-[70] rounded-ui bg-accent px-3 py-2 text-sm font-medium text-accent-fg focus:not-sr-only focus:fixed focus:left-2 focus:top-2">
        Skip to content
      </a>
      <aside className="hidden w-56 shrink-0 flex-col border-r border-line bg-surface lg:flex">
        <Link to="/" className="px-4 py-4 text-base font-semibold">DROOL</Link>
        <SidebarNav />
        <div className="border-t border-line px-4 py-3 text-xs text-muted">{info.data ? info.data.version.replace(/^v?/, "v") : ""}</div>
      </aside>
      {navOpen && <MobileNav version={info.data?.version} onClose={() => setNavOpen(false)} />}

      <div className="flex min-w-0 flex-1 flex-col">
        {info.data?.insecure_plaintext && (
          <div role="alert" className="bg-danger px-4 py-2 text-sm text-danger-fg">
            Security warning: this controller serves plaintext HTTP on a network address. Credentials and
            sessions are exposed to anyone on the path.
          </div>
        )}
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line bg-surface px-3 sm:px-4 lg:px-6">
          <button type="button" className="-ml-1 inline-flex h-9 w-9 items-center justify-center rounded-ui text-fg hover:bg-surface-2 lg:hidden"
            aria-label="Open navigation" aria-expanded={navOpen} aria-controls="mobile-nav" onClick={() => setNavOpen(true)}>
            <MenuIcon width={20} height={20} />
          </button>
          <Link to="/" className="mr-1 font-semibold lg:hidden">DROOL</Link>
          <button type="button" onClick={() => setPalette(true)} aria-keyshortcuts="Control+K Meta+K /" data-testid="search-button"
            className="inline-flex h-8 items-center gap-2 whitespace-nowrap rounded-ui border border-line bg-surface px-2 text-sm text-muted hover:bg-surface-2 hover:text-fg md:w-80 md:px-2.5">
            <SearchIcon className="shrink-0" />
            <span className="sr-only md:not-sr-only md:truncate">Search hosts, services, pages…</span>
            <kbd className="ml-auto hidden rounded border border-line px-1 text-[11px] md:inline">{shortcutLabel()}</kbd>
          </button>
          <div className="ml-auto flex items-center gap-2 sm:gap-3">
            <SecurityStatus />
            <div className="hidden md:block"><ThemeSwitcher compact /></div>
            <span className="hidden text-sm md:inline">
              {me?.user.username} <span className="text-muted">({me?.user.role})</span>
            </span>
            <span className="hidden md:inline-flex"><Button size="sm" variant="ghost" onClick={() => void logout()}>Sign out</Button></span>
            <AccountMenu />
          </div>
        </header>
        <main id="main" tabIndex={-1} className="min-w-0 flex-1 overflow-y-auto p-4 outline-none lg:p-6">
          <Outlet />
        </main>
      </div>
      {palette && <CommandPalette onClose={closePalette} />}
      <ConfirmHost />
    </div>
  );
}

/** The sidebar's destinations under their (non-clickable) headings. */
function SidebarNav({ onNavigate, idPrefix = "nav" }: { onNavigate?: () => void; idPrefix?: string }) {
  const { can } = useAuth();
  const { pathname } = useLocation();
  const alerts = useAlertSummary(can("alerts.read"));
  const sections = visibleSections(can);
  return (
    <nav aria-label="Main" className="flex-1 overflow-y-auto px-2 pb-3">
      {sections.map((s) => (
        <div key={s.id} className={s.apart ? "mt-3 border-t border-line pt-3" : s.heading ? "mt-3" : ""}>
          {s.heading && <div id={`${idPrefix}-${s.id}`} className="px-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-muted">{s.heading}</div>}
          <ul aria-labelledby={s.heading ? `${idPrefix}-${s.id}` : undefined} className="flex flex-col gap-px">
            {s.items.map((i) => (
              <li key={i.to}>
                <NavEntry item={i} active={isActive(i, pathname)} onNavigate={onNavigate}
                  alerts={i.alerts && alerts.data ? { open: alerts.data.active, critical: alerts.data.active_critical } : undefined} />
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}

/** One destination. Themed names show their conventional term beside them
 * and explain themselves on hover and on keyboard focus. */
function NavEntry({ item: i, active, alerts, onNavigate }: {
  item: NavItem; active: boolean; alerts?: { open: number; critical: number }; onNavigate?: () => void;
}) {
  const [tip, setTip] = useState<DOMRect | null>(null);
  const descId = useId();
  const show = (e: React.SyntheticEvent<HTMLElement>) => i.hint && setTip(e.currentTarget.getBoundingClientRect());
  const hide = () => setTip(null);
  return (
    <>
      <Link to={i.to} aria-current={active ? "page" : undefined} aria-describedby={i.hint ? descId : undefined} onClick={onNavigate}
        onMouseEnter={show} onMouseLeave={hide} onFocus={show} onBlur={hide}
        className={cx("relative flex items-center gap-2.5 rounded-ui px-3 py-1.5 text-sm",
          active ? "bg-surface-2 font-medium text-fg before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-accent"
            : "text-muted hover:bg-surface-2 hover:text-fg")}>
        <NavGlyph icon={i.icon} active={active} />
        <span>{i.label}</span>
        {i.hint && <span className="ml-auto whitespace-nowrap text-xs font-normal text-muted">{i.hint}</span>}
        {alerts && alerts.open > 0 && (
          <span className="ml-auto inline-flex items-center gap-1 text-xs font-semibold tabular-nums"
            aria-label={`${alerts.open} unacknowledged alert${alerts.open === 1 ? "" : "s"}${alerts.critical ? `, ${alerts.critical} critical` : ""}`}>
            <StatusIcon status={alerts.critical ? "critical" : "warning"} />{alerts.open}
          </span>
        )}
      </Link>
      {i.hint && <span id={descId} className="sr-only">{i.description}</span>}
      {tip && createPortal(<NavTip rect={tip} text={`${i.label}: ${i.description}`} />, document.body)}
    </>
  );
}

const glyphs: Record<NavIcon, (p: React.SVGProps<SVGSVGElement>) => React.ReactElement> = {
  home: HomeIcon, hosts: ServerIcon, packs: PackIcon, shepherds: ChipIcon, monitoring: PulseIcon, alerts: BellIcon, network: NetworkIcon,
  runs: PlayIcon, schedules: ClockIcon, workflows: StepsIcon, desired: TargetIcon, firewall: WallIcon, credentials: KeyIcon, audit: LogIcon,
  settings: GearIcon,
};

function NavGlyph({ icon, active }: { icon: NavIcon; active: boolean }) {
  const G = glyphs[icon];
  return <G width={16} height={16} className={cx("shrink-0", active ? "text-accent" : "opacity-80")} />;
}

function NavTip({ rect, text }: { rect: DOMRect; text: string }) {
  // Beside the entry when there is room (desktop), else below it (narrow menu).
  const beside = rect.right + 8 + 260 <= window.innerWidth;
  const style: React.CSSProperties = beside
    ? { position: "fixed", left: rect.right + 8, top: rect.top + rect.height / 2, transform: "translateY(-50%)" }
    : { position: "fixed", left: Math.max(8, rect.left + 12), top: rect.bottom + 4 };
  return (
    <div aria-hidden="true" data-testid="nav-tip" style={style}
      className="pointer-events-none z-[80] w-60 rounded-ui border border-line bg-surface px-2.5 py-1.5 text-xs text-fg shadow-lg">
      {text}
    </div>
  );
}

/** The sidebar as an off-canvas menu on narrow screens: focus moves in and
 * stays there, Escape or the backdrop closes it, focus returns to the
 * menu button. */
function MobileNav({ onClose, version }: { onClose: () => void; version?: string }) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    panel.current?.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "Tab" && panel.current) {
        const items = [...panel.current.querySelectorAll<HTMLElement>(focusable)].filter((x) => x.offsetParent !== null);
        if (items.length === 0) return;
        const [a, z] = [items[0]!, items[items.length - 1]!];
        if (e.shiftKey && document.activeElement === a) {
          z.focus();
          e.preventDefault();
        } else if (!e.shiftKey && document.activeElement === z) {
          a.focus();
          e.preventDefault();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      before?.focus?.();
    };
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-50 lg:hidden">
      <div className="absolute inset-0 bg-[var(--ui-backdrop)]" onClick={onClose} aria-hidden="true" />
      <div ref={panel} id="mobile-nav" role="dialog" aria-modal="true" aria-label="Navigation"
        className="absolute inset-y-0 left-0 flex w-72 max-w-[85vw] flex-col border-r border-line bg-surface shadow-lg">
        <div className="flex items-center justify-between px-4 py-3">
          <span className="text-base font-semibold">DROOL</span>
          <button type="button" data-autofocus aria-label="Close navigation" onClick={onClose}
            className="inline-flex h-9 w-9 items-center justify-center rounded-ui hover:bg-surface-2">
            <CloseIcon width={20} height={20} />
          </button>
        </div>
        <SidebarNav onNavigate={onClose} idPrefix="mnav" />
        <div className="border-t border-line px-4 py-3 text-xs text-muted">{version ? version.replace(/^v?/, "v") : ""}</div>
      </div>
    </div>
  );
}

/** A compact, persistent security indicator: expands to the full
 * explanation. Shown while anything is unresolved; never dismissed. */
function SecurityStatus() {
  const items = useSecurityItems();
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);
  if (items.length === 0) return null;
  const danger = items.some((x) => x.tone === "danger");
  const label = `${items.length} security issue${items.length === 1 ? "" : "s"}`;
  return (
    <div ref={box} className="relative">
      <button type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)} data-testid="security-status"
        className={cx("inline-flex h-8 items-center gap-1.5 rounded-ui border px-2 text-xs font-medium",
          danger ? "border-danger/40 bg-danger-bg text-danger" : "border-warn/40 bg-warn-bg text-warn")}>
        <ShieldIcon />
        <span className="sr-only sm:not-sr-only">{label}</span>
        <ChevronDownIcon className="hidden sm:block" width={12} height={12} />
      </button>
      {open && (
        <div id={id} role="region" aria-label="Security status"
          className="absolute right-0 top-full z-40 mt-2 w-[min(26rem,calc(100vw-1.5rem))] space-y-3 rounded-ui border border-line bg-surface p-3 text-sm shadow-lg">
          {items.map((x) => <SecurityNote key={x.id} item={x} />)}
          <p className="text-xs text-muted">Shown until it is fixed. The Overview lists it too.</p>
        </div>
      )}
    </div>
  );
}

export function SecurityNote({ item }: { item: SecurityItem }) {
  return (
    <div>
      <div className={cx("font-medium", item.tone === "danger" ? "text-danger" : "text-warn")}>
        <span aria-hidden="true">{item.tone === "danger" ? "✕ " : "⚠ "}</span>{item.title}
      </div>
      <p className="mt-0.5 text-sm">{item.body}</p>
    </div>
  );
}

/** Narrow screens: the account, appearance and sign-out in one menu. */
function AccountMenu() {
  const { me, logout } = useAuth();
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const id = useId();
  const location = useLocation();
  useEffect(() => setOpen(false), [location.pathname]);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <div ref={box} className="relative md:hidden">
      <button type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}
        className="inline-flex h-9 items-center gap-1 rounded-ui px-2 text-sm hover:bg-surface-2">
        <span className="max-w-[6rem] truncate">{me?.user.username}</span>
        <span className="sr-only">: account menu</span>
        <ChevronDownIcon width={14} height={14} />
      </button>
      {open && (
        <div id={id} className="absolute right-0 top-full z-40 mt-2 w-64 space-y-3 rounded-ui border border-line bg-surface p-3 text-sm shadow-lg">
          <div>{me?.user.username} <span className="text-muted">({me?.user.role})</span></div>
          <ThemeSwitcher compact name="theme-menu" />
          <div className="flex items-center justify-between gap-2 border-t border-line pt-3">
            <Link to="/settings/account" className="text-sm underline decoration-accent underline-offset-2">Account settings</Link>
            <Button size="sm" onClick={() => void logout()}>Sign out</Button>
          </div>
        </div>
      )}
    </div>
  );
}
