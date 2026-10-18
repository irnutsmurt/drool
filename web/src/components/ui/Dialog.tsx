import { useEffect, useId, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { CloseIcon } from "./icons";
import { isTopLayer, useModalLayer } from "./modal";

const focusable = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

/** Can take the focus now. A closed <details> keeps its content laid out
 * (offsetParent is set) but hidden: focusing a field in it does nothing. */
export const canFocus = (x: HTMLElement) => (typeof x.checkVisibility === "function" ? x.checkVisibility() : x.offsetParent !== null);

/** A modal dialog: on top of every open dialog (also when opened from one,
 * like the re-authentication prompt), focus moves into it, Tab stays
 * inside, Escape and the backdrop close it, focus returns to where it was,
 * and everything beneath it is inert. Only the topmost dialog reacts to the
 * keyboard and its backdrop (ui/modal.ts keeps the order). */
export function Dialog({ title, children, onClose, wide, footer, testId }: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
  /** The dialog's buttons, kept in sight below a body that scrolls: the
   * primary action is never under the window's edge. */
  footer?: ReactNode;
  testId?: string;
}) {
  const layer = useRef<HTMLDivElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const titleId = useId();
  // The first field that can take the focus (not a disclosure, which Enter
  // would open), else the dialog itself: focus is inside the dialog from
  // the start, whatever is still loading.
  useModalLayer(layer, () => box.current?.querySelector<HTMLElement>("[data-autofocus]")
    ?? [...(box.current?.querySelectorAll<HTMLElement>(focusable) ?? [])].find((x) => canFocus(x) && x.tagName !== "SUMMARY") ?? box.current);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isTopLayer(layer.current)) return;
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
      if (e.key === "Tab" && box.current) {
        const items = [...box.current.querySelectorAll<HTMLElement>(focusable)].filter(canFocus);
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
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return createPortal(
    <div ref={layer} data-modal-layer="" className="fixed inset-0 flex items-start justify-center bg-[var(--ui-backdrop)] p-2 sm:p-4 sm:pt-16"
      onMouseDown={(e) => { if (e.target === e.currentTarget && isTopLayer(layer.current)) onClose(); }}>
      <div ref={box} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-label={title} tabIndex={-1} data-testid={testId}
        className={`relative w-full ${wide ? "max-w-4xl" : "max-w-lg"} max-h-[calc(100dvh-1rem)] rounded-ui border border-line bg-surface shadow-lg outline-none sm:max-h-[85vh] ${
          footer ? "flex flex-col" : "overflow-y-auto p-4 sm:p-5"}`}>
        {footer ? (
          <>
            <h2 id={titleId} className="shrink-0 px-4 pb-3 pr-12 pt-4 text-base font-semibold sm:px-5 sm:pt-5">{title}</h2>
            <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-4 sm:px-5" data-dialog-body="">{children}</div>
            <div className="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-line px-4 py-3 sm:px-5" data-dialog-footer="">{footer}</div>
          </>
        ) : (
          <>
            <h2 id={titleId} className="mb-4 pr-8 text-base font-semibold">{title}</h2>
            {children}
          </>
        )}
        {/* Last in the tab order: the first field keeps the initial focus. */}
        <button type="button" onClick={onClose} aria-label="Close dialog"
          className="absolute right-2 top-2 inline-flex h-8 w-8 items-center justify-center rounded-ui text-muted hover:bg-surface-2 hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11">
          <CloseIcon />
        </button>
      </div>
    </div>,
    document.body,
  );
}
