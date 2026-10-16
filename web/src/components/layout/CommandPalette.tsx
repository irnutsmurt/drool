import { useEffect, useId, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { useDebounced } from "@/hooks/useDebounced";
import { buildGroups, type RecordResult } from "@/lib/entitysearch";
import { errorMessage, formatDateTime } from "@/lib/format";
import { searchDestinations, type SearchResult as PageResult } from "@/lib/navsearch";
import { SearchIcon } from "@/components/ui/icons";
import { cx } from "@/components/ui/cx";
import { isTopLayer, useModalLayer } from "@/components/ui/modal";
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

const recordHint: Record<string, string> = { host: "Host", service: "Service", alert: "Alert", pack: "Pack", run: "Run" };

/** Search (Ctrl+K / ⌘K, or "/"): pages and settings by name and synonyms,
 * and the records people look for — hosts (name, address, tag), Packs,
 * services with their host, active alerts, recent runs — from the server,
 * as typing pauses, only what the user may read. A combobox over grouped
 * results: arrow keys move, Enter opens, Escape closes and focus returns
 * to where it was. */
export function CommandPalette({ onClose }: { onClose: () => void }) {
  const { can } = useAuth();
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const layer = useRef<HTMLDivElement>(null);
  const id = useId();
  const pages = useMemo(() => searchDestinations(q, destinations, can), [q, can]);
  const term = useDebounced(q.trim(), 200);
  const records = useQuery({
    queryKey: ["search", term],
    queryFn: ({ signal }) => unwrap(api.GET("/search", { params: { query: { q: term, limit: 5 } }, signal })),
    enabled: term.length >= 2,
    staleTime: 15_000,
    retry: false,
  });
  // Results for an older query stay out of the list while the new one loads.
  const current = term === q.trim() && records.data?.query === term ? records.data : undefined;
  const searching = q.trim().length >= 2 && (term !== q.trim() || records.isFetching);
  const groups = useMemo(() => buildGroups(q, pages, current?.results, current?.more ?? []), [q, pages, current]);
  const entries = useMemo(() => groups.flatMap((g) => g.entries), [groups]);

  useEffect(() => setActive(0), [q, current]);
  useModalLayer(layer, () => input.current);
  useEffect(() => {
    list.current?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const open = (to: string | undefined) => {
    if (!to) return;
    onClose();
    navigate(to);
  };
  const onKey = (e: React.KeyboardEvent) => {
    const n = entries.length;
    if (e.key === "ArrowDown" && n) {
      e.preventDefault();
      setActive((a) => (a + 1) % n);
    } else if (e.key === "ArrowUp" && n) {
      e.preventDefault();
      setActive((a) => (a - 1 + n) % n);
    } else if (e.key === "Enter") {
      e.preventDefault();
      open(entries[active]?.to);
    } else if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "Tab") {
      e.preventDefault(); // focus stays in the search field
    }
  };

  const optionId = (i: number) => `${id}-opt-${i}`;
  let index = -1;
  return createPortal(
    <div ref={layer} data-modal-layer="" className="fixed inset-0 flex items-start justify-center bg-[var(--ui-backdrop)] p-2 sm:p-4 sm:pt-[10vh]"
      onMouseDown={(e) => { if (e.target === e.currentTarget && isTopLayer(layer.current)) onClose(); }}>
      <div role="dialog" aria-modal="true" aria-label="Search" data-testid="command-palette" data-searching={searching}
        className="flex max-h-[calc(100dvh-1rem)] w-full max-w-xl flex-col overflow-hidden rounded-ui border border-line bg-surface shadow-lg sm:max-h-[70vh]">
        <div className="flex items-center gap-2 border-b border-line px-3">
          <SearchIcon className="shrink-0 text-muted" />
          <input ref={input} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKey}
            role="combobox" aria-expanded="true" aria-controls={`${id}-list`} aria-autocomplete="list"
            aria-activedescendant={entries.length ? optionId(active) : undefined} aria-label="Search hosts, services, alerts, runs, pages and settings"
            placeholder="Search… a host, a service, an alert, “api token”" autoComplete="off" spellCheck={false}
            className="h-12 min-w-0 flex-1 bg-transparent text-base text-fg outline-none placeholder:text-muted focus-visible:outline-none sm:text-sm" />
          {searching && <span className="shrink-0 text-xs text-muted" aria-hidden="true">Searching…</span>}
          <kbd className="hidden rounded border border-line px-1.5 py-0.5 text-[11px] text-muted sm:inline">Esc</kbd>
        </div>
        <div ref={list} id={`${id}-list`} role="listbox" aria-label="Results" className="min-h-0 flex-1 overflow-y-auto p-1">
          {groups.map((g) => (
            <div key={g.id} role="group" aria-labelledby={`${id}-g-${g.id}`} className="pb-1">
              <div id={`${id}-g-${g.id}`} className="px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wider text-muted">{g.label}</div>
              {g.entries.map((en) => {
                const i = ++index;
                return (
                  <div key={en.key} id={optionId(i)} role="option" aria-selected={i === active} data-index={i} data-to={en.to}
                    onMouseMove={() => i !== active && setActive(i)} onMouseDown={(e) => e.preventDefault()} onClick={() => open(en.to)}
                    className={cx("cursor-pointer rounded-ui border-l-2 px-3 py-2", i === active ? "border-accent bg-surface-2" : "border-transparent")}>
                    {en.type === "page" ? <PageOption r={en.page} /> : <RecordOption r={en.record} />}
                  </div>
                );
              })}
              {g.more && (
                <button type="button" onMouseDown={(e) => e.preventDefault()} onClick={() => open(g.more!.to)} tabIndex={-1}
                  className="ml-3 mt-0.5 text-xs text-muted underline underline-offset-2 hover:text-fg">{g.more.label}</button>
              )}
            </div>
          ))}
        </div>
        {entries.length === 0 && !searching && (
          <p className="px-4 py-6 text-center text-sm text-muted">
            Nothing matches “{q}”. Try a host name or address, a service, words of an alert, or a page such as “threshold” or “api token”.
          </p>
        )}
        {records.error && <p className="px-4 pb-2 text-xs text-danger" role="alert">Hosts, services, alerts and runs couldn&apos;t be searched: {errorMessage(records.error)}</p>}
        <p className="sr-only" role="status" aria-live="polite">{q && !searching ? `${entries.length} result${entries.length === 1 ? "" : "s"}` : ""}</p>
        <div className="hidden border-t border-line px-3 py-1.5 text-[11px] text-muted sm:block" aria-hidden="true">
          ↑ ↓ to move · Enter to open · Esc to close · hosts, services, active alerts, Packs, runs, pages and settings
        </div>
      </div>
    </div>,
    document.body,
  );
}

function PageOption({ r }: { r: PageResult }) {
  return (
    <>
      <div className="flex items-baseline gap-2">
        <span className="font-medium">{r.dest.title}</span>
        {r.dest.hint && <span className="text-xs text-muted">{r.dest.hint}</span>}
        <span className="ml-auto shrink-0 text-[11px] uppercase tracking-wide text-muted">{r.dest.section}</span>
      </div>
      <div className="text-xs text-muted">
        {r.dest.description}
        {r.via && r.via !== r.dest.hint && <> · matches “{r.via}”</>}
      </div>
    </>
  );
}

function RecordOption({ r }: { r: RecordResult }) {
  const tone = r.kind === "alert" ? (r.state === "critical" ? "text-danger" : "text-warn")
    : r.kind === "service" && /^failed/.test(r.state ?? "") ? "text-danger" : "text-muted";
  return (
    <>
      <div className="flex items-baseline gap-2">
        <span className={cx("min-w-0 truncate font-medium", r.kind === "service" && "font-mono text-[13px]")}>{r.title}</span>
        {r.kind === "service" && r.host_name && <span className="shrink-0 text-xs text-muted">— {r.host_name}</span>}
        <span className="ml-auto shrink-0 text-[11px] uppercase tracking-wide text-muted">{recordHint[r.kind]}</span>
      </div>
      <div className="flex flex-wrap gap-x-2 text-xs text-muted">
        {r.kind === "service" && r.state && <span className={tone}>{r.state}</span>}
        {r.detail && <span className={cx("min-w-0", r.kind === "alert" && tone)}>{r.detail}</span>}
        {r.kind === "run" && r.at && <span>· {formatDateTime(r.at)}</span>}
        {r.match && <span>· matches {r.match}</span>}
      </div>
    </>
  );
}
