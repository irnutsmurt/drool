import { cx } from "@/components/ui/cx";
import { CapsList } from "./CapsList";
import { usePresets } from "./queries";

const blurbs: Record<string, string> = {
  monitor: "Reports inventory and health. Changes nothing.",
  managed: "Services, reboots and packages from configured repositories. No arbitrary files, no shell.",
  full: "Everything, including shell and unrestricted files.",
};

/** Shows exactly what each installer preset allows before installation. */
export function PresetPicker({ value, onChange }: { value: string; onChange: (p: string) => void }) {
  const presets = usePresets();
  return (
    <div className="grid gap-3 md:grid-cols-3">
      {(presets.data?.items ?? []).map((p) => (
        <button key={p.name} type="button" onClick={() => onChange(p.name)}
          className={cx("flex flex-col items-stretch justify-start rounded-ui border p-3 text-left", value === p.name ? "border-accent ring-2 ring-accent/30" : "border-line hover:border-accent/50")}>
          <div className="mb-1 font-semibold capitalize">{p.name}</div>
          <p className="mb-2 text-xs text-muted">{blurbs[p.name]}</p>
          <CapsList caps={p.caps} compact />
        </button>
      ))}
    </div>
  );
}
