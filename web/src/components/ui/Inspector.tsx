import { useEffect, useId, useLayoutEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ArrowRightIcon, CloseIcon } from "./icons";
import { isTopLayer, useModalLayer } from "./modal";
import { canFocus } from "./Dialog";

const focusable = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
const nameOf = (el: Element) => el.getAttribute("aria-label") || (el as HTMLElement).innerText?.trim() || "";

/** A contextual inspector (Pattern A): one record looked at, and acted on,
 * beside the list it came from — a panel from the right on wider screens
 * (the list stays in view), the whole screen on a phone. One at a time: a
 * step inside it (classify, acknowledge…) replaces its content with a Back
 * button instead of opening a dialog on top. It is a modal layer (focus
 * stays inside, the re-authentication prompt can still come on top);
 * Escape goes back from a step, else closes, and focus returns to where it
 * was. Opening a step moves focus into the panel (its new title is
 * announced); going back returns it to the button that opened the step. */
export function Inspector({ title, subtitle, onClose, onBack, backLabel = "Back", children, footer, testId }: {
  title: ReactNode;
  subtitle?: ReactNode;
  onClose: () => void;
  /** Shown while a step is open: back to the record. */
  onBack?: () => void;
  backLabel?: string;
  children: ReactNode;
  /** Buttons kept in sight below a body that scrolls. */
  footer?: ReactNode;
  testId?: string;
}) {
  const layer = useRef<HTMLDivElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useModalLayer(layer, () => panel.current?.querySelector<HTMLElement>("[data-autofocus]") ?? panel.current);
  // The control focused last (the step's opener, once a step replaces it).
  const lastFocus = useRef("");
  const opener = useRef("");
  const inStep = !!onBack;
  const wasStep = useRef(inStep);
  useLayoutEffect(() => {
    if (wasStep.current === inStep) return;
    wasStep.current = inStep;
    const p = panel.current;
    if (!p) return;
    if (inStep) {
      opener.current = lastFocus.current;
      (p.querySelector<HTMLElement>("[data-autofocus]") ?? p).focus();
    } else {
      const back = [...p.querySelectorAll<HTMLElement>(focusable)].find((x) => opener.current && nameOf(x) === opener.current);
      (back ?? p).focus();
    }
  }, [inStep]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isTopLayer(layer.current)) return;
      if (e.key === "Escape") {
        e.preventDefault();
        (onBack ?? onClose)();
      }
      if (e.key === "Tab" && panel.current) {
        const items = [...panel.current.querySelectorAll<HTMLElement>(focusable)].filter(canFocus);
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
  return createPortal(
    <div ref={layer} data-modal-layer="" className="fixed inset-0 flex justify-end bg-[rgb(0_0_0/0.25)]"
      onMouseDown={(e) => { if (e.target === e.currentTarget && isTopLayer(layer.current)) onClose(); }}>
      <div ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} data-testid={testId} data-inspector=""
        onFocus={(e) => { if (e.target !== panel.current) lastFocus.current = nameOf(e.target); }}
        className="flex h-full w-full flex-col border-l border-line bg-surface shadow-lg outline-none sm:w-[32rem] xl:w-[36rem]">
        <div className="flex shrink-0 items-start gap-2 border-b border-line px-4 py-3">
          {onBack && (
            <button type="button" onClick={onBack} aria-label={backLabel}
              className="-ml-1 mt-0.5 inline-flex h-8 shrink-0 items-center gap-1 rounded-ui px-2 text-sm text-muted hover:bg-surface-2 hover:text-fg">
              <ArrowRightIcon className="rotate-180" width={14} height={14} />{backLabel}
            </button>
          )}
          <div className="min-w-0 flex-1">
            <h2 id={titleId} className="break-words text-base font-semibold">{title}</h2>
            {subtitle && <div className="mt-0.5 text-sm text-muted">{subtitle}</div>}
          </div>
          <button type="button" onClick={onClose} aria-label="Close"
            className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-ui text-muted hover:bg-surface-2 hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11">
            <CloseIcon />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4" data-inspector-body="">{children}</div>
        {footer && <div className="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-line px-4 py-3">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}
