import { useId, useMemo, useRef, useState, type ReactNode } from "react";
import { cx } from "./cx";

/** One choice of a ComboPicker. `disabled` is why it can't be chosen. */
export type PickOption = { value: string; label: string; detail?: string; group: string; disabled?: string; search?: string };

const perGroup = 8; // shown per group before anything is typed

/** The choices matching what was typed: a label that starts with it ranks
 * first, then a label that contains it, then a match in the detail or
 * search words. Groups keep their order, except that a group with a better
 * match comes first (typing "linux" puts the Pack linux-servers ahead of
 * the hosts that only mention it). */
export function matchOptions(options: PickOption[], query: string, max = 50): PickOption[] {
  const q = query.trim().toLowerCase();
  if (!q) {
    const seen = new Map<string, number>();
    return options.filter((o) => {
      const n = (seen.get(o.group) ?? 0) + 1;
      seen.set(o.group, n);
      return n <= perGroup;
    });
  }
  const hay = (o: PickOption) => `${o.label} ${o.detail ?? ""} ${o.search ?? ""}`.toLowerCase();
  const rank = (o: PickOption) => (o.label.toLowerCase().startsWith(q) ? 0 : o.label.toLowerCase().includes(q) ? 1 : 2);
  const found = options.filter((o) => hay(o).includes(q));
  const groups = [...new Set(options.map((o) => o.group))];
  const best = new Map(groups.map((g) => [g, Math.min(3, ...found.filter((o) => o.group === g).map(rank))]));
  return groups.filter((g) => best.get(g)! < 3)
    .sort((a, b) => best.get(a)! - best.get(b)! || groups.indexOf(a) - groups.indexOf(b))
    .flatMap((g) => found.filter((o) => o.group === g).sort((a, b) => rank(a) - rank(b)))
    .slice(0, max);
}

/**
 * A searchable field that chooses one or more values in one step
 * (DESIGN-STANDARD §1, Workflow first): the chosen values are chips, each
 * with a remove button, and one text field searches every kind of choice at
 * once, in labelled groups. No separate kind, value and Add button.
 *
 * WAI-ARIA combobox with a listbox: typing, a click or ↓ opens the list;
 * ↑ ↓ move, Enter chooses, Escape closes the list (the next Escape is the
 * dialog's), Tab moves on. `suggest` turns what was typed into extra
 * choices, such as an address range or a port, listed first.
 */
export function ComboPicker({ label, description, values, chipLabel, chipKind, options, suggest, onAdd, onRemove, placeholder, emptyText, testId, autoFocus }: {
  label: string;
  /** Shown after the label, and read with it. */
  description?: string;
  values: string[];
  chipLabel: (value: string) => string;
  /** A short word before a chip's label ("Host", "Pack"); none for keywords. */
  chipKind?: (value: string) => string | undefined;
  options: PickOption[];
  suggest?: (query: string) => PickOption[];
  onAdd: (value: string) => void;
  onRemove: (value: string) => void;
  placeholder: string;
  /** Said when nothing matches what was typed. */
  emptyText: (query: string) => ReactNode;
  testId?: string;
  autoFocus?: boolean;
}) {
  const id = useId();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const shown = useMemo(() => {
    const extra = suggest ? suggest(query) : [];
    const rest = options.filter((o) => !values.includes(o.value) && !extra.some((x) => x.value === o.value));
    const list = [...extra, ...matchOptions(rest, query)];
    // Group by group, in order of first appearance: ↑ ↓ follow the rows.
    return [...new Set(list.map((o) => o.group))].flatMap((g) => list.filter((o) => o.group === g));
  }, [options, values, query, suggest]);
  const groups = useMemo(() => [...new Set(shown.map((o) => o.group))], [shown]);
  const choosable = (i: number) => !!shown[i] && !shown[i]!.disabled;
  const move = (from: number, step: number) => {
    for (let k = 1; k <= shown.length; k++) {
      const i = (from + step * k + shown.length * k) % shown.length;
      if (choosable(i)) return i;
    }
    return from;
  };
  const choose = (o: PickOption | undefined) => {
    if (!o || o.disabled) return;
    onAdd(o.value);
    setQuery("");
    setOpen(false);
    setActive(0);
    input.current?.focus();
  };
  const openAt = () => {
    setOpen(true);
    setActive(choosable(0) ? 0 : move(0, 1));
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) return openAt();
      if (shown.length) setActive((a) => move(a, e.key === "ArrowDown" ? 1 : -1));
    } else if (e.key === "Enter") {
      e.preventDefault(); // never submits a form around it
      if (open) choose(shown[active]);
      else if (query.trim()) openAt();
    } else if (e.key === "Escape" && open) {
      e.preventDefault();
      e.stopPropagation(); // closes the list, not the dialog
      setOpen(false);
    } else if (e.key === "Tab") {
      setOpen(false);
    }
  };
  const optionId = (i: number) => `${id}-opt-${i}`;
  const labelId = `${id}-label`;
  const descId = `${id}-desc`;
  return (
    <div ref={box} className="space-y-1.5" data-testid={testId}
      onBlur={(e) => { if (!box.current?.contains(e.relatedTarget as Node | null)) setOpen(false); }}>
      <label id={labelId} htmlFor={`${id}-input`} className="block text-sm font-medium">
        {label}{description && <span id={descId} className="font-normal text-muted"> — {description}</span>}
      </label>
      {values.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={`${label}: chosen`}>
          {values.map((v) => {
            const kind = chipKind?.(v);
            return (
              <li key={v} className="inline-flex items-center gap-1 rounded bg-surface-2 py-0.5 pl-2 text-sm">
                {kind && <span className="text-xs text-muted">{kind}</span>}
                <span>{chipLabel(v)}</span>
                <button type="button" className="rounded px-1.5 text-muted hover:text-danger pointer-coarse:min-h-11 pointer-coarse:min-w-11"
                  aria-label={`Remove ${chipLabel(v)} from ${label}`} onClick={() => { onRemove(v); input.current?.focus(); }}>×</button>
              </li>
            );
          })}
        </ul>
      )}
      <input ref={input} id={`${id}-input`} value={query} placeholder={placeholder} autoComplete="off" spellCheck={false}
        data-autofocus={autoFocus ? "" : undefined}
        role="combobox" aria-expanded={open} aria-controls={`${id}-list`} aria-autocomplete="list"
        aria-activedescendant={open && shown.length ? optionId(active) : undefined}
        className="h-9 w-full rounded-ui border border-line bg-surface px-3 text-sm text-fg outline-none placeholder:text-muted focus:border-accent focus:ring-2 focus:ring-accent/25"
        onChange={(e) => { setQuery(e.target.value); setOpen(true); setActive(0); }}
        onClick={() => (open ? setOpen(false) : openAt())}
        onKeyDown={onKey} />
      {open && (
        <div id={`${id}-list`} role="listbox" aria-label={`${label}: choices`}
          className="max-h-64 overflow-y-auto rounded-ui border border-line bg-surface p-1 shadow-sm">
          {shown.length === 0 && <p className="px-3 py-2 text-sm text-muted">{emptyText(query)}</p>}
          {groups.map((g) => (
            <div key={g} role="group" aria-labelledby={`${id}-g-${g}`}>
              <div id={`${id}-g-${g}`} className="px-3 pb-0.5 pt-2 text-[11px] font-semibold uppercase tracking-wider text-muted">{g}</div>
              {shown.filter((o) => o.group === g).map((o) => {
                const i = shown.indexOf(o);
                return (
                  <div key={o.value} id={optionId(i)} role="option" aria-selected={i === active} aria-disabled={o.disabled ? true : undefined}
                    data-value={o.value}
                    onMouseMove={() => !o.disabled && i !== active && setActive(i)} onMouseDown={(e) => e.preventDefault()} onClick={() => choose(o)}
                    className={cx("rounded-ui border-l-2 px-3 py-1.5 pointer-coarse:min-h-11", o.disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer",
                      i === active ? "border-accent bg-surface-2" : "border-transparent")}>
                    <span className="font-medium">{o.label}</span>
                    {(o.detail || o.disabled) && <span className="ml-2 text-xs text-muted">{o.disabled ?? o.detail}</span>}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
      )}
      <p className="sr-only" role="status" aria-live="polite">{open ? `${shown.length} choice${shown.length === 1 ? "" : "s"}` : ""}</p>
    </div>
  );
}
