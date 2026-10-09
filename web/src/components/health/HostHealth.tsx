import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage } from "@/lib/format";
import { useHostHealth, type HealthSubject } from "./queries";
import { HealthChecksTable, IdentityBadge } from "./ui";

const kindWord: Record<string, string> = {
  filesystem: "Filesystems", disk: "Disks", gpu: "GPUs", sensor: "Temperature sensors", interface: "Network interfaces", service: "Services", probe: "Probe targets",
};

/** A host's health settings: where they come from, host-level
 * customization, and the subjects (filesystems, disks, GPUs, sensors,
 * interfaces, services) that can be customized one by one. */
export function HostHealthPanel({ hostId }: { hostId: string }) {
  const q = useHostHealth(hostId);
  const [subject, setSubject] = useState<HealthSubject>();
  const v = q.data;
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!v) return <p className="text-sm text-muted">Loading…</p>;
  const kinds = [...new Set((v.subjects ?? []).map((s) => s.kind))];
  return (
    <div className="space-y-4">
      <Card title="Health settings">
        <p className="text-sm">
          <b>{v.summary?.text}</b>
          {v.summary?.using_recommended && <span className="text-muted"> — the same as every other host unless you customize something.</span>}
        </p>
        {(v.groups ?? []).length > 0 && (
          <p className="mt-1 text-xs text-muted">
            Groups: {(v.groups ?? []).map((g) => `${g.name} (priority ${g.priority}${g.overrides ? `, ${g.overrides} overrides` : ""})`).join(", ")}.
            {" "}When groups disagree, the higher priority wins; equal priorities are shown as a conflict.
          </p>
        )}
        {(v.summary?.conflicts ?? 0) > 0 && (
          <div className="mt-2"><Alert tone="warn">Groups of equal priority set different values for this host. Until a priority or a host override settles it, those fields use the global or Recommended value.</Alert></div>
        )}
        <p className="mt-2 text-xs text-muted">
          These decide what monitoring considers unhealthy here. Who gets notified is configured in <Link className="text-accent" to="/settings/notifications">Settings → Notifications</Link>.
        </p>
      </Card>
      <Card title="Checks for this host">
        <HealthChecksTable checks={v.checks} k={{ scope: "host", host_id: hostId }} title={(c) => `${c.label} on this host`} />
      </Card>
      <Card title="Individual devices and services">
        <p className="mb-3 text-sm text-muted">
          Customize one filesystem, disk, GPU, sensor, interface or service. Settings follow the device's own identity (a filesystem UUID,
          a disk's WWN, a GPU UUID), not its current name.
        </p>
        {kinds.length === 0 && <p className="text-sm text-muted">Shown once the host has reported.</p>}
        {kinds.map((k) => (
          <div key={k} className="mb-3">
            <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted">{kindWord[k] ?? k}</h3>
            <ul className="divide-y divide-line">
              {(v.subjects ?? []).filter((s) => s.kind === k).map((s) => (
                <li key={s.key} className="flex flex-wrap items-center gap-2 py-1.5 text-sm">
                  <span className="font-medium">{s.label}</span>
                  <IdentityBadge s={s} />
                  {s.customized && <Badge tone="accent">Customized</Badge>}
                  <Button size="sm" className="ml-auto" onClick={() => setSubject(s)} aria-label={`Customize health check for ${s.label}`}>
                    {s.customized ? "Edit" : "Customize"}
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </Card>
      {subject && <SubjectHealthDialog hostId={hostId} subject={subject} onClose={() => setSubject(undefined)} />}
    </div>
  );
}

/** The health checks of one subject (opened from the health settings tab
 * or from a filesystem, disk, sensor, GPU or service row). */
export function SubjectHealthDialog({ hostId, subject, onClose }: { hostId: string; subject: HealthSubject; onClose: () => void }) {
  const q = useHostHealth(hostId, subject.key);
  const v = q.data;
  return (
    <Dialog title={`Health checks: ${subject.label}`} onClose={onClose} wide>
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
        <IdentityBadge s={subject} />
        <span className="text-muted">{subject.present ? subject.identity : "The host doesn't report it now. Its settings stay and apply again if it returns with the same identity."}</span>
      </div>
      {subject.strength === "weak" && (
        <div className="mb-3"><Alert tone="warn">DROOL only knows this by its position or kernel name, which can refer to another device after a reboot or a hardware change.</Alert></div>
      )}
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {!v ? <p className="text-sm text-muted">Loading…</p> : (
        <HealthChecksTable checks={v.checks} k={{ scope: "host", host_id: hostId, subject: subject.key }} title={(c) => `${c.label}: ${subject.label}`} />
      )}
      <div className="mt-4"><Button onClick={onClose}>Close</Button></div>
    </Dialog>
  );
}

/** "Customize health check" for one row of a monitoring page; ref is
 * what that page calls the thing (a mount, a sensor id, gpu:<index>…). */
export function SubjectHealthButton({ hostId, kind, subjectRef: r, label }: { hostId: string; kind: string; subjectRef: string; label: string }) {
  const { can } = useAuth();
  const q = useHostHealth(hostId, undefined, can("monitoring.read"));
  const [open, setOpen] = useState(false);
  const s = (q.data?.subjects ?? []).find((x) => x.kind === kind && x.ref === r);
  if (!s) return null;
  return (
    <>
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)} aria-label={`Customize health check for ${label}`}>
        {s.customized ? "⚙ Customized" : "Customize health check"}
      </Button>
      {open && <SubjectHealthDialog hostId={hostId} subject={s} onClose={() => setOpen(false)} />}
    </>
  );
}
