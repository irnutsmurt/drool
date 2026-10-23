import { SubjectHealthButton } from "@/components/health/HostHealth";
import { Card } from "@/components/ui/Card";
import { StatusBadge } from "@/components/monitoring/Status";
import { fmtPct, fmtTemp } from "@/components/monitoring/format";
import { formatBytes } from "@/lib/format";
import type { MonHostOverview, MonSensor } from "@/components/monitoring/queries";

const kindWord: Record<string, string> = { cpu: "CPU", board: "Motherboard / platform", storage: "Storage", gpu: "GPU", other: "Other" };

function sensorStatus(s: MonSensor) {
  if (s.critical != null && s.celsius >= s.critical) return <StatusBadge status="critical">At critical limit</StatusBadge>;
  if (s.high != null && s.celsius >= s.high) return <StatusBadge status="warning">Above high limit</StatusBadge>;
  if (s.high == null && s.critical == null) return <span className="text-xs text-muted">No hardware limit reported</span>;
  return <StatusBadge status="ok">Within limits</StatusBadge>;
}

export function HardwareTab({ o }: { o: MonHostOverview }) {
  const st = o.state;
  const sensors = st?.sensors ?? [];
  const gpus = st?.gpus ?? [];
  return (
    <div className="space-y-4">
      <Card title="Temperatures">
        {sensors.length === 0 ? (
          <p className="text-sm text-muted">This host exposes no temperature sensors. That is normal for virtual machines and containers; on physical machines the sensor chip's kernel driver may not be loaded.</p>
        ) : (
          <table className="rtable w-full text-left text-sm">
            <caption className="sr-only">Temperature sensors</caption>
            <thead><tr className="border-b border-line text-xs text-muted">
              <th className="py-1 font-medium">Sensor</th><th className="py-1 font-medium">Type</th><th className="py-1 font-medium">Now</th>
              <th className="py-1 font-medium">High</th><th className="py-1 font-medium">Critical</th><th className="py-1 font-medium">State</th>
              <th className="py-1"><span className="sr-only">Health checks</span></th>
            </tr></thead>
            <tbody>
              {sensors.map((s) => (
                <tr key={s.id} className="border-b border-line last:border-0">
                  <td data-primary="" className="py-1"><span className="font-mono text-xs">{s.chip}</span> {s.label}{s.device ? <span className="text-muted"> · {s.device}</span> : ""}</td>
                  <td data-label="Type" className="py-1 text-xs">{kindWord[s.kind] ?? s.kind}</td>
                  <td data-label="Now" className="py-1 tabular-nums">{fmtTemp(s.celsius)}</td>
                  <td data-label="High" className="py-1 tabular-nums">{fmtTemp(s.high)}</td>
                  <td data-label="Critical" className="py-1 tabular-nums">{fmtTemp(s.critical)}</td>
                  <td data-label="State" className="py-1">{sensorStatus(s)}</td>
                  <td data-label="" className="py-1 text-right"><SubjectHealthButton hostId={o.host_id} kind="sensor" subjectRef={s.id} label={`${s.chip} ${s.label}`} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="mt-2 text-xs text-muted">Limits are the hardware's own. DROOL never applies one "danger temperature" to unrelated hardware; a custom limit applies only where you set one (Customize health check).</p>
      </Card>
      <Card title="GPU">
        {gpus.length === 0 && !st?.gpu_note ? (
          <p className="text-sm text-muted">No GPU found.</p>
        ) : (
          <div className="space-y-3">
            {st?.gpu_note && <p className="text-sm text-muted">{st.gpu_note}</p>}
            {gpus.map((g) => (
              <div key={g.index} className="rounded-ui border border-line p-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{g.model} <span className="text-xs text-muted">({g.vendor}, via {g.source})</span></span>
                  <span className="ml-auto"><SubjectHealthButton hostId={o.host_id} kind="gpu" subjectRef={`gpu:${g.index}`} label={g.model} /></span>
                </div>
                {g.note && <p className="text-xs text-muted">{g.note}</p>}
                <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 sm:grid-cols-4">
                  <dt className="text-muted">Utilization</dt><dd>{g.util_percent == null ? "Not available" : fmtPct(g.util_percent)}</dd>
                  <dt className="text-muted">VRAM</dt><dd>{g.mem_used_bytes == null || g.mem_total_bytes == null ? "Not available" : `${formatBytes(g.mem_used_bytes)} of ${formatBytes(g.mem_total_bytes)}`}</dd>
                  <dt className="text-muted">Temperature</dt><dd>{g.celsius == null ? "Not available" : fmtTemp(g.celsius)}</dd>
                  <dt className="text-muted">Power</dt><dd>{g.power_w == null ? "Not available" : `${g.power_w.toFixed(0)} W`}{g.power_state ? ` (${g.power_state})` : ""}</dd>
                </dl>
              </div>
            ))}
            <p className="text-xs text-muted">GPU telemetry comes from the vendor's driver tools where installed (nvidia-smi) or the kernel (AMD, Intel). DROOL never installs GPU drivers. High utilization isn't a health problem unless you enable that check: a busy GPU is usually working.</p>
          </div>
        )}
      </Card>
    </div>
  );
}
