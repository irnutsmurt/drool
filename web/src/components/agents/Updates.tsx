import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { ak, useReleaseKeys, useReleases, useRollout, useUpdateStatus } from "@/components/agents/queries";
import { StatusBadge, type Status } from "@/components/monitoring/Status";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { Field, Input, Select } from "@/components/ui/Field";
import { GuideLink } from "@/components/ui/GuideLink";
import { plainLink } from "@/components/ui/links";
import { ReadOnlyNote } from "@/components/ui/ReadOnlyNote";
import { LoadError, Loading } from "@/components/ui/States";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatAgo, formatBytes, formatDateTime } from "@/lib/format";
import { upload } from "@/lib/upload";

type UpdateStatus = Schemas["AgentUpdateStatus"];
type Policy = Schemas["AgentUpdatePolicy"];
type RolloutSummary = Schemas["AgentRolloutSummary"];

/** Shepherd updates (ARCHITECTURE-ALPHA §42): how updates stand, the two
 * settings a person normally needs (automatic, channel), what needs
 * attention, and the history. Rollout tuning, an exact version and custom
 * releases with their keys are under Advanced settings: nobody has to touch
 * them to keep Shepherds current. */
export function UpdatesTab() {
  const { can } = useAuth();
  const manage = can("agents.manage");
  const [params, setParams] = useSearchParams();
  const advanced = params.get("advanced") === "1";
  const setAdvanced = (v: boolean) => setParams((p) => { if (v) p.set("advanced", "1"); else p.delete("advanced"); return p; }, { replace: true });
  const status = useUpdateStatus();
  if (status.error) return <LoadError what="the update status" error={status.error} />;
  if (!status.data) return <Loading what="the update status" />;
  const st = status.data;
  return (
    <div className="space-y-4" data-testid="shepherd-updates" data-state={st.state}>
      {!manage && (
        <ReadOnlyNote>you can see how Shepherd updates stand; changing the settings and starting or stopping updates needs the agents.manage
          permission (administrators).</ReadOnlyNote>
      )}
      <Overview st={st} manage={manage} />
      {(st.problems.length > 0 || st.blocked.length > 0) && <Attention st={st} manage={manage} />}
      {st.active.length > 0 && <Active st={st} manage={manage} />}
      <History st={st} />
      <div>
        <Button variant="ghost" aria-expanded={advanced} aria-controls="updates-advanced" onClick={() => setAdvanced(!advanced)}>
          {advanced ? "▾" : "▸"} Advanced settings: rollout, exact version, custom releases
        </Button>
      </div>
      {advanced && (
        <div id="updates-advanced" className="space-y-4">
          <RolloutSettings st={st} manage={manage} />
          <CustomReleases st={st} manage={manage} />
          <OfficialSource st={st} />
        </div>
      )}
    </div>
  );
}

const stateLook: Record<UpdateStatus["state"], { status: Status; word: string }> = {
  current: { status: "ok", word: "Up to date" },
  updating: { status: "info", word: "Updating" },
  available: { status: "info", word: "Update available" },
  attention: { status: "critical", word: "Needs attention" },
  blocked: { status: "warning", word: "Can't update" },
  none: { status: "unknown", word: "No Shepherds yet" },
};

const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;

/** Official updates are set up here (perhaps waiting for a publication or
 * the network), so "automatically" is a true promise. */
const officialWorks = (st: UpdateStatus) => ["verified", "pending", "not_published", "unreachable"].includes(st.official.state);

function useSavePolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (p: Policy) => unwrap(api.PUT("/agents/updates/policy", { body: p })),
    // Pending until the page has the saved policy back, so a control can
    // show the saved choice the whole time.
    onSuccess: () => Promise.all([
      qc.invalidateQueries({ queryKey: ak.updateStatus }),
      qc.invalidateQueries({ queryKey: ak.policy }),
    ]),
  });
}

function useStartRollout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { version: string; host_ids?: string[]; retry?: boolean }) => unwrap(api.POST("/agents/rollouts", { body: b })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ak.updateStatus });
      void qc.invalidateQueries({ queryKey: ak.rollouts });
    },
  });
}

/** Why official releases can't work here, when they can't, in words a
 * person can act on (never "updates are working" when they aren't). */
function officialNotice(st: UpdateStatus): { tone: "warn" | "danger" | null; text: string } {
  const o = st.official;
  switch (o.state) {
    case "verified":
    case "pending":
      return { tone: null, text: "" };
    case "not_published":
      return { tone: "warn", text: `${o.detail} Shepherds keep their version until then.` };
    case "unreachable":
      return { tone: "warn", text: `DROOL couldn't reach its release source: ${st.check.last_error || o.detail}. Shepherds keep their version; DROOL tries again.` };
    case "rejected":
      return { tone: "danger", text: "This DROOL's official Shepherd release failed verification and wasn't used (see Needs attention)." };
    case "development":
      return { tone: "warn", text: `${o.detail} Automatic updates can install only custom releases (Advanced settings); use an official DROOL release for automatic official updates.` };
    case "no_key":
      return { tone: "warn", text: `${o.detail} Automatic updates can install only custom releases (Advanced settings).` };
    case "off":
      return { tone: "warn", text: `${o.detail} Automatic updates install only releases uploaded under Advanced settings.` };
  }
  return { tone: "warn", text: o.detail };
}

function Overview({ st, manage }: { st: UpdateStatus; manage: boolean }) {
  const qc = useQueryClient();
  const save = useSavePolicy();
  const start = useStartRollout();
  const check = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/updates/check")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ak.updateStatus }),
  });
  // The two settings save at once and stay enabled while they do: a control
  // disabled under the keyboard focus loses it to the top of the document.
  // Until the saved policy is back, they show the choice being saved.
  const p = save.isPending && save.variables ? save.variables : st.policy;
  const look = stateLook[st.state];
  const notice = officialNotice(st);
  const automatic = p.policy === "automatic";
  const channels = st.channels.includes(p.channel) ? st.channels : [...st.channels, p.channel];
  const channelLabel = (c: string) => (c === "stable" ? "Stable (recommended)" : c === "prerelease" ? "Release candidates" : `${c} (custom releases)`);
  const c = st.counts;
  const failedSince = st.check.last_error && st.check.last_error_at && (!st.check.last_success || st.check.last_error_at > st.check.last_success);
  const updateNow = async () => {
    const n = c.behind;
    const ok = await confirmAction({
      title: `Update ${plural(n, "Shepherd")} to ${st.target}?`, confirmLabel: `Update ${plural(n, "Shepherd")}`,
      body: <p>One Shepherd updates first; the others follow in batches of {p.batch_size} once it&apos;s healthy. A Shepherd that isn&apos;t healthy
        after its update goes back to its previous version by itself, and the update pauses.</p>,
    });
    if (ok) start.mutate({ version: st.target });
  };
  return (
    <Card title="Shepherd updates">
      <div className="space-y-4" data-testid="updates-overview">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1" role="status">
          <StatusBadge status={look.status}>{look.word}</StatusBadge>
          <p className="text-base font-medium" data-testid="updates-summary">{st.summary}</p>
        </div>
        {st.latest && (
          <p className="rounded-ui border border-line bg-surface-2 px-3 py-2 text-sm" data-testid="updates-latest">
            <span className="font-medium">DROOL {st.latest.version} is available</span> (this is {st.controller.version}).
            {automatic && officialWorks(st) ? " Update DROOL, and its Shepherds update automatically." : " Update DROOL, then update its Shepherds here."}{" "}
            <GuideLink page="Updating-DROOL">How to update DROOL</GuideLink>
          </p>
        )}
        {notice.tone && <Alert tone={notice.tone}>{notice.text}</Alert>}
        <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[11rem_1fr]">
          <dt className="font-medium sm:pt-0.5">Automatic updates</dt>
          <dd>
            {manage ? (
              <Checkbox checked={automatic} onChange={(v) => save.mutate({ ...p, policy: v ? "automatic" : "manual" })}
                label="Update Shepherds automatically"
                hint={automatic
                  ? `On (recommended): one Shepherd updates first, then batches of ${p.batch_size}; updates pause if one fails.`
                  : "Off: Shepherds update only when someone starts an update."} />
            ) : (
              <span>{automatic ? "On" : "Off"}</span>
            )}
          </dd>
          <dt className="font-medium sm:pt-2">Update channel</dt>
          <dd>
            {manage ? (
              <>
                <Select className="max-w-xs" aria-label="Update channel" value={p.channel}
                  onChange={(e) => save.mutate({ ...p, channel: e.target.value })}>
                  {channels.map((ch) => <option key={ch} value={ch}>{channelLabel(ch)}</option>)}
                </Select>
                <p className="mt-1 text-xs text-muted">{p.channel === "prerelease" ? "Release candidates as well as releases: for testing DROOL before its releases." : "Releases only."}</p>
              </>
            ) : (
              <span>{channelLabel(p.channel)}</span>
            )}
          </dd>
          <dt className="font-medium">Last checked</dt>
          <dd className="space-y-1" data-testid="updates-last-checked">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              {!st.check.enabled ? <span className="text-muted">{notice.text ? "Not checked (the note above says why)." : "Not checked: checks are off."}</span>
                : st.check.checking ? <span>Checking…</span>
                  : st.check.last_success ? <span title={formatDateTime(st.check.last_success)}>{formatAgo(st.check.last_success)}</span>
                    : <span className="text-muted">Not checked yet: DROOL checks shortly after it starts.</span>}
              {manage && st.check.enabled && (
                // Enabled while a check runs, for the keyboard focus: presses
                // meanwhile ask for at most one more check.
                <Button size="sm" onClick={() => check.mutate()}>Check now</Button>
              )}
            </div>
            {st.official.state === "verified" && <p className="text-xs text-muted">{st.official.detail}</p>}
            {failedSince && <p className="text-xs text-warn">The last check failed {formatAgo(st.check.last_error_at)}: {st.check.last_error}</p>}
          </dd>
          <dt className="font-medium">Shepherds</dt>
          <dd data-testid="updates-counts">
            {c.total === 0 ? <span className="text-muted">None yet.{automatic && officialWorks(st) ? " Shepherds you install are kept up to date automatically." : ""}</span> : (
              <span>
                {plural(c.current, "Shepherd")} up to date
                {c.updating > 0 && <> · {c.updating} updating</>}
                {c.waiting > 0 && <> · {c.waiting} waiting to reconnect</>}
                {c.behind > 0 && <> · {c.behind} to update</>}
                {c.blocked > 0 && <> · {c.blocked} can&apos;t update</>}
                {c.offline - c.waiting > 0 && <> · {c.offline - c.waiting} offline</>}
                {" · "}<Link className={plainLink} to="/agents">View Shepherds</Link>
              </span>
            )}
          </dd>
        </dl>
        {manage && st.state === "available" && (!automatic || st.stopped) && st.target && (
          <Button variant="primary" onClick={() => void updateNow()} disabled={start.isPending}>
            Update {plural(c.behind, "Shepherd")} to {st.target}…</Button>
        )}
        {(save.error || start.error || check.error) && <Alert>{errorMessage(save.error ?? start.error ?? check.error)}</Alert>}
      </div>
    </Card>
  );
}

function Attention({ st, manage }: { st: UpdateStatus; manage: boolean }) {
  const qc = useQueryClient();
  const start = useStartRollout();
  const act = useMutation({
    mutationFn: ({ id, a }: { id: string; a: "resume" | "cancel" }) => unwrap(api.POST(`/agents/rollouts/{id}/${a}`, { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ak.updateStatus }),
  });
  const tryAgain = async (host: string, hostID: string, version: string) => {
    const ok = await confirmAction({
      title: `Try the update of ${host} to ${version} again?`, confirmLabel: "Try again",
      body: <p>The update failed on {host} before. Try again once its cause is fixed: if the new version isn&apos;t healthy, the Shepherd goes back to
        its current version by itself again.</p>,
    });
    if (ok) start.mutate({ version, host_ids: [hostID], retry: true });
  };
  const cancel = async (id: string, version: string) => {
    const ok = await confirmAction({
      title: `Cancel the update to ${version}?`, confirmLabel: "Cancel the update", cancelLabel: "Keep it paused", danger: true,
      body: <p>Shepherds that already updated keep {version}; the others stay on their version. DROOL won&apos;t start {version} again by itself.</p>,
    });
    if (ok) act.mutate({ id, a: "cancel" });
  };
  const problemHosts = new Set(st.problems.filter((p) => p.host_id).map((p) => p.host_id));
  const blocked = st.blocked.filter((b) => !problemHosts.has(b.host_id));
  return (
    <Card title="Needs attention">
      <div className="space-y-4" data-testid="updates-attention">
        {st.problems.length > 0 && (
          <ul className="space-y-3">
            {st.problems.map((p, i) => (
              <li key={i} className="rounded-ui border border-line p-3" data-kind={p.kind}>
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="font-medium">{p.title}</p>
                    {p.detail && <p className="text-sm text-muted">{p.detail}</p>}
                    {p.at && <p className="text-xs text-muted">{formatAgo(p.at)}</p>}
                  </div>
                  {manage && (
                    <span className="flex flex-wrap gap-2">
                      {p.kind === "paused" && p.rollout_id && <>
                        <Button size="sm" onClick={() => act.mutate({ id: p.rollout_id!, a: "resume" })} disabled={act.isPending}>Resume</Button>
                        <Button size="sm" variant="danger" onClick={() => void cancel(p.rollout_id!, p.version ?? "")} disabled={act.isPending}>Cancel…</Button>
                      </>}
                      {(p.kind === "failed" || p.kind === "rolled_back") && p.host_id && p.version && (
                        <Button size="sm" onClick={() => void tryAgain(p.host_name ?? p.host_id!, p.host_id!, p.version!)} disabled={start.isPending}>Try again…</Button>
                      )}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
        {blocked.length > 0 && (
          <div>
            <p className="mb-2 text-sm font-medium">Can&apos;t update</p>
            <Table
              caption="Shepherds that can't update"
              rows={blocked}
              rowKey={(b) => b.host_id}
              columns={[
                { header: "Host", cell: (b) => <Link className="font-medium text-accent" to={`/hosts/${b.host_id}/management`}>{b.host_name || b.host_id}</Link> },
                { header: "Runs", cell: (b) => b.version },
                { header: "Why", cell: (b) => <span className="text-sm">{b.reason}</span> },
                { header: "", cell: (b) => manage && b.retry_version
                  ? <Button size="sm" onClick={() => void tryAgain(b.host_name, b.host_id, b.retry_version!)}>Try again…</Button> : null },
              ]}
            />
          </div>
        )}
        {(act.error || start.error) && <Alert>{errorMessage(act.error ?? start.error)}</Alert>}
      </div>
    </Card>
  );
}

const rolloutWord = (r: RolloutSummary): { status: Status; word: string } => {
  if (r.status === "completed") return r.failed > 0 ? { status: "warning", word: "Completed with failures" } : { status: "ok", word: "Completed" };
  if (r.status === "running") return { status: "info", word: "Updating" };
  if (r.status === "paused") return { status: "critical", word: "Paused" };
  if (r.reason.startsWith("superseded")) return { status: "unknown", word: "Replaced by a newer update" };
  return { status: "unknown", word: "Cancelled" };
};

function counted(r: RolloutSummary) {
  const parts = [`${r.healthy} of ${r.hosts} updated`];
  if (r.in_flight > 0) parts.push(`${r.in_flight} in progress`);
  if (r.waiting > 0) parts.push(`${r.waiting} waiting`);
  if (r.skipped > 0) parts.push(`${r.skipped} skipped`);
  if (r.failed > 0) parts.push(`${r.failed} failed`);
  return parts.join(" · ");
}

function Active({ st, manage }: { st: UpdateStatus; manage: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState<string>();
  const pause = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/agents/rollouts/{id}/pause", { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ak.updateStatus }),
  });
  return (
    <Card title="Updating now">
      <ul className="space-y-3" data-testid="updates-active">
        {st.active.map((r) => (
          <li key={r.id} className="rounded-ui border border-line p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <p className="font-medium">Update to {r.version}{r.automatic ? " (automatic)" : ""}</p>
                <p className="text-sm text-muted">{counted(r)}{r.waiting > 0 && ": offline Shepherds update when they reconnect"}</p>
              </div>
              <span className="flex flex-wrap gap-2">
                <Button size="sm" variant="ghost" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? undefined : r.id)}>
                  {open === r.id ? "Hide hosts" : "Show hosts"}</Button>
                {manage && <Button size="sm" onClick={() => pause.mutate(r.id)} disabled={pause.isPending}>Pause</Button>}
              </span>
            </div>
            {open === r.id && <RolloutHosts id={r.id} />}
          </li>
        ))}
      </ul>
      {pause.error && <div className="mt-3"><Alert>{errorMessage(pause.error)}</Alert></div>}
    </Card>
  );
}

function History({ st }: { st: UpdateStatus }) {
  const [open, setOpen] = useState<string>();
  const automatic = st.policy.policy === "automatic";
  return (
    <Card title="Recent updates">
      {st.history.length === 0 ? (
        <p className="text-sm text-muted" data-testid="updates-history-empty">
          {st.active.length > 0 ? "None finished yet."
            : `No Shepherd updates yet.${!automatic ? " Updates start when someone starts them." : officialWorks(st) ? " When DROOL is updated, its Shepherds follow automatically." : ""}`}
        </p>
      ) : (
        <ul className="divide-y divide-line" data-testid="updates-history">
          {st.history.map((r) => {
            const w = rolloutWord(r);
            return (
              <li key={r.id} className="py-2 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <span className="font-medium">Update to {r.version}</span>
                  <StatusBadge status={w.status}>{w.word}</StatusBadge>
                  <span className="text-sm text-muted">{counted(r)} · {r.automatic ? "automatic" : r.retry ? "tried again" : "started by a person"} ·{" "}
                    <span title={formatDateTime(r.created_at)}>{formatAgo(r.created_at)}</span></span>
                  <Button size="sm" variant="ghost" className="ml-auto" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? undefined : r.id)}>
                    {open === r.id ? "Hide hosts" : "Show hosts"}</Button>
                </div>
                {open === r.id && <RolloutHosts id={r.id} />}
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

const hostStateTone = (s: string) => (s === "healthy" ? "ok" : s === "rolled_back" || s === "failed" ? "danger" : s === "skipped" || s === "pending" ? "neutral" : "warn");
const hostStateWord: Record<string, string> = {
  pending: "waiting", offered: "offered", staged: "downloaded", updating: "switching", healthy: "updated",
  rolled_back: "rolled back", failed: "failed", skipped: "skipped",
};

/** One rollout's hosts: wave, state and the Shepherd's own words. */
export function RolloutHosts({ id }: { id: string }) {
  const r = useRollout(id);
  if (r.error) return <LoadError what="the rollout" error={r.error} />;
  if (!r.data) return <Loading what="the rollout" />;
  const ro = r.data;
  return (
    <div className="mt-3">
      {ro.paused_reason && <p className="mb-2 text-sm text-muted">{ro.paused_reason}</p>}
      <Table
        caption={`Hosts in the update to ${ro.version}`}
        rows={ro.hosts ?? []}
        rowKey={(h) => h.host_id}
        columns={[
          { header: "Host", cell: (h) => <Link className="text-accent" to={`/hosts/${h.host_id}/management`}>{h.host_name}</Link> },
          { header: "Wave", cell: (h) => (h.wave === 0 ? "first (canary)" : `batch ${h.wave}`) },
          { header: "State", cell: (h) => <><Badge tone={hostStateTone(h.state)}>{hostStateWord[h.state] ?? h.state}</Badge>{h.offline && h.state === "pending" && <span className="ml-1 text-xs text-muted">offline: updates when it reconnects</span>}</> },
          { header: "From", cell: (h) => h.from_version },
          { header: "Detail", cell: (h) => <span className="text-xs text-muted">{h.detail}</span> },
        ]}
      />
    </div>
  );
}

function RolloutSettings({ st, manage }: { st: UpdateStatus; manage: boolean }) {
  const save = useSavePolicy();
  const [p, setP] = useState(st.policy);
  const [saved, setSaved] = useState(false);
  const num = (k: "canary" | "batch_size" | "max_failures" | "health_window_s") => (e: React.ChangeEvent<HTMLInputElement>) => setP({ ...p, [k]: Number(e.target.value) });
  const dirty = JSON.stringify(p) !== JSON.stringify(st.policy);
  return (
    <Card title="How updates roll out">
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save.mutate(p, { onSuccess: () => setSaved(true) }); }}>
        <p className="text-sm text-muted">The defaults suit most fleets: one Shepherd first, then five at a time, stopping at the first failure.</p>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="Hosts first (canary)"><Input type="number" min={1} value={p.canary} disabled={!manage} onChange={num("canary")} /></Field>
          <Field label="Then batches of"><Input type="number" min={1} value={p.batch_size} disabled={!manage} onChange={num("batch_size")} /></Field>
          <Field label="Pause after failures"><Input type="number" min={1} value={p.max_failures} disabled={!manage} onChange={num("max_failures")} /></Field>
          <Field label="Health window (s)" hint="A new version must connect to DROOL within this time, or it goes back to the previous one.">
            <Input type="number" min={30} max={3600} value={p.health_window_s} disabled={!manage} onChange={num("health_window_s")} /></Field>
        </div>
        <Field label="Exactly this version (optional)" hint="Automatic updates keep every Shepherd on this version instead of the newest in the channel.">
          <Input className="max-w-xs font-mono" placeholder="v1.0.1" value={p.version} disabled={!manage} onChange={(e) => setP({ ...p, version: e.target.value })} />
        </Field>
        {manage && (
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" variant="primary" disabled={!dirty || save.isPending}>Save rollout settings</Button>
            {saved && !dirty && <span className="text-sm text-muted" role="status">Saved.</span>}
          </div>
        )}
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
      </form>
    </Card>
  );
}

function CustomReleases({ st, manage }: { st: UpdateStatus; manage: boolean }) {
  const qc = useQueryClient();
  const releases = useReleases();
  const keys = useReleaseKeys();
  const start = useStartRollout();
  const [keyLine, setKeyLine] = useState("");
  const [bin, setBin] = useState<File>();
  const [manifest, setManifest] = useState<File>();
  const [progress, setProgress] = useState<number>();
  const inv = () => {
    for (const k of [ak.releases, ak.releaseKeys, ak.updateStatus]) void qc.invalidateQueries({ queryKey: k });
  };
  const addKey = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/release-keys", { body: { public_key: keyLine.trim() } })),
    onSuccess: () => { setKeyLine(""); inv(); },
  });
  const delKey = useMutation({ mutationFn: (id: string) => unwrap(api.DELETE("/agents/release-keys/{id}", { params: { path: { id } } })), onSuccess: inv });
  const uploadRelease = useMutation({
    mutationFn: async () => {
      const m = await manifest!.text();
      // Without the program: a description of a Shepherd DROOL ships.
      return upload("POST", "/agents/releases", bin ?? new Blob([]), setProgress, undefined, { "X-DROOL-Release": btoa(m) });
    },
    onSuccess: () => { setBin(undefined); setManifest(undefined); setProgress(undefined); inv(); },
    onError: () => setProgress(undefined),
  });
  const rollOut = async (version: string) => {
    const n = behind.get(version) ?? 0;
    const ok = await confirmAction({
      title: `Update ${plural(n, "Shepherd")} to ${version}?`, confirmLabel: `Update ${plural(n, "Shepherd")}`,
      body: <p>Shepherds running an older version update, {st.policy.canary === 1 ? "one first" : `${st.policy.canary} first`}, then batches of {st.policy.batch_size};
        the update pauses after {plural(st.policy.max_failures, "failure")}. A Shepherd that doesn&apos;t trust the key that signed {version} is skipped.</p>,
    });
    if (ok) start.mutate({ version });
  };
  const rows = releases.data?.items ?? [];
  // What updating to a version would update, over its platforms.
  const behind = new Map<string, number>();
  for (const r of rows) behind.set(r.version, (behind.get(r.version) ?? 0) + (r.shepherds_behind ?? 0));
  const offered = new Set<string>();
  return (
    <Card title="Custom releases">
      <div className="space-y-4">
        <p className="text-sm text-muted">
          Shepherd builds you sign yourself, with your own release-signing key (<span className="font-mono">drool release keygen</span>,{" "}
          <span className="font-mono">drool release sign</span>). Each host must trust that key itself: when you install its Shepherd, or with{" "}
          <span className="font-mono">sudo drool-agent trust-release-key</span> on the host; DROOL can&apos;t make a host trust a key. With automatic
          updates on, a custom release in the update channel is rolled out like an official one: sign test builds for another channel (for example{" "}
          <span className="font-mono">--channel testing</span>). <GuideLink page="Updating-Shepherds" anchor="custom-releases">Custom releases, step by step</GuideLink>
        </p>
        <div>
          <p className="mb-1 text-sm font-medium">Your release-signing keys</p>
          <p className="mb-2 text-xs text-muted">Listed here only so DROOL can check uploads early; adding one here doesn&apos;t make any host trust it.</p>
          <ul className="mb-2 space-y-1 text-sm">
            {(keys.data?.items ?? []).map((k) => (
              <li key={k.id} className="flex flex-wrap items-center gap-2"><span className="font-mono text-xs">{k.id}</span> {k.name}
                {manage && <Button size="sm" variant="ghost" onClick={() => delKey.mutate(k.id)}>Remove {k.name || k.id}</Button>}</li>
            ))}
            {keys.data && keys.data.items.length === 0 && <li className="text-muted">None.</li>}
          </ul>
          {manage && (
            <div className="flex flex-wrap gap-2">
              <Input className="max-w-xl font-mono text-xs" aria-label="Release-signing public key" placeholder="drool-release-ed25519 AAAA… my builds"
                value={keyLine} onChange={(e) => setKeyLine(e.target.value)} />
              <Button disabled={!keyLine.trim() || addKey.isPending} onClick={() => addKey.mutate()}>Add key</Button>
            </div>
          )}
          {(addKey.error || delKey.error) && <div className="mt-2"><Alert>{errorMessage(addKey.error ?? delKey.error)}</Alert></div>}
        </div>
        {manage && (
          <div className="grid gap-3 rounded-ui border border-line p-3 md:grid-cols-[1fr_1fr_auto]">
            <Field label="Signed description (.release.json)" hint="From drool release sign. The private key never comes near DROOL.">
              <input type="file" accept=".json" className="text-sm" onChange={(e) => setManifest(e.target.files?.[0])} />
            </Field>
            <Field label="Shepherd program" hint="Not needed for an official release of this DROOL's own Shepherd (without internet access).">
              <input type="file" className="text-sm" onChange={(e) => setBin(e.target.files?.[0])} />
            </Field>
            <div className="self-end">
              <Button variant="primary" disabled={!manifest || uploadRelease.isPending} onClick={() => uploadRelease.mutate()}>
                {progress != null ? `Uploading ${Math.round(progress * 100)}%` : "Upload release"}</Button>
            </div>
          </div>
        )}
        {uploadRelease.error && <Alert>{errorMessage(uploadRelease.error)}</Alert>}
        {start.error && <Alert>{errorMessage(start.error)}</Alert>}
        <Table
          caption="Shepherd releases on this controller"
          rows={rows}
          rowKey={(r) => r.id}
          empty={releases.isPending ? "Loading…" : "No releases yet. Official releases appear here once DROOL has verified them."}
          columns={[
            { header: "Version", cell: (r) => <span className="font-medium">{r.version}</span> },
            { header: "Signed by", cell: (r) => r.official ? <Badge tone="ok">DROOL project</Badge> : <Badge>your key {r.key_id}</Badge> },
            { header: "Platform", cell: (r) => `${r.os}/${r.arch}` },
            { header: "Channel", cell: (r) => r.channel },
            { header: "Program", cell: (r) => <span className="text-xs text-muted">{formatBytes(r.size)} · sha256 {r.sha256.slice(0, 12)}</span> },
            { header: "", cell: (r) => {
              // One button per version (its first row), when it would update a Shepherd.
              if (!manage || !(behind.get(r.version) ?? 0) || offered.has(r.version)) return null;
              offered.add(r.version);
              return <Button size="sm" onClick={() => void rollOut(r.version)}>Update {plural(behind.get(r.version)!, "Shepherd")} to {r.version}…</Button>;
            } },
          ]}
        />
      </div>
    </Card>
  );
}

function OfficialSource({ st }: { st: UpdateStatus }) {
  return (
    <Card title="Official releases">
      <div className="space-y-2 text-sm">
        <p className="text-muted">
          Shepherds follow DROOL: when DROOL is an official release, its own Shepherd is the official update. DROOL fetches only that
          Shepherd&apos;s signed description from the release source and checks it against the DROOL project&apos;s key built into it and against
          the Shepherd program it shipped with; each Shepherd checks the signature again with the key built into it before it updates. Nothing
          executable is downloaded.
        </p>
        <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[12rem_1fr]">
          <dt className="text-muted">Release source</dt>
          <dd className="break-all font-mono text-xs">{st.check.source === "off" ? "off" : st.check.source}</dd>
          <dt className="text-muted">DROOL project keys built in</dt>
          <dd className="font-mono text-xs">{st.official.keys.length > 0 ? st.official.keys.join(", ") : <span className="font-sans text-muted">none in this build</span>}</dd>
          <dt className="text-muted">This DROOL</dt>
          <dd>{st.controller.version} <span className="text-muted">({st.controller.channel})</span></dd>
        </dl>
        <p className="text-xs text-muted">The source is set with <span className="font-mono">agent.update_source</span> (a mirror, or <span className="font-mono">off</span>).
          A host can refuse official releases with <span className="font-mono">updates: {"{"}official: deny{"}"}</span> in its agent.yaml.{" "}
          <GuideLink page="Configuration">Configuration</GuideLink></p>
      </div>
    </Card>
  );
}
