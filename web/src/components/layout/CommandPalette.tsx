import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { searchDestinations, type SearchResult } from "@/lib/navsearch";
import { SearchIcon } from "@/components/ui/icons";
import { cx } from "@/components/ui/cx";
import { destinations } from "./destinations";

export const isMac = () => typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
export const shortcutLabel = () => (isMac() ? "⌘K" : "Ctrl K");

/** True when a key press belongs to a text field ("/" types there). */
export function typingIn(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable || !!el.closest?.("[contenteditable='true'], .cm-editor");
}

/** Go to a page or setting by name (Ctrl+K / ⌘K, or "/"): a combobox over
 * a list of destinations. Arrow keys move, Enter opens, Escape closes and
 * focus returns to where it was. */
export function CommandPalette({ onClose }: { onClose: () => void }) {
  const { can } = useAuth();
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const id = useId();
  const results = useMemo(() => searchDestinations(q, destinations, can), [q, can]);

  useEffect(() => setActive(0), [q]);
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    input.current?.focus();
    return () => before?.focus?.();
  }, []);
  useEffect(() => {
    list.current?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const open = (r: SearchResult | undefined) => {
    if (!r) return;
    onClose();
    navigate(r.dest.to);
  };
  const onKey = (e: React.KeyboardEvent) => {
    const n = results.length;
    if (e.key === "ArrowDown" && n) {
      e.preventDefault();
      setActive((a) => (a + 1) % n);
    } else if (e.key === "ArrowUp" && n) {
      e.preventDefault();
      setActive((a) => (a - 1 + n) % n);
    } else if (e.key === "Enter") {
      e.preventDefault();
      open(results[active]);
    } else if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "Tab") {
      e.preventDefault(); // focus stays in the search field
    }
  };

  const optionId = (i: number) => `${id}-opt-${i}`;
  return (
    <div className="fixed inset-0 z-[60] flex items-start justify-center bg-[var(--ui-backdrop)] p-2 sm:p-4 sm:pt-[10vh]" onMouseDown={onClose}>
      <div role="dialog" aria-modal="true" aria-label="Go to a page or setting" data-testid="command-palette"
        className="flex max-h-[calc(100dvh-1rem)] w-full max-w-xl flex-col overflow-hidden rounded-ui border border-line bg-surface shadow-lg sm:max-h-[70vh]"
        onMouseDown={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-2 border-b border-line px-3">
          <SearchIcon className="shrink-0 text-muted" />
          <input ref={input} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKey}
            role="combobox" aria-expanded="true" aria-controls={`${id}-list`} aria-autocomplete="list"
            aria-activedescendant={results.length ? optionId(active) : undefined} aria-label="Go to a page or setting"
            placeholder="Go to… try “api token”, “drift” or “agent”" autoComplete="off" spellCheck={false}
            className="h-12 min-w-0 flex-1 bg-transparent text-base text-fg outline-none placeholder:text-muted focus-visible:outline-none sm:text-sm" />
          <kbd className="hidden rounded border border-line px-1.5 py-0.5 text-[11px] text-muted sm:inline">Esc</kbd>
        </div>
        <ul ref={list} id={`${id}-list`} role="listbox" aria-label="Pages and settings" className="min-h-0 flex-1 overflow-y-auto p-1">
          {results.map((r, i) => (
            <li key={r.dest.id} id={optionId(i)} role="option" aria-selected={i === active} data-index={i} data-to={r.dest.to}
              onMouseMove={() => i !== active && setActive(i)} onMouseDown={(e) => e.preventDefault()} onClick={() => open(r)}
              className={cx("cursor-pointer rounded-ui border-l-2 px-3 py-2", i === active ? "border-accent bg-surface-2" : "border-transparent")}>
              <div className="flex items-baseline gap-2">
                <span className="font-medium">{r.dest.title}</span>
                {r.dest.hint && <span className="text-xs text-muted">{r.dest.hint}</span>}
                <span className="ml-auto shrink-0 text-[11px] uppercase tracking-wide text-muted">{r.dest.section}</span>
              </div>
              <div className="text-xs text-muted">
                {r.dest.description}
                {r.via && r.via !== r.dest.hint && <> · matches “{r.via}”</>}
              </div>
            </li>
          ))}
        </ul>
        {results.length === 0 && (
          <p className="px-4 py-6 text-center text-sm text-muted">
            No page or setting matches “{q}”. Try other words, for example “agent”, “threshold” or “api token”.
          </p>
        )}
        <p className="sr-only" role="status" aria-live="polite">{q ? `${results.length} result${results.length === 1 ? "" : "s"}` : ""}</p>
        <div className="hidden border-t border-line px-3 py-1.5 text-[11px] text-muted sm:block" aria-hidden="true">
          ↑ ↓ to move · Enter to open · Esc to close · searches pages and settings, not hosts or runs
        </div>
      </div>
    </div>
  );
}
