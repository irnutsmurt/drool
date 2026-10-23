import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { useGroups, useHosts } from "@/api/queries";
import { Alert as Notice } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { plainLink } from "@/components/ui/links";
import { ReadOnlyNote } from "@/components/ui/ReadOnlyNote";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { fmtSince, fmtTime } from "@/components/monitoring/format";
import { StatusBadge } from "@/components/monitoring/Status";
import {
  useDeleteDestination, useDeleteRoute, useDeliveries, useDestinations, useNotificationSettings, useRoutes, useSaveDestination,
  useSaveNotificationSettings, useSaveRoute, useTestDestination,
  type Destination, type DestinationConfig, type DestinationInput, type NotificationSettings, type Route, type RouteInput, type TestResult,
} from "@/components/alerts/queries";
import { categoryLabel, checkLabel } from "@/components/alerts/ui";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { emptyText } from "@/components/ui/States";

const kinds = [
  { key: "discord", label: "Discord", hint: "Channel settings → Integrations → Webhooks → copy the URL." },
  { key: "slack", label: "Slack", hint: "An incoming webhook URL from a Slack app (https://hooks.slack.com/services/…)." },
  { key: "telegram", label: "Telegram", hint: "A bot you create with @BotFather posts to your group, channel or chat." },
  { key: "email", label: "Email", hint: "Any SMTP server or provider: your mail host, a relay, or an app password." },
  { key: "webhook", label: "Webhook", hint: "POSTs JSON (schema drool.notification.v1) to any URL, including services on your own network." },
] as const;
const kindLabel: Record<string, string> = Object.fromEntries(kinds.map((k) => [k.key, k.label]));

export function NotificationsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const advanced = params.get("advanced") === "1";
  const setAdvanced = (v: boolean) => setParams((p) => { if (v) p.set("advanced", "1"); else p.delete("advanced"); return p; }, { replace: true });
  return (
    <div className="space-y-6">
      <p className="text-sm text-muted">
        Where alerts are sent. The alerts themselves, acknowledgements and maintenance are under <Link className={plainLink} to="/alerts">Alerts</Link>;
        what counts as a problem is set in <Link className={plainLink} to="/monitoring/settings">Monitoring → Health checks</Link>.
      </p>
      {!can("notifications.manage") && (
        <ReadOnlyNote>
          you can see destinations and routing (never their secrets); changing them needs the notifications.manage permission (administrators).
          {can("notifications.test") && " You can send test notifications."}
        </ReadOnlyNote>
      )}
      <Destinations />
      <div>
        <Button variant="ghost" aria-expanded={advanced} aria-controls="notify-advanced" onClick={() => setAdvanced(!advanced)}>
          {advanced ? "▾" : "▸"} Advanced: routing, delivery history and settings
        </Button>
      </div>
      {advanced && (
        <div id="notify-advanced" className="space-y-6">
          <Routes />
          <DeliveryHistory />
          {can("notifications.read") && <SettingsCard />}
        </div>
      )}
    </div>
  );
}

function health(d: Destination) {
  const h = d.health;
  switch (h.status) {
    case "failing":
      return <StatusBadge status="critical">Delivery failing</StatusBadge>;
    case "ok":
      return <StatusBadge status="ok">Delivering</StatusBadge>;
    case "disabled":
      return <Badge>Disabled</Badge>;
  }
  return <Badge>Not used yet</Badge>;
}

function Destinations() {
  const { can } = useAuth();
  const ds = useDestinations();
  const del = useDeleteDestination();
  const test = useTestDestination();
  const [edit, setEdit] = useState<Destination | "new" | null>(null);
  const [tested, setTested] = useState<Record<string, TestResult>>({});
  const rows = ds.data?.destinations ?? [];
  const manage = can("notifications.manage");
  return (
    <Card title="Notifications" actions={manage && rows.length > 0 && <Button size="sm" variant="primary" onClick={() => setEdit("new")}>Add destination</Button>}>
      {ds.error && <Notice>Couldn&apos;t load the destinations: {errorMessage(ds.error)}</Notice>}
      {rows.length === 0 && !ds.isLoading && !ds.error ? (
        <EmptyState title="No destinations configured" body="Add Discord, Slack, Telegram, email or a webhook, and DROOL tells you when something needs attention — and when it's fixed."
          action={manage && <Button variant="primary" onClick={() => setEdit("new")}>Add destination</Button>} />
      ) : (
        <ul className="divide-y divide-line">
          {rows.map((d) => (
            <li key={d.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
              <div className="min-w-0 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{d.name}</span>
                  <Badge>{kindLabel[d.kind] ?? d.kind}</Badge>
                  {health(d)}
                </div>
                <div className="mt-0.5 text-muted">
                  {d.kind === "email" ? `${d.config.to?.join(", ")} via ${d.config.host}`
                    : d.kind === "telegram" ? `Chat ${d.config.chat_id ?? ""}${d.config.thread_id ? `, topic ${d.config.thread_id}` : ""} · bot token •••••••`
                    : `URL: ${d.config.url_display ?? ""}•••••••`}
                  {" · "}Notify for: {d.notify_for === "recommended" ? "Recommended (Warnings + Critical, with recoveries)" : d.routes.length ? `routes: ${d.routes.join(", ")}` : "no route yet"}
                </div>
                <div className="text-xs text-muted">
                  Last success: {d.health.last_success_at ? fmtSince(d.health.last_success_at) : "never"}
                  {d.health.last_failure_at && <> · Last failure: {fmtSince(d.health.last_failure_at)} — {d.health.last_error}</>}
                  {d.health.queued > 0 && <> · {d.health.queued} waiting</>}
                </div>
                {d.warnings.map((w) => <div key={w} className="text-xs text-warn">⚠ {w}</div>)}
                {tested[d.id] && <TestOutcome r={tested[d.id]!} />}
              </div>
              <div className="flex gap-2">
                {can("notifications.test") && (
                  <Button size="sm" disabled={test.isPending} onClick={() => test.mutate({ id: d.id }, { onSuccess: (r) => setTested((t) => ({ ...t, [d.id]: r })) })}>
                    Send test
                  </Button>
                )}
                {manage && <Button size="sm" onClick={() => setEdit(d)}>Edit</Button>}
                {manage && (
                  <Button size="sm" variant="ghost" onClick={() => void confirmAction({ title: `Delete the destination ${d.name}?`, confirmLabel: "Delete destination", danger: true,
                    body: <p>Notifications not yet sent to it are cancelled, and routes stop sending to it. Alerts themselves are not affected.</p> })
                    .then((ok) => ok && del.mutate(d.id))}>
                    Delete
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {del.error && <Notice>{errorMessage(del.error)}</Notice>}
      {edit && <DestinationDialog dest={edit === "new" ? undefined : edit} onClose={() => setEdit(null)} />}
    </Card>
  );
}

const categoryText: Record<string, string> = {
  ok: "Delivered", timeout: "Timed out", tls: "TLS/certificate error", auth: "Authentication error", rejected: "Rejected by the server",
  rate_limited: "Rate limited", server_error: "Server error", network: "Network error", config: "Configuration problem",
};

function TestOutcome({ r }: { r: TestResult }) {
  const ok = r.outcome === "delivered";
  return (
    <div role="status" className={`mt-1 text-xs ${ok ? "text-ok" : "text-danger"}`}>
      {ok ? "✓ Test delivered" : `✕ Test failed — ${categoryText[r.category] ?? r.category}`}: {r.message}{" "}
      <span className="text-muted">({r.duration_ms} ms)</span>
    </div>
  );
}

function DestinationDialog({ dest, onClose }: { dest?: Destination; onClose: () => void }) {
  const save = useSaveDestination();
  const test = useTestDestination();
  const [kind, setKind] = useState<string>(dest?.kind ?? "");
  const [name, setName] = useState(dest?.name ?? "");
  const [cfg, setCfg] = useState<DestinationConfig>(dest?.config ?? { security: "starttls" });
  const [url, setUrl] = useState("");
  const [headerValue, setHeaderValue] = useState("");
  const [password, setPassword] = useState("");
  const [botToken, setBotToken] = useState("");
  const [to, setTo] = useState((dest?.config.to ?? []).join(", "));
  const [notifyFor, setNotifyFor] = useState(dest?.notify_for ?? "recommended");
  const [enabled, setEnabled] = useState(dest?.enabled ?? true);
  const [result, setResult] = useState<TestResult | null>(null);
  const set = (k: keyof DestinationConfig, v: unknown) => setCfg((c) => ({ ...c, [k]: v }));
  const secrets = () => {
    const s: Record<string, string> = {};
    if (url) s.url = url;
    if (headerValue) s.header_value = headerValue;
    if (password) s.password = password;
    if (botToken) s.bot_token = botToken;
    return Object.keys(s).length ? s : undefined;
  };
  const config = (): DestinationConfig => ({ ...cfg, to: kind === "email" ? to.split(/[,\s]+/).filter(Boolean) : undefined });
  const body = (): DestinationInput => ({ name: name || kindLabel[kind], kind: kind as "email", enabled, config: config(), secrets: secrets(), notify_for: notifyFor as "recommended" });
  const sendTest = () => {
    setResult(null);
    const unchanged = dest && !secrets() && JSON.stringify(config()) === JSON.stringify({ ...dest.config, to: kind === "email" ? dest.config.to : undefined });
    test.mutate(unchanged ? { id: dest!.id } : { kind, config: config(), secrets: secrets() }, { onSuccess: setResult });
  };
  if (!kind) {
    return (
      <Dialog title="Add destination" onClose={onClose}>
        <p className="mb-3 text-sm text-muted">Where should DROOL send alerts?</p>
        <div className="grid grid-cols-2 gap-2">
          {kinds.map((k) => (
            <button key={k.key} type="button" onClick={() => setKind(k.key)}
              className="rounded-ui border border-line p-3 text-left hover:border-accent focus-visible:outline-2 focus-visible:outline-accent">
              <div className="font-medium">{k.label}</div>
              <div className="text-xs text-muted">{k.hint}</div>
            </button>
          ))}
        </div>
      </Dialog>
    );
  }
  const hint = kinds.find((k) => k.key === kind)?.hint;
  const secretPlaceholder = dest ? "••••••• (unchanged)" : "";
  return (
    <Dialog title={dest ? `Edit ${dest.name}` : `Add ${kindLabel[kind]}`} onClose={onClose}>
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save.mutate({ id: dest?.id, body: body() }, { onSuccess: onClose }); }}>
        <Field label="Name"><Input value={name} placeholder={kindLabel[kind]} onChange={(e) => setName(e.target.value)} data-autofocus /></Field>
        {kind === "telegram" ? (
          <>
            <ol className="list-decimal space-y-1 rounded-ui border border-line bg-surface-2 p-3 pl-7 text-xs" data-testid="telegram-steps">
              <li>In Telegram, open <strong>@BotFather</strong>, send <code>/newbot</code>, and copy the token it gives you.</li>
              <li>Add the bot to your group, or to your channel as an administrator. For a chat with just you, send the bot a message.</li>
              <li>
                The chat ID: <code>@channelname</code> for a public channel. Otherwise send a message in the chat, open
                {" "}<code>https://api.telegram.org/bot&lt;token&gt;/getUpdates</code> in a browser with your token in place of &lt;token&gt;, and copy the
                {" "}<code>&quot;chat&quot;:{"{"}&quot;id&quot;:…</code> number (groups and channels start with -100). That address contains the token: don&apos;t share it.
              </li>
            </ol>
            <Field label="Bot token" hint="Stored encrypted. DROOL never shows it again, and it never appears in logs, the audit log or delivery history.">
              <Input type="password" autoComplete="off" value={botToken} placeholder={dest?.secrets_set.bot_token ? "••••••• (unchanged)" : "123456789:AA…"}
                onChange={(e) => setBotToken(e.target.value)} required={!dest} />
            </Field>
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2">
                <Field label="Chat ID"><Input value={cfg.chat_id ?? ""} placeholder="-1001234567890 or @channelname" onChange={(e) => set("chat_id", e.target.value)} required /></Field>
              </div>
              <Field label="Topic ID (optional)" hint="Groups with topics">
                <Input type="number" min={1} inputMode="numeric" value={cfg.thread_id ?? ""} onChange={(e) => set("thread_id", e.target.value ? Number(e.target.value) : undefined)} />
              </Field>
            </div>
          </>
        ) : kind !== "email" ? (
          <>
            <Field label={kind === "webhook" ? "URL" : "Webhook URL"} hint={hint}>
              <Input type="password" autoComplete="off" value={url} placeholder={secretPlaceholder || (kind === "discord" ? "https://discord.com/api/webhooks/…" : kind === "slack" ? "https://hooks.slack.com/services/…" : "https://…")}
                onChange={(e) => setUrl(e.target.value)} required={!dest} />
            </Field>
            {kind === "webhook" && (
              <div className="grid grid-cols-2 gap-3">
                <Field label="Secret header (optional)"><Input value={cfg.header_name ?? ""} placeholder="Authorization" onChange={(e) => set("header_name", e.target.value)} /></Field>
                <Field label="Header value"><Input type="password" autoComplete="off" value={headerValue} placeholder={dest?.secrets_set.header_value ? "••••••• (unchanged)" : "Bearer …"} onChange={(e) => setHeaderValue(e.target.value)} /></Field>
              </div>
            )}
          </>
        ) : (
          <>
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2"><Field label="SMTP server"><Input value={cfg.host ?? ""} placeholder="smtp.example.com" onChange={(e) => set("host", e.target.value)} required /></Field></div>
              <Field label="Port"><Input type="number" value={cfg.port ?? ""} placeholder={cfg.security === "tls" ? "465" : cfg.security === "none" ? "25" : "587"} onChange={(e) => set("port", e.target.value ? Number(e.target.value) : undefined)} /></Field>
            </div>
            <Field label="Security">
              <Select value={cfg.security ?? "starttls"} onChange={(e) => set("security", e.target.value)}>
                <option value="starttls">STARTTLS (port 587, recommended)</option>
                <option value="tls">TLS (port 465)</option>
                <option value="none">None — unencrypted</option>
              </Select>
            </Field>
            {cfg.security === "none" && (
              <Checkbox checked={!!cfg.allow_plaintext} onChange={(v) => set("allow_plaintext", v)}
                label="Send unencrypted" hint="Only for a relay on a trusted network. DROOL never sends a password without encryption." />
            )}
            <div className="grid grid-cols-2 gap-3">
              <Field label="Username (optional)"><Input value={cfg.username ?? ""} autoComplete="off" onChange={(e) => set("username", e.target.value)} /></Field>
              <Field label="Password"><Input type="password" autoComplete="new-password" value={password} placeholder={dest?.secrets_set.password ? "••••••• (unchanged)" : ""} onChange={(e) => setPassword(e.target.value)} /></Field>
            </div>
            <Field label="From"><Input value={cfg.from ?? ""} placeholder="DROOL <drool@example.com>" onChange={(e) => set("from", e.target.value)} required /></Field>
            <Field label="To" hint="One or more addresses, separated by commas."><Input value={to} placeholder="you@example.com" onChange={(e) => setTo(e.target.value)} required /></Field>
          </>
        )}
        <Field label="Notify for">
          <Select value={notifyFor} onChange={(e) => setNotifyFor(e.target.value as "recommended")}>
            <option value="recommended">Recommended: Warnings and Critical, plus recoveries</option>
            <option value="custom">Only what advanced routes send here</option>
          </Select>
        </Field>
        {dest && <Checkbox checked={enabled} onChange={setEnabled} label="Enabled" />}
        {result && <TestOutcome r={result} />}
        {result?.warnings.map((w) => <div key={w} className="text-xs text-warn">⚠ {w}</div>)}
        {save.error && <Notice>{errorMessage(save.error)}</Notice>}
        {test.error && <Notice>{errorMessage(test.error)}</Notice>}
        <div className="flex flex-wrap justify-between gap-2">
          <Button type="button" onClick={sendTest} disabled={test.isPending}>{test.isPending ? "Sending…" : "Send test"}</Button>
          <div className="flex gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}

// --- advanced ---

function routeSummary(r: Route, hostName: (id: string) => string, groupName: (id: string) => string) {
  const parts: string[] = [];
  parts.push(r.severities.length === 0 || r.severities.length === 2 ? "Warning and Critical" : r.severities.map((s) => (s === "critical" ? "Critical" : "Warning")).join(", "));
  const scope = [...r.host_ids.map(hostName), ...r.group_ids.map((g) => `Pack ${groupName(g)}`)];
  if (scope.length) parts.push(`on ${scope.join(", ")}`);
  const what = [...r.categories.map((c) => categoryLabel[c] ?? c), ...r.checks.map((c) => checkLabel[c] ?? c)];
  if (what.length) parts.push(`(${what.join(", ")})`);
  return parts.join(" ");
}

function Routes() {
  const { can } = useAuth();
  const rs = useRoutes();
  const ds = useDestinations();
  const hosts = useHosts();
  const groups = useGroups();
  const del = useDeleteRoute();
  const [edit, setEdit] = useState<Route | "new" | null>(null);
  const dname = (id: string) => ds.data?.destinations.find((d) => d.id === id)?.name ?? id;
  const hname = (id: string) => hosts.data?.items.find((h) => h.id === id)?.name ?? id;
  const gname = (id: string) => groups.data?.items.find((g) => g.id === id)?.name ?? id;
  const manage = can("notifications.manage");
  return (
    <Card title="Routing" actions={manage && <Button size="sm" onClick={() => setEdit("new")}>Add route</Button>}>
      <p className="mb-3 text-sm text-muted">
        Every route whose filters match an alert sends it to its destinations; a destination chosen by several routes is notified once.
        There is no order to think about. Most setups need only the Recommended route.
      </p>
      {rs.error && <Notice>{errorMessage(rs.error)}</Notice>}
      <ul className="divide-y divide-line">
        {(rs.data?.routes ?? []).map((r) => (
          <li key={r.id} className="flex flex-wrap items-start justify-between gap-3 py-3 text-sm">
            <div>
              <div className="flex items-center gap-2">
                <span className="font-medium">{r.name}</span>
                {r.is_default && <Badge tone="accent">Default</Badge>}
                {!r.enabled && <Badge>Disabled</Badge>}
              </div>
              <div className="text-muted">{routeSummary(r, hname, gname)} → {r.destination_ids.length ? r.destination_ids.map(dname).join(", ") : "no destination"}</div>
              <div className="text-xs text-muted">
                Recoveries {r.recovery ? "on" : "off"} · Reminders {r.reminder_minutes ? `every ${r.reminder_minutes >= 60 ? `${r.reminder_minutes / 60} h` : `${r.reminder_minutes} min`} until acknowledged` : "off"}
              </div>
            </div>
            {manage && (
              <div className="flex gap-2">
                <Button size="sm" onClick={() => setEdit(r)}>Edit</Button>
                {!r.is_default && <Button size="sm" variant="ghost" onClick={() => void confirmAction({ title: `Delete the route ${r.name}?`, confirmLabel: "Delete route", danger: true,
                  body: <p>Alerts it matches are sent by the other routes only (or not at all, if none matches them). Alerts themselves are not affected.</p> })
                  .then((ok) => ok && del.mutate(r.id))}>Delete</Button>}
              </div>
            )}
          </li>
        ))}
      </ul>
      {del.error && <Notice>{errorMessage(del.error)}</Notice>}
      {edit && <RouteDialog route={edit === "new" ? undefined : edit} categories={rs.data?.categories ?? []} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function toggle(list: string[], v: string) {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
}

function RouteDialog({ route, categories, onClose }: { route?: Route; categories: string[]; onClose: () => void }) {
  const save = useSaveRoute();
  const ds = useDestinations();
  const hosts = useHosts();
  const groups = useGroups();
  const [r, setR] = useState<Required<RouteInput>>({
    name: route?.name ?? "", enabled: route?.enabled ?? true, severities: route?.severities ?? [], host_ids: route?.host_ids ?? [],
    group_ids: route?.group_ids ?? [], categories: route?.categories ?? [], checks: route?.checks ?? [], recovery: route?.recovery ?? true,
    reminder_minutes: route?.reminder_minutes ?? 0, destination_ids: route?.destination_ids ?? [],
  });
  const up = (p: Partial<RouteInput>) => setR((x) => ({ ...x, ...p }));
  return (
    <Dialog title={route ? `Edit route ${route.name}` : "Add route"} onClose={onClose} wide>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); save.mutate({ id: route?.id, body: r }, { onSuccess: onClose }); }}>
        <Field label="Name"><Input value={r.name} onChange={(e) => up({ name: e.target.value })} required data-autofocus placeholder="e.g. Storage warnings on the NAS" /></Field>
        <fieldset>
          <legend className="mb-1 text-sm font-medium">Severity <span className="font-normal text-muted">(none ticked: both)</span></legend>
          <div className="flex gap-4">
            <Checkbox checked={r.severities.includes("critical")} onChange={() => up({ severities: toggle(r.severities, "critical") as ("critical" | "warning")[] })} label="Critical" />
            <Checkbox checked={r.severities.includes("warning")} onChange={() => up({ severities: toggle(r.severities, "warning") as ("critical" | "warning")[] })} label="Warning" />
          </div>
        </fieldset>
        <fieldset>
          <legend className="mb-1 text-sm font-medium">Hosts and Packs <span className="font-normal text-muted">(none: all hosts)</span></legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {(groups.data?.items ?? []).map((g) => <Checkbox key={g.id} checked={r.group_ids.includes(g.id)} onChange={() => up({ group_ids: toggle(r.group_ids, g.id) })} label={<>Pack <b>{g.name}</b></>} />)}
          </div>
          <div className="mt-1 max-h-28 overflow-y-auto rounded-ui border border-line p-2">
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {(hosts.data?.items ?? []).filter((h) => !h.jump_only).map((h) => <Checkbox key={h.id} checked={r.host_ids.includes(h.id)} onChange={() => up({ host_ids: toggle(r.host_ids, h.id) })} label={h.name} />)}
            </div>
          </div>
        </fieldset>
        <fieldset>
          <legend className="mb-1 text-sm font-medium">Categories <span className="font-normal text-muted">(none: everything)</span></legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {categories.filter((c) => c !== "fidelity").map((c) => <Checkbox key={c} checked={r.categories.includes(c)} onChange={() => up({ categories: toggle(r.categories, c) })} label={categoryLabel[c] ?? c} />)}
          </div>
        </fieldset>
        <fieldset>
          <legend className="mb-1 text-sm font-medium">Send to</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {(ds.data?.destinations ?? []).map((d) => <Checkbox key={d.id} checked={r.destination_ids.includes(d.id)} onChange={() => up({ destination_ids: toggle(r.destination_ids, d.id) })} label={d.name} />)}
            {(ds.data?.destinations.length ?? 0) === 0 && <span className="text-sm text-muted">Add a destination first.</span>}
          </div>
        </fieldset>
        <div className="grid gap-3 sm:grid-cols-2">
          <Checkbox checked={r.recovery} onChange={(v) => up({ recovery: v })} label="Send recoveries" hint="When the problem is fixed, to destinations that got the alert." />
          <Field label="Reminders while unacknowledged">
            <Select value={r.reminder_minutes} onChange={(e) => up({ reminder_minutes: Number(e.target.value) })}>
              <option value={0}>Off (recommended)</option><option value={60}>Every hour</option><option value={240}>Every 4 hours</option>
              <option value={720}>Every 12 hours</option><option value={1440}>Every day</option>
            </Select>
          </Field>
        </div>
        <Checkbox checked={r.enabled} onChange={(v) => up({ enabled: v })} label="Enabled" />
        {save.error && <Notice>{errorMessage(save.error)}</Notice>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={save.isPending}>Save</Button>
        </div>
      </form>
    </Dialog>
  );
}

function DeliveryHistory() {
  const ds = useDestinations();
  const [dest, setDest] = useState("");
  const [status, setStatus] = useState("");
  const q = useDeliveries({ destination_id: dest || undefined, status: status || undefined });
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Card title="Delivery history">
      <div className="mb-3 flex flex-wrap gap-3">
        <Field label="Destination">
          <Select value={dest} onChange={(e) => setDest(e.target.value)}>
            <option value="">All</option>
            {(ds.data?.destinations ?? []).map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </Select>
        </Field>
        <Field label="Status">
          <Select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">All</option><option value="queued">Waiting / retrying</option><option value="delivered">Delivered</option>
            <option value="failed">Failed</option><option value="cancelled">Not sent</option>
          </Select>
        </Field>
      </div>
      {q.error && <Notice>{errorMessage(q.error)}</Notice>}
      {rows.length === 0 ? <p className="text-sm text-muted">{emptyText("deliveries", q, "No deliveries yet.", true)}</p> : (
        <ul className="divide-y divide-line text-sm">
          {rows.map((x) => (
            <li key={x.id} className="py-2">
              <span className="font-medium">{x.destination}</span> · {x.kind} · <span className={x.status === "failed" ? "text-danger" : x.status === "delivered" ? "text-ok" : ""}>{x.status}</span>
              {x.ambiguous && <span className="text-warn"> · may have arrived twice</span>}
              <span className="text-muted"> · {x.host_name} — {x.alert_title} · {fmtTime(x.created_at)} · {x.attempts} attempt{x.attempts === 1 ? "" : "s"}</span>
              {x.last_error && <div className="text-xs text-muted">{x.last_error}</div>}
            </li>
          ))}
        </ul>
      )}
      {q.hasNextPage && <Button className="mt-2" onClick={() => void q.fetchNextPage()}>Load more</Button>}
    </Card>
  );
}

function SettingsCard() {
  const { can } = useAuth();
  const s = useNotificationSettings();
  const save = useSaveNotificationSettings();
  const [v, setV] = useState<NotificationSettings | null>(null);
  const cur = v ?? s.data?.settings;
  if (!cur) return null;
  const up = (p: Partial<NotificationSettings>) => setV({ ...cur, ...p });
  const manage = can("notifications.manage");
  return (
    <Card title="Settings">
      <form className="grid gap-3 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); save.mutate(cur, { onSuccess: () => setV(null) }); }}>
        <Field label="DROOL address for links" hint="Notifications link here. Set automatically from your browser when the first destination is added.">
          <Input value={cur.link_base_url} disabled={!manage} placeholder={window.location.origin} onChange={(e) => up({ link_base_url: e.target.value })} />
        </Field>
        <Field label="Time zone in notifications" hint="IANA name such as Europe/Berlin; empty: the controller's.">
          <Input value={cur.time_zone} disabled={!manage} onChange={(e) => up({ time_zone: e.target.value })} />
        </Field>
        <Field label="Keep alert history (days)"><Input type="number" min={30} max={730} disabled={!manage} value={cur.alert_retention_days} onChange={(e) => up({ alert_retention_days: Number(e.target.value) })} /></Field>
        <Field label="Keep delivery details (days)"><Input type="number" min={7} max={365} disabled={!manage} value={cur.delivery_retention_days} onChange={(e) => up({ delivery_retention_days: Number(e.target.value) })} /></Field>
        {save.error && <div className="sm:col-span-2"><Notice>{errorMessage(save.error)}</Notice></div>}
        {manage && <div className="sm:col-span-2"><Button type="submit" variant="primary" disabled={!v || save.isPending}>Save settings</Button></div>}
      </form>
    </Card>
  );
}
