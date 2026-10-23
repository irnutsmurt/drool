import { useEffect, useId, useLayoutEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ArrowRightIcon, CloseIcon } from "./icons";
import { isTopLayer, useModalLayer } from "./modal";

const focusable = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

/** Can take the focus now. A closed <details> keeps its content laid out
 * (offsetParent is set) but hidden: focusing a field in it does nothing. */
export const canFocus = (x: HTMLElement) => (typeof x.checkVisibility === "function" ? x.checkVisibility() : x.offsetParent !== null);
const nameOf = (el: Element) => el.getAttribute("aria-label") || (el as HTMLElement).innerText?.trim() || "";

/** A modal dialog: on top of every open dialog (also when opened from one,
 * like the re-authentication prompt), focus moves into it, Tab stays
 * inside, Escape and the backdrop close it, focus returns to where it was,
 * and everything beneath it is inert. Only the topmost dialog reacts to the
 * keyboard and its backdrop (ui/modal.ts keeps the order).
 *
 * A follow-up step replaces the dialog's content (never a dialog on top,
 * §5): with onBack set, the dialog shows Back, Escape goes back, focus
 * moves into the step, and Back returns it to the control that opened the
 * step — as an inspector's step does. With several steps, `step` names the
 * current one so that each forward step moves focus in and each step back
 * returns it to that step's opener. */
export function Dialog({ title, children, onClose, onBack, backLabel = "Back", step, wide, footer, testId }: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  /** In a step: what Back and Escape do. */
  onBack?: () => void;
  backLabel?: string;
  /** The current step's name (default: one step while onBack is set). */
  step?: string;
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
  const firstField = () => box.current?.querySelector<HTMLElement>("[data-autofocus]")
    ?? [...(box.current?.querySelectorAll<HTMLElement>(focusable) ?? [])].find((x) => canFocus(x) && x.tagName !== "SUMMARY" && !x.hasAttribute("data-dialog-back")) ?? box.current;
  useModalLayer(layer, firstField);
  // The control focused last, and for each step taken the control that
  // opened it: going back focuses it again.
  const lastFocus = useRef("");
  const key = step ?? (onBack ? "step" : "");
  const history = useRef<{ key: string; opener: string }[]>([{ key, opener: "" }]);
  useLayoutEffect(() => {
    const h = history.current;
    if (h[h.length - 1]!.key === key) return;
    const b = box.current;
    const at = h.findIndex((x) => x.key === key);
    if (at >= 0) {
      const left = h[at + 1]!;
      h.length = at + 1;
      const back = b && [...b.querySelectorAll<HTMLElement>(focusable)].find((x) => left.opener && nameOf(x) === left.opener);
      (back ?? b)?.focus();
    } else {
      h.push({ key, opener: lastFocus.current });
      firstField()?.focus();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isTopLayer(layer.current)) return;
      if (e.key === "Escape") {
        e.preventDefault();
        (onBack ?? onClose)();
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
  }, [onClose, onBack]);
  const back = onBack && (
    <button type="button" onClick={onBack} aria-label={backLabel} data-dialog-back=""
      className="-ml-1 mb-1 inline-flex h-8 items-center gap-1 rounded-ui px-2 text-sm text-muted hover:bg-surface-2 hover:text-fg pointer-coarse:h-11">
      <ArrowRightIcon className="rotate-180" width={14} height={14} />{backLabel}
    </button>
  );
  return createPortal(
    <div ref={layer} data-modal-layer="" className="fixed inset-0 flex items-start justify-center bg-[var(--ui-backdrop)] p-2 sm:p-4 sm:pt-16"
      onMouseDown={(e) => { if (e.target === e.currentTarget && isTopLayer(layer.current)) onClose(); }}>
      <div ref={box} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-label={title} tabIndex={-1} data-testid={testId}
        onFocus={(e) => { if (e.target !== box.current) lastFocus.current = nameOf(e.target); }}
        className={`relative w-full ${wide ? "max-w-4xl" : "max-w-lg"} max-h-[calc(100dvh-1rem)] rounded-ui border border-line bg-surface shadow-lg outline-none sm:max-h-[85vh] ${
          footer ? "flex flex-col" : "overflow-y-auto p-4 sm:p-5"}`}>
        {footer ? (
          <>
            <div className="shrink-0 px-4 pb-3 pr-12 pt-4 sm:px-5 sm:pt-5">{back}<h2 id={titleId} className="text-base font-semibold">{title}</h2></div>
            <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-4 sm:px-5" data-dialog-body="">{children}</div>
            <div className="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-line px-4 py-3 sm:px-5" data-dialog-footer="">{footer}</div>
          </>
        ) : (
          <>
            {back}
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
