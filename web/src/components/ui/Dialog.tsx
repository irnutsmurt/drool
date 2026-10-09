import { useEffect, useRef, type ReactNode } from "react";

const focusable = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** A modal dialog: focus moves into it, Tab stays inside, Escape closes,
 * and focus returns to where it was. Opened from another dialog, only the
 * top one reacts to the keyboard. */
export function Dialog({ title, children, onClose, wide }: { title: string; children: ReactNode; onClose: () => void; wide?: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    const first = box.current?.querySelector<HTMLElement>("[data-autofocus]") ?? box.current?.querySelector<HTMLElement>(focusable);
    first?.focus();
    return () => before?.focus?.();
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Only the topmost of nested dialogs handles keys.
      const all = document.querySelectorAll('[role="dialog"][aria-modal="true"]');
      if (all[all.length - 1] !== box.current) return;
      if (e.key === "Escape") onClose();
      if (e.key === "Tab" && box.current) {
        const items = [...box.current.querySelectorAll<HTMLElement>(focusable)].filter((x) => x.offsetParent !== null);
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
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-[var(--ui-backdrop)] p-4 pt-16" onMouseDown={onClose}>
      <div ref={box} role="dialog" aria-modal="true" aria-label={title}
        className={`w-full ${wide ? "max-w-4xl" : "max-w-lg"} max-h-[85vh] overflow-y-auto rounded-ui border border-line bg-surface p-5 shadow-lg`}
        onMouseDown={(e) => e.stopPropagation()}>
        <h2 className="mb-4 text-base font-semibold">{title}</h2>
        {children}
      </div>
    </div>
  );
}
