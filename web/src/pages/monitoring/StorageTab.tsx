import { SubjectHealthButton } from "@/components/health/HostHealth";
import { Fragment, useState } from "react";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Select } from "@/components/ui/Field";
import { errorMessage, formatBytes } from "@/lib/format";
import { capabilityWord, deviceHealth, StatusBadge, statusOf } from "@/components/monitoring/Status";
import { fmtHours, fmtSince, fmtTemp, fmtTime } from "@/components/monitoring/format";
import { InstallHelperButton } from "@/components/monitoring/HelperInstall";
import { useSaveHostSettings, useStorage, type MonDevice, type MonEvent, type MonHostOverview, type MonProbe } from "@/components/monitoring/queries";

const kindWord: Record<string, string> = {
  hdd: "Hard disk", ssd: "SATA/SAS SSD", nvme: "NVMe SSD", "usb-flash": "USB flash drive", "usb-sata": "SATA drive via USB bridge",
  "usb-nvme": "NVMe drive in a USB enclosure", usb: "USB storage", sd: "SD / microSD card", emmc: "eMMC", virtual: "Virtual disk", unknown: "Disk",
};

const PROBE_WARNING = "Frequent active storage probes create additional writes to flash media. Once per hour is recommended for SD cards. Shorter intervals may contribute unnecessary wear.";

function Facts({ rows }: { rows: [string, string | undefined | null][] }) {
  const shown = rows.filter(([, v]) => v != null && v !== "");
  if (!shown.length) return null;
  return (
    <dl className="grid grid-cols-[minmax(10rem,auto)_1fr] gap-x-4 gap-y-1 text-sm">
      {shown.map(([k, v]) => (<Fragment key={k}><dt className="text-muted">{k}</dt><dd>{v}</dd></Fragment>))}
    </dl>
  );
}

const n = (v: number | null | undefined) => (v == null ? undefined : String(v));

function evidence(dev: MonDevice, events: MonEvent[]) {
  const mine = events.filter((e) => e.subject === dev.name);
  const kinds = (ks: string[]) => mine.filter((e) => {
    const d = e.data as { kind?: string } | undefined;
    return d?.kind && ks.includes(d.kind);
  }).length;
  return {
    io: kinds(["io_error", "medium_error", "fs_error"]),
    mmc: kinds(["mmc_error", "timeout"]),
    usb: kinds(["usb_reset", "usb_disconnect"]),
  };
}

function DeviceCard({ id, d, events, probes, settings, canProbe, fs }: {
  id: string; d: MonDevice; events: MonEvent[]; probes: Record<string, MonProbe>; settings: MonHostOverview["settings"]; canProbe: boolean;
  fs: { mount: string; read_only: boolean }[];
}) {
  const h = deviceHealth[d.health.state] ?? deviceHealth.unknown!;
  const ev = evidence(d, events);
  const mounts = [...(d.mounts ?? []), ...d.partitions.flatMap((p) => p.mounts ?? [])];
  const limited = d.capability === "limited" || d.kind === "sd" || d.kind === "emmc" || d.kind === "usb-flash";
  const probeOf = mounts.map((m) => probes[m]).find(Boolean);
  const s = d.smart;
  const v = d.nvme;
  return (
    <Card title={`${d.name}${d.model ? ` · ${d.model}` : ""}`} actions={<StatusBadge status={h.status}>{h.word}</StatusBadge>}>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge>{kindWord[d.kind] ?? d.kind}</Badge>
          <span className="order-last ml-auto"><SubjectHealthButton hostId={id} kind="disk" subjectRef={d.key} label={d.name} /></span>
          <span>{formatBytes(d.size_bytes)}</span>
          <span className="text-muted">Health telemetry: <strong className="text-fg">{capabilityWord[d.capability] ?? d.capability}</strong></span>
          {d.standby && <Badge tone="accent">In standby (not woken)</Badge>}
        </div>
        <p className="text-sm">{d.health.summary}</p>
        {d.capability_note && <p className="text-xs text-muted">{d.capability_note}</p>}
        {d.helper && <div className="flex items-center gap-2"><InstallHelperButton hostId={id} pkg={d.helper} /></div>}
        {s && (
          <Facts rows={[
            ["SMART overall result", s.passed == null ? undefined : s.passed ? "Passed" : "FAILED"],
            ["Temperature", s.celsius == null ? undefined : fmtTemp(s.celsius)],
            ["Power-on time", s.power_on_hours == null ? undefined : fmtHours(s.power_on_hours)],
            ["Power cycles", n(s.power_cycles)],
            ["Reallocated sectors", n(s.reallocated)],
            ["Pending sectors", n(s.pending)],
            ["Offline uncorrectable", n(s.offline_uncorrectable)],
            ["Interface CRC errors", n(s.crc_errors)],
            ["Grown defects", n(s.grown_defects)],
            ["Endurance used", s.endurance_used_percent == null ? undefined : `${s.endurance_used_percent}%${s.endurance_vendor_specific ? " (vendor-specific attribute)" : ""}`],
            ["Written", s.bytes_written == null ? undefined : formatBytes(s.bytes_written)],
            ["Last self-test", s.last_self_test],
          ]} />
        )}
        {v && (
          <Facts rows={[
            ["Critical warnings", v.critical_warning == null ? undefined : v.warnings?.length ? v.warnings.join("; ") : "None"],
            ["Temperature", v.celsius == null ? undefined : fmtTemp(v.celsius)],
            ["Available spare", v.available_spare == null ? undefined : `${v.available_spare}%${v.spare_threshold != null ? ` (threshold ${v.spare_threshold}%)` : ""}`],
            ["Endurance used", v.percent_used == null ? undefined : `${v.percent_used}%`],
            ["Media and data integrity errors", n(v.media_errors)],
            ["Error log entries", n(v.error_log_entries)],
            ["Unsafe shutdowns", n(v.unsafe_shutdowns)],
            ["Power-on time", v.power_on_hours == null ? undefined : fmtHours(v.power_on_hours)],
            ["Power cycles", n(v.power_cycles)],
            ["Read / written", v.bytes_read == null ? undefined : `${formatBytes(v.bytes_read)} / ${formatBytes(v.bytes_written)}`],
            ["Last self-test", v.last_self_test],
          ]} />
        )}
        {d.emmc && (
          <Facts rows={[["Life time estimate (A)", d.emmc.life_a], ["Life time estimate (B)", d.emmc.life_b], ["Reserved blocks (pre-EOL)", d.emmc.pre_eol]]} />
        )}
        {limited && (
          <Facts rows={[
            ["Wear remaining", d.emmc?.life_a ? undefined : "Not available"],
            ["Filesystem", (() => {
              const mine = fs.filter((f) => mounts.includes(f.mount));
              if (!mine.length) return undefined;
              const ro = mine.filter((f) => f.read_only).map((f) => f.mount);
              return ro.length ? `Read-only: ${ro.join(", ")}` : "Read/write";
            })()],
            ["Block device", d.read_only ? "Read-only" : "Read/write"],
            ["Write/read probe", probeOf ? (probeOf.ok ? `Passed ${fmtSince(probeOf.at * 1000)}` : `Failed (${probeOf.stage}): ${probeOf.error}`) : "Not enabled"],
            ["Kernel IO errors", ev.io ? `${ev.io} recently` : "None"],
            [d.transport === "mmc" ? "MMC errors/resets" : "Timeouts", ev.mmc ? `${ev.mmc} recently` : "None"],
            ...(d.transport === "usb" ? [["USB resets/disconnects", ev.usb ? `${ev.usb} recently` : "None"] as [string, string]] : []),
          ]} />
        )}
        {d.evidence.io_errors != null && d.evidence.io_errors > 0 && (
          <p className="text-xs text-muted">The kernel counted {d.evidence.io_errors} failed IO commands on this device since it appeared (any kind, including harmless ones).</p>
        )}
        <div className="text-sm">
          <div className="text-xs text-muted">Partitions and filesystems</div>
          <ul className="mt-1 space-y-0.5">
            {(d.mounts ?? []).map((m) => <li key={m} className="font-mono text-xs">{d.name} → {m}</li>)}
            {d.partitions.map((p) => (
              <li key={p.name} className="font-mono text-xs">
                {p.name} ({formatBytes(p.size_bytes)}){p.via ? ` → ${p.via}` : ""}{p.mounts?.length ? ` → ${p.mounts.join(", ")}` : ""}
                {p.uuid && <span className="text-muted"> · UUID {p.uuid}</span>}
              </li>
            ))}
            {d.partitions.length === 0 && !(d.mounts ?? []).length && <li className="text-xs text-muted">None mounted</li>}
          </ul>
          <div className="mt-1 text-xs text-muted">Identified by <span className="font-mono">{d.key}</span>{d.serial ? ` · serial ${d.serial}` : ""}{d.firmware ? ` · firmware ${d.firmware}` : ""}</div>
        </div>
        {canProbe && limited && mounts.length > 0 && <ProbeControl id={id} mounts={mounts} settings={settings} />}
        {(d.raw?.length ?? 0) > 0 && (
          <details className="text-sm">
            <summary className="cursor-pointer text-accent">Advanced: raw SMART attributes</summary>
            <p className="mt-1 text-xs text-muted">Attribute meanings vary by manufacturer; DROOL only interprets attributes whose names are standardized.</p>
            {/* The columns only make sense side by side: on a phone it scrolls. */}
            <div className="mt-2 overflow-x-auto">
              <table className="w-full text-left text-xs">
                <thead><tr className="border-b border-line text-muted"><th>ID</th><th>Name</th><th>Value</th><th>Worst</th><th>Threshold</th><th>Raw</th><th>Failed</th></tr></thead>
                <tbody>{d.raw!.map((a) => (
                  <tr key={a.id} className="border-b border-line last:border-0"><td>{a.id}</td><td className="font-mono">{a.name}</td><td>{a.value}</td><td>{a.worst}</td><td>{a.thresh}</td><td className="font-mono">{a.raw}</td><td>{a.when_failed || "—"}</td></tr>
                ))}</tbody>
              </table>
            </div>
          </details>
        )}
      </div>
    </Card>
  );
}

function ProbeControl({ id, mounts, settings }: { id: string; mounts: string[]; settings: MonHostOverview["settings"] }) {
  const save = useSaveHostSettings(id);
  const [every, setEvery] = useState(settings.probe_s ?? 3600);
  const { can } = useAuth();
  if (!can("monitoring.manage")) return null;
  const probeMounts = settings.probe_mounts;
  const set = (mountsOn: string[], interval: number) => save.mutate({ ...settings, probe_mounts: mountsOn, probe_s: interval });
  return (
    <div className="space-y-2 rounded-ui border border-line p-3">
      <div className="text-sm font-medium">Write/read probe</div>
      <p className="text-xs text-muted">
        Writes one 512-byte value to a DROOL-owned file on the filesystem, flushes it to the device and reads it back. One
        signal among several: a pass does not prove every flash block is healthy.
      </p>
      {save.error && <Alert>{errorMessage(save.error)}</Alert>}
      {mounts.map((m) => (
        <Checkbox key={m} label={`Probe ${m}`} checked={probeMounts.includes(m)} disabled={save.isPending}
          onChange={(on) => set(on ? [...new Set([...probeMounts, m])] : probeMounts.filter((x) => x !== m), every)} />
      ))}
      <Field label="Interval">
        <Select value={every} onChange={(e) => { const v = Number(e.target.value); setEvery(v); if (probeMounts.length) set(probeMounts, v); }} className="max-w-xs">
          <option value={3600}>Every hour (recommended)</option>
          <option value={21600}>Every 6 hours</option>
          <option value={86400}>Daily</option>
          <option value={900}>Every 15 minutes</option>
          <option value={300}>Every 5 minutes</option>
        </Select>
      </Field>
      {every < 3000 && <Alert tone="warn">{PROBE_WARNING}</Alert>}
    </div>
  );
}

export function StorageTab({ id, o }: { id: string; o: MonHostOverview }) {
  const q = useStorage(id);
  const v = q.data;
  return (
    <div className="space-y-4">
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      <p className="text-sm text-muted">
        Physical device health, separate from filesystem space (see Metrics). Devices that can't report health are judged only on what
        Linux sees (IO errors, resets, read-only changes) and are never called healthy just because nothing could be read.
        {v && v.updated_at ? ` Health read ${fmtSince(v.updated_at)}${v.source === "ssh" ? " over SSH" : ""}.` : ""}
      </p>
      {v && v.devices.length === 0 && <Card><p className="text-sm text-muted">{v.note || "No physical storage devices reported yet."}</p></Card>}
      {v?.devices.map((d) => (
        <DeviceCard key={d.key} id={id} d={d} events={v.events} probes={v.probes} settings={o.settings}
          canProbe={o.source === "agent"} fs={o.state?.filesystems ?? []} />
      ))}
      {v && v.events.length > 0 && (
        <Card title="Recent kernel storage messages">
          <ul className="space-y-1 text-xs">
            {v.events.slice(0, 20).map((e) => (
              <li key={e.id} className="flex gap-2"><StatusBadge status={statusOf(e.severity)} /><span className="text-muted">{fmtTime(e.ts)}</span><span className="font-mono">{e.message}</span></li>
            ))}
          </ul>
          <p className="mt-2 text-xs text-muted">Only messages matching known storage problems are kept; the rest of the kernel log is never collected.</p>
        </Card>
      )}
      {o.source === "ssh" && <p className="text-xs text-muted">The write/read probe needs a Shepherd (DROOL agent).</p>}
    </div>
  );
}
