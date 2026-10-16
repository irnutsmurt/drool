import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router";
import { ChevronDownIcon } from "./icons";
import { cx } from "./cx";

export interface MenuItem {
  label: ReactNode;
  /** A short line under the label. */
  hint?: ReactNode;
  /** Navigation (a link), or… */
  to?: string;
  /** …an action. */
  onSelect?: () => void;
  danger?: boolean;
  disabled?: boolean;
  /** A line above this item: the start of another group. */
  separated?: boolean;
  /** Only on phones (where the page shows the same action as a button on wider screens). */
  narrowOnly?: boolean;
}

/** A button that opens a short list of actions (the "Actions ▾" pattern):
 * the less frequent things a page offers, so the page header keeps one or
 * two visible actions. Keyboard: Enter/Space/↓ open it on the first item,
 * ↑/↓/Home/End move, Enter activates, Escape or Tab closes and focus
 * returns to the button. A click outside closes it; scrolling moves it with
 * its button, and closes it once the button is off the screen. The open
 * list is drawn above the page (a portal), so a table's scroll box can't
 * cut it off: under its button, above it when there's no room below, and
 * always on the screen. */
export function MenuButton({ label, items, align = "end", size = "md", testId, ariaLabel, ghost }: {
  label: ReactNode;
  items: MenuItem[];
  align?: "start" | "end";
  size?: "sm" | "md";
  testId?: string;
  /** The button's accessible name when its label is only a symbol (⋯). */
  ariaLabel?: string;
  /** A borderless button (row menus in tables). */
  ghost?: boolean;
}) {
  const [open, setOpen] = useState(false);
  // Where the open list is drawn (fixed, in the viewport); null until it
  // has been measured.
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const id = useId();
  const shown = items.filter(Boolean);

  const entries = () => [...(list.current?.querySelectorAll<HTMLElement>("[role=menuitem]:not([aria-disabled=true])") ?? [])].filter((e) => e.offsetParent !== null);
  const close = (focusButton = true) => {
    setOpen(false);
    if (focusButton) button.current?.focus();
  };
  // Under the button, above it when there's no room below, on the screen.
  const place = useCallback(() => {
    const b = button.current?.getBoundingClientRect();
    const l = list.current;
    if (!b || !l) return;
    const w = l.offsetWidth;
    const h = l.offsetHeight;
    const left = Math.min(Math.max(8, align === "end" ? b.right - w : b.left), window.innerWidth - 8 - w);
    const below = b.bottom + 4;
    const top = below + h > window.innerHeight - 8 && b.top - 4 - h >= 8 ? b.top - 4 - h : below;
    setPos({ top, left });
  }, [align]);
  useLayoutEffect(() => {
    if (!open) {
      setPos(null);
      return;
    }
    place();
  }, [open, place]);
  const placed = pos !== null;
  useEffect(() => {
    if (!open || !placed) return;
    entries()[0]?.focus({ preventScroll: true });
    const inside = (t: EventTarget | null) => !!t && (box.current?.contains(t as Node) || list.current?.contains(t as Node));
    const onDown = (e: MouseEvent) => { if (!inside(e.target)) setOpen(false); };
    // Scrolling (or resizing) moves the list with its button, also a scroll
    // still settling as it opens (a touch flick, a button scrolled into
    // view); once the button has left the screen, the list closes.
    const onMove = (e: Event) => {
      if (inside(e.target)) return;
      const b = button.current?.getBoundingClientRect();
      if (!b || b.bottom < 0 || b.top > window.innerHeight) setOpen(false);
      else place();
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("scroll", onMove, true);
    window.addEventListener("resize", onMove);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("scroll", onMove, true);
      window.removeEventListener("resize", onMove);
    };
  }, [open, placed, place]);

  const onKey = (e: React.KeyboardEvent) => {
    const all = entries();
    const i = all.indexOf(document.activeElement as HTMLElement);
    const move = (n: number) => {
      e.preventDefault();
      all[(n + all.length) % all.length]?.focus();
    };
    if (e.key === "ArrowDown") move(i + 1);
    else if (e.key === "ArrowUp") move(i - 1);
    else if (e.key === "Home") move(0);
    else if (e.key === "End") move(all.length - 1);
    else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === "Tab") {
      // Back to the button, then the browser's Tab moves on from there.
      setOpen(false);
      button.current?.focus();
    }
  };

  const itemClass = (x: MenuItem) => cx(
    "flex w-full flex-col items-start rounded-ui px-3 py-2 text-left text-sm outline-none",
    x.disabled ? "cursor-not-allowed opacity-50" : "hover:bg-surface-2 focus:bg-surface-2",
    x.danger && !x.disabled ? "text-danger" : "text-fg",
  );
  return (
    <div ref={box} className="relative inline-block">
      <button ref={button} type="button" aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined} data-testid={testId}
        aria-label={ariaLabel} onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => { if (e.key === "ArrowDown" && !open) { e.preventDefault(); setOpen(true); } }}
        className={cx("inline-flex items-center justify-center gap-1.5 rounded-ui font-medium text-fg hover:bg-surface-2",
          ghost ? "" : "border border-line bg-surface",
          size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3.5 text-sm")}>
        {label}
        {!ghost && <ChevronDownIcon width={14} height={14} />}
      </button>
      {open && createPortal(
        <div ref={list} id={id} role="menu" aria-orientation="vertical" onKeyDown={onKey}
          style={{ position: "fixed", top: pos?.top ?? 0, left: pos?.left ?? 0, visibility: pos ? "visible" : "hidden" }}
          className="z-50 w-64 max-w-[calc(100vw-1rem)] rounded-ui border border-line bg-surface p-1 shadow-lg">
          {shown.map((x, i) => (
            <div key={i} role="none" className={cx(x.separated && i > 0 && "mt-1 border-t border-line pt-1", x.narrowOnly && "sm:hidden")}>
              {x.to && !x.disabled ? (
                <Link role="menuitem" tabIndex={-1} to={x.to} className={itemClass(x)} onClick={() => setOpen(false)}>
                  <span>{x.label}</span>
                  {x.hint && <span className="text-xs text-muted">{x.hint}</span>}
                </Link>
              ) : (
                <button type="button" role="menuitem" tabIndex={-1} aria-disabled={x.disabled || undefined} className={itemClass(x)}
                  onClick={() => {
                    if (x.disabled) return;
                    setOpen(false);
                    // Focus goes back to the button first: a dialog the
                    // action opens returns focus there when it closes.
                    button.current?.focus();
                    x.onSelect?.();
                  }}>
                  <span>{x.label}</span>
                  {x.hint && <span className="text-xs text-muted">{x.hint}</span>}
                </button>
              )}
            </div>
          ))}
        </div>,
        document.body,
      )}
    </div>
  );
}
