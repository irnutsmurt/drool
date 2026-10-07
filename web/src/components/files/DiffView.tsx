import { cx } from "@/components/ui/cx";

/** Renders a unified diff with added/removed lines highlighted. */
export function DiffView({ diff, note }: { diff?: string; note?: string }) {
  if (!diff) return <p className="text-sm text-muted">{note || "No differences."}</p>;
  return (
    <pre className="max-h-96 overflow-auto rounded-ui border border-line bg-surface-2 p-2 font-mono text-xs leading-5">
      {diff.split("\n").map((l, i) => (
        <div key={i} className={cx(
          l.startsWith("+") && !l.startsWith("+++") && "bg-ok/15 text-ok",
          l.startsWith("-") && !l.startsWith("---") && "bg-danger/15 text-danger",
          l.startsWith("@@") && "text-accent",
          (l.startsWith("---") || l.startsWith("+++")) && "font-semibold text-muted",
        )}>{l || " "}</div>
      ))}
    </pre>
  );
}
