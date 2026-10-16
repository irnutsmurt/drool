import { useEffect, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { DiffView } from "@/components/files/DiffView";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage, formatBytes } from "@/lib/format";
import { describe, parseOctal } from "@/lib/modes";
import { upload } from "@/lib/upload";

export interface FileDeployState {
  artifact?: Schemas["Artifact"];
  path: string;
  overwrite: "replace" | "create_only";
  owner: string;
  group: string;
  mode: string;
  elevated: boolean;
  validate: string;
  serviceAction: "" | "reload" | "restart";
  serviceName: string;
}

export const emptyFileDeploy: FileDeployState = {
  path: "", overwrite: "replace", owner: "", group: "", mode: "", elevated: false, validate: "", serviceAction: "", serviceName: "",
};

export function fileDeployStep(v: FileDeployState, timeout_s?: number, when?: Schemas["When"]): Schemas["FileDeployOp"] {
  return {
    type: "file_deploy", artifact: v.artifact?.sha256 ?? "", path: v.path.trim(), overwrite: v.overwrite,
    owner: v.owner.trim() || undefined, group: v.group.trim() || undefined, mode: v.mode.trim() || undefined,
    elevated: v.elevated, validate: v.validate.trim() || undefined,
    service: v.serviceAction && v.serviceName.trim() ? { name: v.serviceName.trim(), action: v.serviceAction } : undefined,
    timeout_s, when,
  };
}

export function FileDeployForm({ v, set }: { v: FileDeployState; set: (v: FileDeployState) => void }) {
  const { can } = useAuth();
  const [progress, setProgress] = useState<number>();
  const up = useMutation({
    mutationFn: (f: File) => upload("POST", `/artifacts?name=${encodeURIComponent(f.name)}`, f, setProgress) as Promise<Schemas["Artifact"]>,
    onSuccess: (a) => {
      setProgress(undefined);
      set({ ...v, artifact: a, path: v.path || "" });
    },
    onError: () => setProgress(undefined),
  });
  const m = v.mode ? parseOctal(v.mode) : null;
  return (
    <div className="space-y-4">
      <Field label="File to deploy" hint="Uploaded to the controller once; every host gets exactly these bytes (checked by SHA-256).">
        <input type="file" className="text-sm" onChange={(e) => e.target.files?.[0] && up.mutate(e.target.files[0])} />
      </Field>
      {progress != null && <p className="text-sm text-muted">Uploading… {Math.round(progress * 100)}%</p>}
      {up.error && <Alert>{errorMessage(up.error)}</Alert>}
      {v.artifact && (
        <p className="text-sm">
          <span className="font-medium">{v.artifact.name || "file"}</span>{" "}
          <span className="text-muted">{formatBytes(v.artifact.size)} · sha256 <span className="font-mono">{v.artifact.sha256.slice(0, 16)}…</span>
            {v.artifact.is_text ? " · text" : " · binary"}</span>
        </p>
      )}
      <Field label="Destination path on the hosts" hint="Absolute path of the file, e.g. /etc/nginx/conf.d/site.conf. Symlinks are never written through.">
        <Input className="font-mono" value={v.path} required placeholder="/etc/myapp/config.yaml" onChange={(e) => set({ ...v, path: e.target.value })} />
      </Field>
      <Field label="If the file already exists">
        <Select value={v.overwrite} onChange={(e) => set({ ...v, overwrite: e.target.value as FileDeployState["overwrite"] })}>
          <option value="replace">Replace it (the previous version is kept for rollback)</option>
          <option value="create_only">Leave it alone (only create missing files)</option>
        </Select>
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Owner" hint="Empty: keep the current owner (new files: root, or the login user).">
          <Input value={v.owner} placeholder="keep" onChange={(e) => set({ ...v, owner: e.target.value })} />
        </Field>
        <Field label="Group" hint="Empty: keep the current group.">
          <Input value={v.group} placeholder="keep" onChange={(e) => set({ ...v, group: e.target.value })} />
        </Field>
        <Field label="Permissions" hint={v.mode ? (m == null ? "Use octal, like 644 or 0640." : describe(m)) : "Empty: keep the current permissions (new files: 0644)."}>
          <Input className="font-mono" value={v.mode} placeholder="keep" onChange={(e) => set({ ...v, mode: e.target.value })} />
        </Field>
      </div>
      {can("files.elevate") && (
        <Checkbox checked={v.elevated} onChange={(elevated) => set({ ...v, elevated })} label="Use administrator privileges"
          hint="Write the file as root through each host's sudo setting (needed for most system files)." />
      )}
      <Field label="Validation command (optional)" hint="Runs on each host against the new file before anything is replaced; {path} is the staged file. A failure leaves the live file untouched. Examples: sshd -t -f {path} · visudo -cf {path} · python3 -m json.tool {path}">
        <Input className="font-mono" value={v.validate} placeholder="visudo -cf {path}" onChange={(e) => set({ ...v, validate: e.target.value })} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-[12rem_1fr]">
        <Field label="Then">
          <Select value={v.serviceAction} onChange={(e) => set({ ...v, serviceAction: e.target.value as FileDeployState["serviceAction"] })}>
            <option value="">Do nothing else</option>
            <option value="reload">Reload a service</option>
            <option value="restart">Restart a service</option>
          </Select>
        </Field>
        {v.serviceAction && (
          <Field label="Service" hint="Only when the file actually changed. Its result is reported separately from the file's.">
            <Input className="font-mono" value={v.serviceName} placeholder="nginx" onChange={(e) => set({ ...v, serviceName: e.target.value })} />
          </Field>
        )}
      </div>
    </div>
  );
}

const stateLabel: Record<string, { text: string; tone: "ok" | "warn" | "accent" | "neutral" | "danger" }> = {
  create: { text: "missing: would create", tone: "accent" },
  replace: { text: "different: would replace", tone: "warn" },
  identical: { text: "identical (nothing to do)", tone: "ok" },
  metadata: { text: "only owner/permissions change", tone: "accent" },
  keep: { text: "exists; left alone", tone: "neutral" },
  remove: { text: "would remove", tone: "danger" },
};

/** What a deployment preview found, as far as the run's review needs it. */
export interface DeploySummary {
  hosts: number;
  /** Hosts per outcome ("would create", "identical (nothing to do)"…). */
  outcomes: [string, number][];
  skipped: string[];
  failed: string[];
}

/** Per-host dry run with diffs. */
export function DeployPreviewPanel({ targets, step, hasTargets, onResult }: {
  targets: Schemas["TargetSpec"];
  step: Schemas["FileDeployOp"];
  hasTargets: boolean;
  /** The preview's outcome, for the run's review (null: none for this step). */
  onResult?: (r: DeploySummary | null) => void;
}) {
  const prev = useMutation({
    mutationFn: () => unwrap(api.POST("/file-deployments/preview", { body: { targets, step } })),
  });
  const key = useMemo(() => JSON.stringify([targets, step]), [targets, step]);
  const { reset } = prev;
  useEffect(() => reset(), [key, reset]);
  useEffect(() => {
    if (!onResult) return;
    const hs = prev.data?.hosts;
    if (!hs) return onResult(null);
    const counts = new Map<string, number>();
    for (const h of hs) {
      const st = h.preview && stateLabel[h.preview.state];
      if (st) counts.set(st.text, (counts.get(st.text) ?? 0) + 1);
    }
    onResult({ hosts: hs.length, outcomes: [...counts], skipped: hs.filter((h) => h.skipped).map((h) => h.host_name),
      failed: hs.filter((h) => h.error).map((h) => h.host_name) });
  }, [prev.data, onResult]);
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <Button type="button" size="sm" onClick={() => prev.mutate()} disabled={!hasTargets || !step.artifact || !step.path || prev.isPending}>
          {prev.isPending ? "Checking hosts…" : prev.data ? "Preview again" : "Preview changes"}
        </Button>
        <span className="text-xs text-muted">Reads the current file on each host. Nothing is changed.</span>
      </div>
      {prev.error && <Alert>{errorMessage(prev.error)}</Alert>}
      {prev.data && (
        <ul className="divide-y divide-line rounded-ui border border-line">
          {prev.data.hosts.map((h) => {
            const st = h.preview && stateLabel[h.preview.state];
            return (
              <li key={h.host_id} className="space-y-2 p-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{h.host_name}</span>
                  {st && <Badge tone={st.tone}>{st.text}</Badge>}
                  {h.skipped && <span className="text-muted">skipped: {h.skipped}</span>}
                  {h.error && <span className="text-danger">inaccessible: {h.error.message}</span>}
                </div>
                {h.error?.hint && <p className="text-xs text-muted">{h.error.hint}</p>}
                {h.preview?.current && (
                  <p className="text-xs text-muted">
                    Now: {h.preview.current.owner}:{h.preview.current.group} {h.preview.current.mode} · {formatBytes(h.preview.current.size)}
                    {" → "}after: {h.preview.desired.owner || "(default)"}:{h.preview.desired.group || "(default)"} {h.preview.desired.mode}
                  </p>
                )}
                {(h.preview?.state === "replace" || h.preview?.state === "create") && (
                  <details open={prev.data!.hosts.length <= 3}>
                    <summary className="cursor-pointer text-xs text-muted">Show changes</summary>
                    <div className="mt-1"><DiffView diff={h.preview.diff} note={h.preview.diff_note} /></div>
                  </details>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
