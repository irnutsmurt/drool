import { useRef } from "react";
import type { Problem } from "./useDesigner";

/**
 * The YAML form of the same document. Edits are checked as you type; once
 * they parse, Topology and Rules show them too. Comments and formatting are
 * not kept: leaving this view shows the canonical form.
 */
export function YamlView({ text, onChange, problems, broken, readOnly }: {
  text: string; onChange: (t: string) => void; problems: Problem[]; broken: boolean; readOnly: boolean;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const lines = text.split("\n").length;
  const bad = new Set(problems.map((p) => p.line).filter(Boolean));
  const goTo = (line: number) => {
    const ta = area.current;
    if (!ta) return;
    const offset = text.split("\n").slice(0, line - 1).reduce((n, l) => n + l.length + 1, 0);
    ta.focus();
    ta.setSelectionRange(offset, offset + (text.split("\n")[line - 1]?.length ?? 0));
    ta.scrollTop = Math.max(0, (line - 5) * 20);
  };
  return (
    <div className="flex h-full flex-col gap-2">
      <p className="text-xs text-muted">
        The same policy as the topology and the rules, as YAML. It goes through the same validation, management-path checks, preview and safe apply.
        Comments and custom formatting are not kept. Names can be written bare when unambiguous (<code>web1</code>), or as <code>host:web1</code>, <code>group:web</code>, <code>network:admin-lan</code>; addresses as CIDRs; and <code>internet</code>, <code>internal</code>, <code>any</code>.
      </p>
      {broken && <div role="alert" className="rounded-ui border border-danger bg-danger-bg px-3 py-1.5 text-sm">This YAML doesn&apos;t parse yet; the topology and rules show the last version that did.</div>}
      <div className="flex min-h-0 flex-1 overflow-hidden rounded-ui border border-line bg-surface font-mono text-[13px] leading-5">
        <div ref={gutter} aria-hidden className="select-none overflow-hidden border-r border-line bg-surface-2 px-2 py-2 text-right text-muted">
          {Array.from({ length: lines }, (_, i) => (
            <div key={i} className={bad.has(i + 1) ? "font-bold text-danger" : undefined}>{i + 1}</div>
          ))}
        </div>
        <textarea
          ref={area} aria-label="Firewall policy as YAML" spellCheck={false} readOnly={readOnly}
          className="min-h-0 flex-1 resize-none bg-surface p-2 text-fg outline-none"
          style={{ tabSize: 2, whiteSpace: "pre", overflowWrap: "normal" }}
          value={text} onChange={(e) => onChange(e.target.value)}
          onScroll={(e) => { if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop; }}
          onKeyDown={(e) => {
            if (e.key === "Tab" && !e.shiftKey && !readOnly) {
              // Two spaces, not focus change, while editing (Esc then Tab leaves the editor).
              e.preventDefault();
              const ta = e.currentTarget;
              const { selectionStart: s, selectionEnd: en } = ta;
              onChange(text.slice(0, s) + "  " + text.slice(en));
              requestAnimationFrame(() => ta.setSelectionRange(s + 2, s + 2));
            }
            if (e.key === "Escape") e.currentTarget.blur();
          }}
        />
      </div>
      {problems.length > 0 && (
        <ul className="max-h-32 overflow-y-auto text-xs" aria-label="YAML problems">
          {problems.map((p, i) => (
            <li key={i}>
              {p.line ? <button type="button" className="text-danger underline" onClick={() => goTo(p.line!)}>Line {p.line}</button> : <span className="text-danger">Problem</span>}: {p.message}
            </li>
          ))}
        </ul>
      )}
      <p className="text-[11px] text-muted">Tab inserts two spaces; press Esc, then Tab, to leave the editor.</p>
    </div>
  );
}
