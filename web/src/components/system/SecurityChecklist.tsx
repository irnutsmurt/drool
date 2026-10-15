import type { ReactNode } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { usePKI } from "@/components/agents/queries";
import { useAuditIntegrity, useAuditSink } from "@/components/audit/AuditIntegrity";
import { useSystemInfo } from "@/components/layout/security";
import { StatusBadge, type Status } from "@/components/monitoring/Status";
import { Card } from "@/components/ui/Card";
import { plainLink } from "@/components/ui/links";
import { formatBytes, formatDateTime } from "@/lib/format";
import { GuideLink } from "@/components/ui/GuideLink";

export function useSecurityFacts(enabled = true) {
  return useQuery({ queryKey: ["system", "security"], queryFn: () => unwrap(api.GET("/system/security")), enabled, staleTime: 60_000 });
}

/** Where an item stands. Problems need fixing; recommended hardening
 * protects against likely trouble; optional hardening is for those who
 * want more. Nothing here is required for DROOL to work. */
type Level = "problem" | "recommended" | "optional" | "done" | "manual";

const levels: Record<Level, { status: Status; word: string }> = {
  problem: { status: "critical", word: "Problem" },
  recommended: { status: "warning", word: "Recommended" },
  optional: { status: "info", word: "Optional" },
  done: { status: "ok", word: "Done" },
  manual: { status: "unknown", word: "Check yourself" },
};

interface Item {
  id: string;
  level: Level;
  title: string;
  body: ReactNode;
}

const code = "rounded bg-surface-2 px-1 font-mono text-xs";

/** "Secure your installation" on Settings → System: the security posture
 * DROOL can see, and what to check where it can't — recommendations, not
 * prerequisites (M17). */
export function SecurityChecklist() {
  const { can, me } = useAuth();
  const info = useSystemInfo();
  const facts = useSecurityFacts(can("settings.read"));
  const pki = usePKI(can("agents.read"));
  const integrity = useAuditIntegrity(can("audit.read"));
  const sink = useAuditSink(can("settings.read"));
  if (!can("settings.read")) return null;
  const f = facts.data;
  const d = info.data;
  if (!f || !d) {
    return (
      <Card title="Secure your installation">
        <p className="text-sm text-muted">{facts.isError ? "Couldn't load the security checks." : "Loading…"}</p>
      </Card>
    );
  }
  const container = f.deployment === "container";
  const items: Item[] = [];

  // Problems: things that are actively wrong.
  if (d.insecure_plaintext) {
    items.push({ id: "plaintext", level: "problem", title: "Plaintext HTTP on a network address",
      body: "Passwords and sessions cross the network unencrypted. Use the built-in TLS (the default) or put HTTPS in front." });
  } else if (d.tls_mode === "off") {
    items.push({ id: "proxy", level: "manual", title: "HTTPS through your reverse proxy",
      body: "DROOL serves plain HTTP on this machine only (TLS off, loopback): make sure the reverse proxy in front of it uses HTTPS." });
  } else {
    items.push({ id: "https", level: "done", title: "Encrypted connections", body: "The web UI and API use HTTPS." });
  }
  if (f.test_mode.length > 0) {
    items.push({ id: "test-mode", level: "problem", title: "Test switches are set",
      body: <>These environment variables change how DROOL behaves for automated tests and must never be set in production: {f.test_mode.join(", ")}.</> });
  }
  if (integrity.data?.state === "failed") {
    items.push({ id: "audit-chain", level: "problem", title: "Audit history verification failed",
      body: <>Stored audit records were changed outside DROOL. See the <Link className={plainLink} to="/audit">Audit Log</Link>.</> });
  }

  // Recommended hardening.
  const self = me?.user.role === "admin" && !me.mfa_enabled;
  items.push(f.admins_without_mfa.length === 0
    ? { id: "mfa", level: "done", title: "Two-factor sign-in for every administrator", body: "Every administrator account asks for a code from an authenticator app." }
    : { id: "mfa", level: "recommended", title: "Two-factor sign-in for every administrator",
        body: <>Without it, a stolen password is enough to sign in as an administrator: {f.admins_without_mfa.join(", ")}.{" "}
          {self ? <>Turn yours on under <Link className={plainLink} to="/settings/account">Settings → Account</Link>.</> : null}
          {can("users.manage") ? <> Others: <Link className={plainLink} to="/settings/users">Settings → Users</Link>.</> : null}</> });
  if (pki.data) {
    items.push(pki.data.root_key_online
      ? { id: "root-key", level: "recommended", title: "Agent Root CA key offline",
          body: <>The key Shepherds trust is still on this controller. Export it to offline storage and remove it{can("agents.pki")
            ? <> (<Link className={plainLink} to="/agents?tab=trust">Shepherds → Trust (PKI)</Link>)</> : null}. You need it again only to replace the intermediate CA.</> }
      : { id: "root-key", level: "done", title: "Agent Root CA key offline", body: "The Agent Root CA's private key isn't on this controller." });
  }
  items.push({ id: "master-key", level: "manual", title: "A copy of the master key, kept apart from your backups",
    body: (
      <>
        Stored credentials are encrypted with <code className={code}>{f.master_key.path}</code>. A backup without it can't give them back, and
        DROOL can't see whether you keep a copy. Copy it once (and after every rotation) to somewhere safe that isn't where your backups are:{" "}
        {container
          ? <><code className={code}>docker compose cp drool:{f.master_key.path} ./master.key</code> (Podman:{" "}
              <code className={code}>podman cp drool:{f.master_key.path} ./master.key</code>)</>
          : <code className={code}>sudo install -m 0600 -o "$USER" {f.master_key.path} ./master.key</code>}.
        {f.master_key.previous_files.length > 0 && (
          <> Older key files from a rotation are still next to it ({f.master_key.previous_files.join(", ")}): backups made before the rotation
            need them, so move them offline together with those backups.</>
        )}
      </>
    ) });
  items.push({ id: "backups", level: "manual", title: "Backups, kept off this machine",
    body: (
      <>
        {f.backups.newest_manual
          ? <>The newest backup in DROOL's data directory is from {formatDateTime(f.backups.newest_manual)}.</>
          : <>There is no <code className={code}>drool backup</code> in DROOL's data directory.</>}{" "}
        Copies you keep elsewhere are invisible to DROOL; check that you have a recent one off this machine (commands under Backup and restore
        below). <code className={code}>drool backup verify</code> checks one without restoring it.
      </>
    ) });
  const s = sink.data;
  items.push(s?.configured && s.enabled && s.state === "ok"
    ? { id: "audit-copy", level: "done", title: "Remote copy of the audit history", body: <>Every audit record is also sent to {s.url_display}.</> }
    : { id: "audit-copy", level: "recommended", title: "Remote copy of the audit history",
        body: <>A copy on another machine is what still tells the truth if this controller is ever compromised. Set one up under{" "}
          <Link className={plainLink} to="/audit">Audit Log → Remote copy</Link>{s?.configured && s.state !== "ok" ? " (it is set up but not delivering)" : ""}.</> });

  // Optional hardening.
  items.push(d.tls_mode === "provided"
    ? { id: "tls", level: "done", title: "Your own TLS certificate", body: "Browsers trust this controller's certificate." }
    : { id: "tls", level: "optional", title: "Your own TLS certificate",
        body: <>DROOL uses its own self-signed certificate, so browsers warn on first visit. Fine at home; to change it, set{" "}
          <code className={code}>server.tls.mode: provided</code> with your certificate (<GuideLink page="Configuration" anchor="your-own-tls-certificate">Your own TLS certificate</GuideLink>).</> });
  if (f.certificate_hosts + f.credential_hosts > 0) {
    items.push(f.credential_hosts === 0
      ? { id: "ssh-certs", level: "done", title: "Short-lived SSH certificates", body: "Every SSH host signs DROOL in with a certificate valid for minutes." }
      : { id: "ssh-certs", level: "optional", title: "Short-lived SSH certificates instead of stored keys",
          body: <>{f.credential_hosts} host{f.credential_hosts === 1 ? "" : "s"} still sign{f.credential_hosts === 1 ? "s" : ""} DROOL in with a stored key or password
            {f.certificate_hosts > 0 ? `, ${f.certificate_hosts} with certificates` : ""}. Certificates from DROOL's own SSH CA leave nothing long-lived on the
            controller to steal (<Link className={plainLink} to="/credentials">Credentials</Link>).</> });
  }

  const order: Level[] = ["problem", "recommended", "manual", "optional", "done"];
  items.sort((a, b) => order.indexOf(a.level) - order.indexOf(b.level));
  const open = items.filter((i) => i.level === "problem" || i.level === "recommended").length;
  return (
    <Card title="Secure your installation">
      <div id="secure" className="space-y-3 text-sm" data-testid="security-checklist">
        <p className="text-muted">
          {open === 0 ? "Nothing needs your attention." : `${open} thing${open === 1 ? "" : "s"} to look at.`} DROOL works without any of these; they protect it,
          and what it manages, against what could go wrong. Problems first, then recommendations, then what's up to you.
        </p>
        <ul className="space-y-3">
          {items.map((i) => (
            <li key={i.id} className="flex flex-col gap-1 sm:flex-row sm:items-start sm:gap-3" data-check={i.id} data-level={i.level}>
              <span className="shrink-0 sm:w-36"><StatusBadge status={levels[i.level].status}>{levels[i.level].word}</StatusBadge></span>
              <div className="min-w-0">
                <div className="font-medium">{i.title}</div>
                <div className="text-muted">{i.body}</div>
              </div>
            </li>
          ))}
        </ul>
        {f.backups.pre_upgrade > 0 && (
          <p className="text-xs text-muted">
            Backups in the data directory take {formatBytes(f.backups.bytes)} ({f.backups.manual} by <code className={code}>drool backup</code>,{" "}
            {f.backups.pre_upgrade} made before upgrades; DROOL keeps the newest three of those).
          </p>
        )}
      </div>
    </Card>
  );
}
