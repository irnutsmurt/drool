import { useEffect, useId, useRef, useState, type ReactNode } from "react";
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
 * returns to the button. A click outside closes it. */
export function MenuButton({ label, items, align = "end", size = "md", testId }: {
  label: ReactNode;
  items: MenuItem[];
  align?: "start" | "end";
  size?: "sm" | "md";
  testId?: string;
}) {
  const [open, setOpen] = useState(false);
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
  useEffect(() => {
    if (!open) return;
    entries()[0]?.focus();
    const onDown = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

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
    } else if (e.key === "Tab") close(false);
  };

  const itemClass = (x: MenuItem) => cx(
    "flex w-full flex-col items-start rounded-ui px-3 py-2 text-left text-sm outline-none",
    x.disabled ? "cursor-not-allowed opacity-50" : "hover:bg-surface-2 focus:bg-surface-2",
    x.danger && !x.disabled ? "text-danger" : "text-fg",
  );
  return (
    <div ref={box} className="relative inline-block">
      <button ref={button} type="button" aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined} data-testid={testId}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => { if (e.key === "ArrowDown" && !open) { e.preventDefault(); setOpen(true); } }}
        className={cx("inline-flex items-center justify-center gap-1.5 rounded-ui border border-line bg-surface font-medium text-fg hover:bg-surface-2",
          size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3.5 text-sm")}>
        {label}
        <ChevronDownIcon width={14} height={14} />
      </button>
      {open && (
        <div ref={list} id={id} role="menu" aria-orientation="vertical" onKeyDown={onKey}
          className={cx("absolute top-full z-40 mt-1 w-64 max-w-[calc(100vw-2rem)] rounded-ui border border-line bg-surface p-1 shadow-lg",
            align === "end" ? "right-0" : "left-0")}>
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
        </div>
      )}
    </div>
  );
}
