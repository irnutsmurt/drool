import { useTheme, type ThemeChoice } from "@/lib/theme";
import { cx } from "./cx";

const choices: { value: ThemeChoice; label: string; hint: string }[] = [
  { value: "light", label: "Light", hint: "Always light" },
  { value: "dark", label: "Dark", hint: "Always dark" },
  { value: "system", label: "System", hint: "Follow the operating system / browser setting" },
];

/** Light / Dark / System as a native radio group (keyboard: arrow keys). */
export function ThemeSwitcher({ compact = false, name }: { compact?: boolean; name?: string }) {
  const { choice, resolved, setTheme } = useTheme();
  return (
    <fieldset className={cx("flex items-center gap-2", compact ? "text-xs" : "text-sm")}>
      <legend className={compact ? "sr-only" : "mb-2 text-sm font-medium"}>Appearance</legend>
      <div className="inline-flex overflow-hidden rounded-ui border border-line" data-testid="theme-switcher">
        {choices.map((c) => (
          <label key={c.value} title={c.hint}
            className={cx("relative cursor-pointer px-2.5 py-1 focus-within:outline focus-within:outline-2 focus-within:outline-accent",
              choice === c.value ? "bg-accent text-accent-fg" : "bg-surface text-fg hover:bg-surface-2")}>
            <input type="radio" name={name ?? (compact ? "theme-compact" : "theme")} value={c.value} checked={choice === c.value}
              onChange={() => setTheme(c.value)} className="sr-only" />
            {c.label}
          </label>
        ))}
      </div>
      {!compact && choice === "system" && <span className="text-xs text-muted">currently {resolved}</span>}
    </fieldset>
  );
}
