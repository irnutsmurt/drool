import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useGroups, useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { RolloutFields, defaultRollout, recommendedRollout, rolloutFrom, strategyFrom, type RolloutState } from "@/components/automation/Rollout";
import { useBaseline, useInvalidateDesired, resourceKey, type BaselineInput, type DesiredImpact, type DesiredResource } from "@/components/desired/queries";
import { DesiredStatusBadge } from "@/components/desired/ui";
import { useDebounced } from "@/hooks/useDebounced";
import { upload } from "@/lib/upload";
import { errorMessage, formatBytes } from "@/lib/format";

/** A resource row as edited (strings as typed). */
interface Row {
  id: number;
  kind: "service" | "package" | "file";
  name: string;
  state: string; // service: "" | running | stopped; package: installed | absent
  enabled: "" | "yes" | "no";
  path: string;
  artifact: string;
  artifactName: string;
  artifactSize?: number;
  owner: string;
  group: string;
  mode: string;
  elevated: boolean;
  validate: string;
  svcName: string;
  svcAction: "" | "reload" | "restart";
}

let nextId = 1;
const blank = (kind: Row["kind"]): Row => ({
  id: nextId++, kind, name: "", state: kind === "package" ? "installed" : kind === "service" ? "running" : "", enabled: kind === "service" ? "yes" : "",
  path: "", artifact: "", artifactName: "", owner: "", group: "", mode: kind === "file" ? "0644" : "", elevated: kind === "file", validate: "", svcName: "", svcAction: "",
});

function rowFrom(r: DesiredResource): Row {
  return {
    ...blank(r.kind), kind: r.kind, name: r.name ?? "", state: r.state ?? "", enabled: r.enabled == null ? "" : r.enabled ? "yes" : "no",
    path: r.path ?? "", artifact: r.artifact ?? "", artifactName: r.artifact_name ?? "", owner: r.owner ?? "", group: r.group ?? "", mode: r.mode ?? "",
    elevated: !!r.elevated, validate: r.validate ?? "", svcName: r.service?.name ?? "", svcAction: r.service?.action ?? "",
  };
}

function toResource(r: Row): DesiredResource {
  if (r.kind === "service") {
    return { kind: "service", name: r.name.trim(), state: (r.state || undefined) as DesiredResource["state"], enabled: r.enabled === "" ? undefined : r.enabled === "yes" };
  }
  if (r.kind === "package") return { kind: "package", name: r.name.trim(), state: r.state as DesiredResource["state"] };
  return {
    kind: "file", path: r.path.trim(), artifact: r.artifact, artifact_name: r.artifactName || undefined, owner: r.owner.trim() || undefined,
    group: r.group.trim() || undefined, mode: r.mode.trim() || undefined, elevated: r.elevated || undefined, validate: r.validate.trim() || undefined,
    service: r.svcName.trim() && r.svcAction ? { name: r.svcName.trim(), action: r.svcAction } : undefined,
  };
}

/** What's wrong with a row before the server is asked ("" when fine). */
function rowProblem(r: Row): string {
  if (r.kind === "service") {
    if (!r.name.trim()) return "Name the service.";
    if (!r.state && !r.enabled) return "Choose whether it should run, start at boot, or both.";
  }
  if (r.kind === "package" && !r.name.trim()) return "Name the package.";
  if (r.kind === "file") {
    if (!r.path.trim().startsWith("/")) return "Give the file's absolute path.";
    if (!r.artifact) return "Upload the file's content.";
  }
  return "";
}

/** Create or change a baseline. The impact is shown before anything is saved. */
export function BaselineEditorPage() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const focus = params.get("focus") ?? "";
  const navigate = useNavigate();
  const { can } = useAuth();
  const existing = useBaseline(id);
  const groups = useGroups();
  const hosts = useHosts();
  const invalidate = useInvalidateDesired();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [everywhere, setEverywhere] = useState(true);
  const [pickedGroups, setPickedGroups] = useState<string[]>([]);
  const [pickedHosts, setPickedHosts] = useState<string[]>([]);
  const [rows, setRows] = useState<Row[]>([]);
  const [advanced, setAdvanced] = useState(false);
  const [auto, setAuto] = useState(false);
  const [rollout, setRollout] = useState<RolloutState>(recommendedRollout);
  const loaded = useRef(false);

  useEffect(() => {
    const b = existing.data;
    if (!b || loaded.current) return;
    loaded.current = true;
    setName(b.name);
    setDescription(b.description);
    setEverywhere(b.assignments.some((a) => a.scope === "global") || b.assignments.length === 0);
    setPickedGroups(b.assignments.filter((a) => a.scope === "group").map((a) => a.group_id!));
    setPickedHosts(b.assignments.filter((a) => a.scope === "host").map((a) => a.host_id!));
    setRows(b.resources.map(rowFrom));
    setAuto(b.auto.enabled);
    setRollout(b.auto.strategy ? rolloutFrom(b.auto.strategy) : recommendedRollout);
    if (focus) setTimeout(() => document.getElementById(`res-${focus}`)?.scrollIntoView({ block: "center" }), 100);
  }, [existing.data, focus]);
  useEffect(() => {
    if (!id && rows.length === 0) setRows([blank("service")]);
  }, [id, rows.length]);

  const input: BaselineInput = useMemo(() => ({
    name: name.trim(),
    description: description.trim(),
    resources: rows.map(toResource),
    assignments: everywhere ? [{ scope: "global" }] : [
      ...pickedGroups.map((g) => ({ scope: "group" as const, group_id: g })),
      ...pickedHosts.map((h) => ({ scope: "host" as const, host_id: h })),
    ],
  }), [name, description, rows, everywhere, pickedGroups, pickedHosts]);
  const problems = rows.map(rowProblem);
  // A baseline may be saved unassigned (the impact preview says it then
  // applies to no host).
  const valid = input.name !== "" && problems.every((p) => !p);
  const debounced = useDebounced(input, 500);
  const preview = useQuery({
    queryKey: ["desired", "baseline-preview", id ?? "", debounced],
    queryFn: () => unwrap(api.POST("/desired/baselines/preview", { body: { ...debounced, id } })),
    enabled: valid && debounced.name !== "",
    retry: false,
    staleTime: Infinity,
  });
  const upToDate = debounced === input && !preview.isFetching && !!preview.data;

  const save = useMutation({
    mutationFn: async () => {
      const b = id
        ? await unwrap(api.PUT("/desired/baselines/{id}", { params: { path: { id } }, body: input }))
        : await unwrap(api.POST("/desired/baselines", { body: input }));
      const before = existing.data?.auto;
      const strat = strategyFrom(rollout);
      if (can("desired.auto") && (auto !== !!before?.enabled || (auto && JSON.stringify(strat) !== JSON.stringify(before?.strategy ?? null)))) {
        await unwrap(api.PUT("/desired/baselines/{id}/auto", { params: { path: { id: b.id } }, body: { enabled: auto, strategy: auto ? strat : undefined } }));
      }
      return b;
    },
    onSuccess: (b) => {
      invalidate();
      navigate(`/desired/baselines/${b.id}`);
    },
  });

  const set = (rid: number, patch: Partial<Row>) => setRows(rows.map((r) => (r.id === rid ? { ...r, ...patch } : r)));
  if (id && existing.error) return <Alert>{errorMessage(existing.error)}</Alert>;
  if (id && !existing.data) return <p className="text-muted">Loading…</p>;
  const nextRev = existing.data ? existing.data.revision + 1 : 1;
  return (
    <>
      <PageHeader crumbs={[{ label: "Desired State", to: "/desired" }, { label: "Baselines", to: "/desired/baselines" }, ...(id ? [{ label: existing.data?.name ?? "Baseline", to: `/desired/baselines/${id}` }] : [])]}
        title={id ? `Change baseline: ${existing.data?.name}` : "Create baseline"}
        description={id ? `Saving creates revision ${nextRev}; earlier revisions are kept unchanged.` : "Say what machines should look like. DROOL checks it continuously and tells you when they drift."} />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (upToDate) save.mutate(); }}>
          <Card title="Baseline">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} placeholder="Media Server" required /></Field>
              <Field label="Description (optional)"><Input value={description} onChange={(e) => setDescription(e.target.value)} /></Field>
            </div>
          </Card>

          <Card title="Applies to">
            <fieldset className="space-y-2">
              <legend className="sr-only">Applies to</legend>
              <label className="flex items-center gap-2 text-sm"><input type="radio" name="scope" checked={everywhere} onChange={() => setEverywhere(true)} /> Every host</label>
              <label className="flex items-center gap-2 text-sm"><input type="radio" name="scope" checked={!everywhere} onChange={() => setEverywhere(false)} /> Packs and hosts I choose</label>
            </fieldset>
            {!everywhere && (
              <div className="mt-3 grid gap-4 sm:grid-cols-2">
                <div>
                  <h3 className="mb-1 text-sm font-medium">Packs</h3>
                  <div className="max-h-48 space-y-1 overflow-y-auto rounded-ui border border-line p-2">
                    {(groups.data?.items ?? []).length === 0 && <p className="text-xs text-muted">No Packs yet.</p>}
                    {(groups.data?.items ?? []).map((g) => (
                      <Checkbox key={g.id} checked={pickedGroups.includes(g.id)} label={g.name}
                        onChange={(on) => setPickedGroups(on ? [...pickedGroups, g.id] : pickedGroups.filter((x) => x !== g.id))} />
                    ))}
                  </div>
                </div>
                <div>
                  <h3 className="mb-1 text-sm font-medium">Hosts</h3>
                  <div className="max-h-48 space-y-1 overflow-y-auto rounded-ui border border-line p-2">
                    {(hosts.data?.items ?? []).filter((h) => !h.jump_only).map((h) => (
                      <Checkbox key={h.id} checked={pickedHosts.includes(h.id)} label={h.name}
                        onChange={(on) => setPickedHosts(on ? [...pickedHosts, h.id] : pickedHosts.filter((x) => x !== h.id))} />
                    ))}
                  </div>
                </div>
              </div>
            )}
            <p className="mt-2 text-xs text-muted">
              When baselines disagree about a resource, the most specific wins: a host's own setting, then baselines assigned to the host,
              then its Packs (the higher <Link className={plainLink} to="/groups">Pack priority</Link> wins), then baselines for every host.
              Equal priorities that disagree are shown as a conflict — DROOL never picks one silently.
            </p>
          </Card>

          <Card title={`Resources (${rows.length})`}>
            <ul className="space-y-3" aria-label="Resources">
              {rows.map((r, i) => (
                <li key={r.id} id={`res-${resourceKey(toResource(r))}`}
                  className={cx("rounded-ui border p-3", focus && resourceKey(toResource(r)) === focus ? "border-accent" : "border-line")}>
                  <ResourceFields r={r} set={(p) => set(r.id, p)} />
                  <div className="mt-2 flex items-center justify-between gap-2">
                    <span className="text-xs text-danger">{problems[i]}</span>
                    <Button size="sm" type="button" variant="ghost" onClick={() => setRows(rows.filter((x) => x.id !== r.id))}>Remove</Button>
                  </div>
                </li>
              ))}
            </ul>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button type="button" size="sm" onClick={() => setRows([...rows, blank("service")])}>Add service</Button>
              <Button type="button" size="sm" onClick={() => setRows([...rows, blank("package")])}>Add package</Button>
              <Button type="button" size="sm" onClick={() => setRows([...rows, blank("file")])}>Add file</Button>
            </div>
            <p className="mt-2 text-xs text-muted">Only these typed resources: no scripts, no expressions. Firewall policy is managed on the Firewall page.</p>
          </Card>

          <Card>
            <button type="button" className="flex w-full items-center justify-between text-left text-sm font-semibold" aria-expanded={advanced} onClick={() => setAdvanced(!advanced)}>
              <span>Advanced: automatic restore{auto ? " (on)" : " (off)"}</span>
              <span className="text-muted">{advanced ? "Hide" : "Show"}</span>
            </button>
            {advanced && (
              <div className="mt-3 space-y-3">
                <Checkbox checked={auto} disabled={!can("desired.auto")} onChange={setAuto}
                  label="Automatically restore managed state"
                  hint={can("desired.auto")
                    ? "Off by default. When on, DROOL corrects drift from this baseline by itself — never during maintenance, with bounded retries, pausing when something keeps changing it back — as an ordinary job run with your permissions (you become its owner)."
                    : "Only administrators can turn automatic restore on or off."} />
                {existing.data?.auto.owner && <p className="text-xs text-muted">Owner: {existing.data.auto.owner.name}. Their permissions are checked before every automatic job.</p>}
                {auto && can("desired.auto") && (
                  <div className="rounded-ui border border-line p-3">
                    <p className="mb-2 text-sm">Rollout of automatic corrections (for several hosts: one canary first, then batches, pausing after failures):</p>
                    <RolloutFields v={rollout} set={setRollout} hosts={[]} noExplicit />
                    <Button className="mt-2" type="button" size="sm" onClick={() => setRollout(defaultRollout)}>All hosts at once</Button>
                  </div>
                )}
                <p className="text-xs text-muted">Pack priorities are set on the <Link className={plainLink} to="/groups">Packs</Link> page.</p>
              </div>
            )}
          </Card>

          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          <div className="flex flex-wrap items-center justify-end gap-2">
            {!upToDate && valid && <span className="mr-auto text-xs text-muted" role="status">Updating the impact preview…</span>}
            <Link to={id ? `/desired/baselines/${id}` : "/desired/baselines"}><Button type="button">Cancel</Button></Link>
            <Button type="submit" variant="primary" disabled={!valid || !upToDate || save.isPending || (id != null && preview.data?.changed === false && auto === existing.data?.auto.enabled)}>
              {save.isPending ? "Saving…" : id ? `Save revision ${nextRev}` : "Create baseline"}
            </Button>
          </div>
        </form>
        <aside className="lg:sticky lg:top-4 lg:self-start">
          <ImpactPanel impact={preview.data} error={preview.error} pending={valid && (!upToDate || preview.isPending)} valid={valid} />
        </aside>
      </div>
    </>
  );
}

function ResourceFields({ r, set }: { r: Row; set: (p: Partial<Row>) => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [more, setMore] = useState(!!(r.validate || r.svcName));
  if (r.kind === "service") {
    return (
      <div className="grid gap-3 sm:grid-cols-[1fr_10rem_10rem]">
        <Field label="Service"><Input value={r.name} onChange={(e) => set({ name: e.target.value })} placeholder="jellyfin.service" /></Field>
        <Field label="Running">
          <Select value={r.state} onChange={(e) => set({ state: e.target.value })}>
            <option value="running">Yes, running</option>
            <option value="stopped">No, stopped</option>
            <option value="">Leave alone</option>
          </Select>
        </Field>
        <Field label="Enabled at boot">
          <Select value={r.enabled} onChange={(e) => set({ enabled: e.target.value as Row["enabled"] })}>
            <option value="yes">Yes</option>
            <option value="no">No</option>
            <option value="">Leave alone</option>
          </Select>
        </Field>
      </div>
    );
  }
  if (r.kind === "package") {
    return (
      <div className="grid gap-3 sm:grid-cols-[1fr_14rem]">
        <Field label="Package"><Input value={r.name} onChange={(e) => set({ name: e.target.value })} placeholder="curl" /></Field>
        <Field label="Should be">
          <Select value={r.state} onChange={(e) => set({ state: e.target.value })}>
            <option value="installed">Installed</option>
            <option value="absent">Not installed (prohibited)</option>
          </Select>
        </Field>
      </div>
    );
  }
  const pick = async (f: File | undefined) => {
    if (!f) return;
    setBusy(true);
    setErr("");
    try {
      const a = (await upload("POST", `/artifacts?name=${encodeURIComponent(f.name)}`, f)) as { sha256: string; size: number; name: string };
      set({ artifact: a.sha256, artifactName: a.name || f.name, artifactSize: a.size, path: r.path || "" });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-[1fr_16rem]">
        <Field label="File path on the host"><Input value={r.path} onChange={(e) => set({ path: e.target.value })} placeholder="/etc/caddy/Caddyfile" /></Field>
        <Field label="Content" hint={r.artifact ? `${r.artifactName || "uploaded"} · sha256 ${r.artifact.slice(0, 12)}…${r.artifactSize ? ` · ${formatBytes(r.artifactSize)}` : ""}` : "Upload the exact content the file should have."}>
          <input type="file" aria-label="Upload content" className="text-sm" disabled={busy} onChange={(e) => void pick(e.target.files?.[0])} />
        </Field>
      </div>
      {err && <p className="text-xs text-danger">{err}</p>}
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Owner (optional)" hint="Empty: not managed."><Input value={r.owner} onChange={(e) => set({ owner: e.target.value })} placeholder="root" /></Field>
        <Field label="Group (optional)"><Input value={r.group} onChange={(e) => set({ group: e.target.value })} placeholder="root" /></Field>
        <Field label="Permissions (optional)"><Input value={r.mode} onChange={(e) => set({ mode: e.target.value })} placeholder="0644" /></Field>
      </div>
      <Checkbox checked={r.elevated} onChange={(v) => set({ elevated: v })} label="Read and write it as root (sudo) on SSH hosts" hint="Needed for most files under /etc." />
      <button type="button" className="text-xs text-muted underline" aria-expanded={more} onClick={() => setMore(!more)}>{more ? "Hide" : "Validation and service action"}</button>
      {more && (
        <div className="grid gap-3 sm:grid-cols-[1fr_12rem_9rem]">
          <Field label="Validation command (optional)" hint="Runs on the staged file before it replaces the live one; {path} is the staged file. A failure leaves the live file untouched.">
            <Input value={r.validate} onChange={(e) => set({ validate: e.target.value })} placeholder="caddy validate --config {path}" />
          </Field>
          <Field label="Then reload or restart"><Input value={r.svcName} onChange={(e) => set({ svcName: e.target.value })} placeholder="caddy.service" /></Field>
          <Field label="Action">
            <Select value={r.svcAction} onChange={(e) => set({ svcAction: e.target.value as Row["svcAction"] })}>
              <option value="">None</option>
              <option value="reload">Reload</option>
              <option value="restart">Restart</option>
            </Select>
          </Field>
        </div>
      )}
    </div>
  );
}

function hostList(items: DesiredImpact["become_drifted"], n: number) {
  return (
    <ul className="mt-1 space-y-1">
      {items.slice(0, 8).map((h) => (
        <li key={h.host_id}>
          <span className="font-medium">{h.host_name}</span>{" "}
          <span className="text-xs"><DesiredStatusBadge status={h.now} /> → <DesiredStatusBadge status={h.after} /></span>
          {h.detail.length > 0 && <div className="text-xs text-muted">{h.detail.slice(0, 3).join("; ")}{h.detail.length > 3 ? "; …" : ""}</div>}
        </li>
      ))}
      {n > 8 && <li className="text-xs text-muted">… and {n - 8} more</li>}
    </ul>
  );
}

/** What saving would do, before anything is saved. */
function ImpactPanel({ impact, error, pending, valid }: { impact?: DesiredImpact; error: unknown; pending: boolean; valid: boolean }) {
  return (
    <Card title="Impact before saving">
      {!valid && <p className="text-sm text-muted">Complete the baseline to see what saving it would do.</p>}
      {valid && pending && <p className="text-sm text-muted" role="status">Updating…</p>}
      {valid && error ? <Alert>{errorMessage(error)}</Alert> : null}
      {valid && impact && (
        <div className={cx("space-y-3 text-sm", pending && "opacity-60")} aria-label="Impact">
          {!impact.changed && <p className="text-muted">No changes to save.</p>}
          <p><strong>{impact.hosts_affected}</strong> host{impact.hosts_affected === 1 ? "" : "s"} affected.</p>
          {(impact.added.length > 0 || impact.removed.length > 0 || impact.modified.length > 0) && (
            <ul className="space-y-0.5">
              {impact.added.map((c) => <li key={c.key}><span className="text-ok">+</span> {c.name}: {c.to}</li>)}
              {impact.modified.map((c) => <li key={c.key}><span className="text-warn">~</span> {c.name}: {c.from} → {c.to}</li>)}
              {impact.removed.map((c) => <li key={c.key}><span className="text-danger">−</span> {c.name} (no longer managed)</li>)}
            </ul>
          )}
          {impact.counts.become_drifted > 0 && <div><strong className="text-warn">{impact.counts.become_drifted} host{impact.counts.become_drifted === 1 ? "" : "s"} would drift</strong>{hostList(impact.become_drifted, impact.counts.become_drifted)}</div>}
          {impact.counts.become_compliant > 0 && <div><strong className="text-ok">{impact.counts.become_compliant} drifted host{impact.counts.become_compliant === 1 ? "" : "s"} would comply</strong> (resolved by configuration){hostList(impact.become_compliant, impact.counts.become_compliant)}</div>}
          {impact.counts.new_conflicts > 0 && <div><strong className="text-danger">{impact.counts.new_conflicts} new conflict{impact.counts.new_conflicts === 1 ? "" : "s"}</strong>{hostList(impact.new_conflicts, impact.counts.new_conflicts)}</div>}
          {impact.counts.resolved_conflicts > 0 && <div><strong>{impact.counts.resolved_conflicts} conflict{impact.counts.resolved_conflicts === 1 ? "" : "s"} resolved</strong>{hostList(impact.resolved_conflicts, impact.counts.resolved_conflicts)}</div>}
          {impact.counts.to_check > 0 && <p className="text-muted">{impact.counts.to_check} host{impact.counts.to_check === 1 ? "" : "s"} will be checked after saving (DROOL hasn't observed some of these resources yet).</p>}
          {impact.warnings.map((w, i) => <Alert key={i} tone="warn">{w}</Alert>)}
          <p className="text-xs text-muted">Saving changes the desired state only; nothing on the hosts changes until you (or automatic restore) restore managed state.</p>
        </div>
      )}
    </Card>
  );
}
