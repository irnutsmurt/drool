import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import type { Recurrence } from "./queries";

/** The patterns offered (no cron). "Selected weekdays" and "every week"
 * are both weekly recurrences; they differ only in how they're chosen. */
export type RecurMode = "once" | "hourly" | "daily" | "weekdays" | "weekly" | "monthly";

export const recurModes: { mode: RecurMode; label: string }[] = [
  { mode: "once", label: "Once" },
  { mode: "hourly", label: "Every few hours" },
  { mode: "daily", label: "Every day" },
  { mode: "weekdays", label: "Selected weekdays" },
  { mode: "weekly", label: "Every week" },
  { mode: "monthly", label: "Every month" },
];

const days = [["mon", "Mon"], ["tue", "Tue"], ["wed", "Wed"], ["thu", "Thu"], ["fri", "Fri"], ["sat", "Sat"], ["sun", "Sun"]] as const;
const dayNames: Record<string, string> = { sun: "Sunday", mon: "Monday", tue: "Tuesday", wed: "Wednesday", thu: "Thursday", fri: "Friday", sat: "Saturday" };

export interface RecurState {
  mode: RecurMode;
  at: string; // once / hourly first run: "YYYY-MM-DDTHH:MM" in the zone
  every: string; // hours
  time: string; // "HH:MM"
  weekdays: string[];
  weekday: string;
  dom: string;
}

/** "YYYY-MM-DDTHH:MM" for an instant in a zone. */
export function localIn(zone: string, d = new Date()): string {
  try {
    const p = Object.fromEntries(new Intl.DateTimeFormat("en-CA", {
      timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
    }).formatToParts(d).map((x) => [x.type, x.value]));
    return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}`;
  } catch {
    return "";
  }
}

/** The browser's zone: a sensible default the user can change. The
 * controller's zone is never assumed. */
export const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

export function defaultRecur(zone: string): RecurState {
  // Tomorrow at 03:00: a one-time run, or an hourly schedule's first run.
  const at = localIn(zone, new Date(Date.now() + 86400_000)).slice(0, 10) + "T03:00";
  return { mode: "weekly", at, every: "6", time: "03:00", weekdays: ["sun"], weekday: "sun", dom: "1" };
}

export function recurFrom(r: Recurrence, zone: string): RecurState {
  const d = defaultRecur(zone);
  switch (r.kind) {
    case "once":
      return { ...d, mode: "once", at: r.at ?? d.at };
    case "hourly":
      return { ...d, mode: "hourly", at: r.at ?? d.at, every: String(r.every_hours ?? 6) };
    case "daily":
      return { ...d, mode: "daily", time: r.time ?? d.time };
    case "weekly": {
      const wd = r.weekdays ?? [];
      return wd.length === 1
        ? { ...d, mode: "weekly", time: r.time ?? d.time, weekday: wd[0]!, weekdays: wd }
        : { ...d, mode: "weekdays", time: r.time ?? d.time, weekdays: wd };
    }
    case "monthly":
      return { ...d, mode: "monthly", time: r.time ?? d.time, dom: String(r.day_of_month ?? 1) };
  }
}

export function recurrenceFrom(s: RecurState): Recurrence {
  switch (s.mode) {
    case "once":
      return { kind: "once", at: s.at };
    case "hourly":
      return { kind: "hourly", at: s.at, every_hours: Number(s.every) || 0 };
    case "daily":
      return { kind: "daily", time: s.time };
    case "weekdays":
      return { kind: "weekly", time: s.time, weekdays: s.weekdays as Recurrence["weekdays"] };
    case "weekly":
      return { kind: "weekly", time: s.time, weekdays: [s.weekday] as Recurrence["weekdays"] };
    case "monthly":
      return { kind: "monthly", time: s.time, day_of_month: Number(s.dom) || 0 };
  }
}

function zones(): string[] {
  try {
    return Intl.supportedValuesOf("timeZone");
  } catch {
    return ["UTC"];
  }
}

/** Time zone choice: explicit, never the controller's. */
export function ZoneSelect({ value, onChange }: { value: string; onChange: (z: string) => void }) {
  const all = useMemo(zones, []);
  const mine = browserZone();
  const suggested = [...new Set([mine, "UTC"])];
  return (
    <Field label="Time zone" hint="Times are in this zone, also when DROOL's own server is elsewhere.">
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        {!all.includes(value) && !suggested.includes(value) && <option value={value}>{value}</option>}
        <optgroup label="Suggested">
          {suggested.map((z) => <option key={`s-${z}`} value={z}>{z}{z === mine ? " (this browser)" : ""}</option>)}
        </optgroup>
        <optgroup label="All time zones">
          {all.filter((z) => !suggested.includes(z)).map((z) => <option key={z} value={z}>{z}</option>)}
        </optgroup>
      </Select>
    </Field>
  );
}

/** When something runs: a pattern, its time and the zone. */
export function RecurrenceFields({ v, set, zone, setZone, allowOnce = true }: {
  v: RecurState;
  set: (v: RecurState) => void;
  zone: string;
  setZone: (z: string) => void;
  allowOnce?: boolean;
}) {
  const modes = allowOnce ? recurModes : recurModes.filter((m) => m.mode !== "once");
  const toggle = (d: string, on: boolean) => set({ ...v, weekdays: on ? [...v.weekdays, d] : v.weekdays.filter((x) => x !== d) });
  return (
    <div className="space-y-4">
      <fieldset>
        <legend className="mb-1.5 text-sm font-medium">Repeat</legend>
        <div className="flex flex-wrap gap-2">
          {modes.map((m) => (
            <label key={m.mode} className={cx("cursor-pointer rounded-ui border px-3 py-1.5 text-sm has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent",
              v.mode === m.mode ? "border-accent bg-accent/15 font-medium" : "border-line")}>
              <input type="radio" name="repeat" className="sr-only" checked={v.mode === m.mode} onChange={() => set({ ...v, mode: m.mode })} />
              {m.label}
            </label>
          ))}
        </div>
      </fieldset>
      <div className="grid gap-4 sm:grid-cols-2">
        {v.mode === "once" && (
          <Field label="Date and time">
            <Input type="datetime-local" value={v.at} onChange={(e) => set({ ...v, at: e.target.value })} required />
          </Field>
        )}
        {v.mode === "hourly" && (
          <>
            <Field label="Every (hours)" hint="1–168. Counted in real hours, so across a clock change the local times shift by an hour.">
              <Input type="number" min={1} max={168} value={v.every} onChange={(e) => set({ ...v, every: e.target.value })} />
            </Field>
            <Field label="First run">
              <Input type="datetime-local" value={v.at} onChange={(e) => set({ ...v, at: e.target.value })} required />
            </Field>
          </>
        )}
        {v.mode === "weekly" && (
          <Field label="Day">
            <Select value={v.weekday} onChange={(e) => set({ ...v, weekday: e.target.value })}>
              {days.map(([k]) => <option key={k} value={k}>{dayNames[k]}</option>)}
            </Select>
          </Field>
        )}
        {v.mode === "monthly" && (
          <Field label="Day of the month" hint="1–31. In shorter months, days they don't have mean the last day.">
            <Input type="number" min={1} max={31} value={v.dom} onChange={(e) => set({ ...v, dom: e.target.value })} />
          </Field>
        )}
        {v.mode !== "once" && v.mode !== "hourly" && (
          <Field label="Time">
            <Input type="time" value={v.time} onChange={(e) => set({ ...v, time: e.target.value })} required />
          </Field>
        )}
        <ZoneSelect value={zone} onChange={setZone} />
      </div>
      {v.mode === "weekdays" && (
        <fieldset>
          <legend className="mb-1.5 text-sm font-medium">Days</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {days.map(([k, label]) => <Checkbox key={k} checked={v.weekdays.includes(k)} onChange={(on) => toggle(k, on)} label={label} />)}
          </div>
        </fieldset>
      )}
    </div>
  );
}

/** The next runs, computed by the controller (with daylight-saving notes). */
export function NextRunsList({ recurrence, zone, count = 5 }: { recurrence: Recurrence; zone: string; count?: number }) {
  const q = useQuery({
    queryKey: ["schedules", "next-runs", recurrence, zone, count],
    queryFn: () => unwrap(api.POST("/schedules/next-runs", { body: { recurrence, time_zone: zone, count } })),
    retry: false,
  });
  if (q.error) return <p className="text-sm text-danger">{errorMessage(q.error)}</p>;
  if (!q.data) return <p className="text-sm text-muted">…</p>;
  return (
    <div className="text-sm">
      <p className="font-medium">{q.data.when}</p>
      {q.data.runs.length === 0 ? <p className="text-warn">That time has already passed: it would never run.</p> : (
        <ul className="mt-1 space-y-0.5 text-muted" aria-label="Next runs">
          {q.data.runs.map((r) => (
            <li key={r.at}>{r.local}{r.note && <span className="block text-xs text-warn">{r.note}</span>}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
