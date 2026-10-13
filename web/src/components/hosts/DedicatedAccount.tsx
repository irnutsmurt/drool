import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { jk, qk, type Host } from "@/api/queries";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Field, Input } from "@/components/ui/Field";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";

/** Said wherever DROOL's own account is offered or set up. */
export const ROOT_EQUIVALENT =
  "Full management gives this DROOL account passwordless sudo and therefore root-equivalent administrative capability.";

type Plan = Schemas["SSHAccountPlan"];
type CredentialPlan = Plan["credentials"][number];

const kindText: Record<string, string> = { password: "password", sudo_password: "sudo password", ssh_private_key: "SSH key" };
const users = (names: string[]) => (names.length === 1 ? `${names[0]} still uses it` : `${names.join(", ")} still use it`);

function planOutcome(c: CredentialPlan): string {
  const what = `${c.name} (${kindText[c.kind] ?? c.kind})`;
  switch (c.outcome) {
    case "delete":
      return `DROOL forgets ${what}: it was entered only for the setup.`;
    case "retain":
      return `DROOL keeps ${what}, although it was entered for the setup: ${users(c.used_by)}.`;
    default:
      return `${what} stays under Credentials: it's a stored credential. Delete it there when nothing needs it.`;
  }
}

/** The ssh_account step's result (internal/sshaccount.Result). */
interface AccountResult {
  user: string;
  setup_user: string;
  phase: string;
  account?: string;
  verified: boolean;
  switched: boolean;
  outcome_unknown?: boolean;
  credentials: { name: string; kind: string; outcome: string; used_by?: string[] }[];
}

/** What a setup did, on the job page and after the setup. */
export function AccountSetupResult({ r }: { r: Record<string, unknown> }) {
  const a = r as unknown as AccountResult;
  return (
    <dl className="grid grid-cols-[8rem_1fr] gap-y-1 text-sm sm:grid-cols-[10rem_1fr]" data-testid="ssh-account-result">
      <dt className="text-muted">DROOL's account</dt>
      <dd>
        <span className="font-mono text-xs">{a.user}</span>
        {a.account === "created" ? " (created now)" : a.account === "existing" ? " (created by DROOL on an earlier attempt)" : " (not created)"}
      </dd>
      <dt className="text-muted">Fresh sign-in</dt>
      <dd>{a.verified ? "worked, and so did sudo without a password" : "not proven"}</dd>
      <dt className="text-muted">Switched</dt>
      <dd>{a.switched ? `yes: DROOL signs in as ${a.user} now` : `no: DROOL still signs in as ${a.setup_user}`}</dd>
      {a.outcome_unknown && (<><dt className="text-muted">Outcome</dt><dd>unknown: the connection was lost during the setup</dd></>)}
      {a.credentials.length > 0 && (
        <>
          <dt className="text-muted">Credentials</dt>
          <dd>
            <ul className="space-y-0.5">
              {a.credentials.map((c) => (
                <li key={c.name} className="break-words">
                  <span className="font-mono text-xs">{c.name}</span>:{" "}
                  {c.outcome === "deleted" ? "forgotten" : c.outcome === "retained" ? `kept, ${users(c.used_by ?? [])}` : "kept (a stored credential)"}
                </li>
              ))}
            </ul>
          </dd>
        </>
      )}
    </dl>
  );
}

const active = (s?: string) => s === "pending" || s === "running" || s === "paused";

/** Set up DROOL's own SSH account on a host: the plan first (what changes on
 * the host, what becomes of the credentials, what DROOL leaves alone), then,
 * once confirmed, the job that sets it up and switches, and its result. The
 * server decides everything; this only asks and shows. Used by Add host and
 * by an existing host's page alike. */
export function DedicatedAccountSetup({ host, initialUser = "drool", onFinished, onCancel, cancelLabel = "Cancel" }: {
  host: Host;
  initialUser?: string;
  onFinished: () => void;
  onCancel?: () => void;
  cancelLabel?: string;
}) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState(initialUser);
  const [user, setUser] = useState(initialUser);
  const [jobId, setJobId] = useState<string>();
  // Ask the host again once the name stops changing.
  useEffect(() => {
    const t = setTimeout(() => setUser(draft.trim() || "drool"), 500);
    return () => clearTimeout(t);
  }, [draft]);
  const plan = useQuery({
    queryKey: ["hosts", host.id, "ssh-account-plan", user],
    queryFn: () => unwrap(api.POST("/hosts/{id}/ssh-account/plan", { params: { path: { id: host.id } }, body: { user } })),
    enabled: !jobId,
    gcTime: 0,
    retry: false,
  });
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/ssh-account", { params: { path: { id: host.id } }, body: { user } })),
    onSuccess: (j) => setJobId(j.id),
  });
  const job = useQuery({
    queryKey: jk.job(jobId ?? "none"),
    queryFn: () => unwrap(api.GET("/jobs/{id}", { params: { path: { id: jobId! } } })),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && !active(q.state.data.status) ? false : 1000),
  });
  const done = !!job.data && !active(job.data.status);
  const detail = useQuery({
    queryKey: jk.jobHost(jobId ?? "none", host.id),
    queryFn: () => unwrap(api.GET("/jobs/{id}/hosts/{hostId}", { params: { path: { id: jobId!, hostId: host.id } } })),
    enabled: done,
  });
  useEffect(() => {
    if (!done) return;
    void qc.invalidateQueries({ queryKey: qk.hosts });
    void qc.invalidateQueries({ queryKey: qk.credentials });
  }, [done, qc]);

  if (jobId) {
    if (!done || !detail.data) {
      return (
        <div className="space-y-3 text-sm" data-testid="ssh-account-running">
          <p>Setting up <strong>{user}</strong> on {host.name}, then signing in as it on a brand-new connection…</p>
          <Link className={plainLink} to={`/jobs/${jobId}`}>View the run</Link>
        </div>
      );
    }
    const d = detail.data;
    const ok = d.state === "succeeded";
    const result = d.steps?.[0]?.result as Record<string, unknown> | undefined;
    return (
      <div className="space-y-4 text-sm" data-testid="ssh-account-done">
        {ok
          ? <Alert tone="ok">{host.name} now signs in as <strong>{user}</strong> with DROOL's key.</Alert>
          : <Alert>{d.error_message || "The setup failed."}{d.error_hint && <> {d.error_hint}</>}</Alert>}
        {result && Object.keys(result).length > 0 && <AccountSetupResult r={result} />}
        <div className="flex flex-wrap items-center gap-3">
          {ok ? <Button variant="primary" onClick={onFinished}>Done</Button> : (
            <>
              <Button variant="primary" onClick={() => { setJobId(undefined); start.reset(); void plan.refetch(); }}>Try again</Button>
              {onCancel && <Button onClick={onCancel}>{cancelLabel}</Button>}
            </>
          )}
          <Link className={plainLink} to={`/jobs/${jobId}`}>View the run</Link>
        </div>
      </div>
    );
  }

  const p: Plan | undefined = plan.data;
  const settled = draft.trim() === user || (draft.trim() === "" && user === "drool");
  const blocked = !p || p.blockers.length > 0;
  return (
    <div className="space-y-4 text-sm" data-testid="ssh-account-setup">
      <p>
        DROOL signs in to {host.name} as <strong>{p?.setup_user ?? host.user}</strong> only to set itself up. It creates its own
        account, signs in as that account on a brand-new connection, and switches to it only if that works. If anything fails
        along the way, {host.name} keeps working as it does now.
      </p>
      <Field label="DROOL's account name" hint="Created on the host. Lower-case letters, digits, - and _.">
        <Input value={draft} onChange={(e) => setDraft(e.target.value)} className="max-w-xs font-mono" data-autofocus
          autoComplete="off" spellCheck={false} />
      </Field>
      {(plan.isFetching || !settled) && <p className="text-muted">Asking {host.name}…</p>}
      {plan.error && <Alert>{errorMessage(plan.error)}</Alert>}
      {settled && !plan.isFetching && p && (
        <>
          {p.blockers.length > 0 && (
            <Alert>{p.blockers.map((b) => <p key={b}>{b}</p>)}</Alert>
          )}
          {p.changes.length > 0 && (
            <section>
              <h3 className="mb-1 font-medium">On {host.name}, DROOL will</h3>
              <ol className="list-decimal space-y-1 pl-5">{p.changes.map((c) => <li key={c}>{c}</li>)}</ol>
            </section>
          )}
          {p.credentials.length > 0 && (
            <section>
              <h3 className="mb-1 font-medium">Once it has switched</h3>
              <ul className="list-disc space-y-1 pl-5">{p.credentials.map((c) => <li key={c.id}>{planOutcome(c)}</li>)}</ul>
            </section>
          )}
          {p.notes.length > 0 && (
            <section>
              <h3 className="mb-1 font-medium">What stays as it is</h3>
              <ul className="list-disc space-y-1 pl-5">{p.notes.map((n) => <li key={n}>{n}</li>)}</ul>
            </section>
          )}
          {p.check?.sshd_limits && (
            <Alert tone="warn">
              sshd on {host.name} limits who may sign in ({p.check.sshd_limits}). If that doesn't let {p.user} in, the setup stops
              before switching.
            </Alert>
          )}
          {p.check?.sudo_warning && <Alert tone="warn">{p.check.sudo_warning}. They aren't DROOL's to fix; DROOL checks that its own rule adds no problem.</Alert>}
        </>
      )}
      <Alert tone="warn">{ROOT_EQUIVALENT}</Alert>
      {start.error && <Alert>{errorMessage(start.error)}</Alert>}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" onClick={() => start.mutate()} disabled={blocked || !settled || plan.isFetching || start.isPending}>
          {start.isPending ? "Starting…" : `Create ${user} and switch`}
        </Button>
        {onCancel && <Button onClick={onCancel}>{cancelLabel}</Button>}
      </div>
    </div>
  );
}
