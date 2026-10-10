import { useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { meQueryKey } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { CopyButton } from "@/components/ui/CopyButton";
import { errorMessage, formatDateTime } from "@/lib/format";
import { qrPath } from "@/lib/qr";

const mfaKey = ["auth", "mfa"] as const;

/** Two-factor authentication on the Account page (M16): set up an
 * authenticator app, confirm it, keep recovery codes, turn it off. */
export function TwoFactorCard() {
  const qc = useQueryClient();
  const status = useQuery({ queryKey: mfaKey, queryFn: () => unwrap(api.GET("/auth/mfa")) });
  const [enrollment, setEnrollment] = useState<Schemas["MfaEnrollment"]>();
  const [codes, setCodes] = useState<string[]>();
  const [confirmOff, setConfirmOff] = useState(false);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: mfaKey });
    void qc.invalidateQueries({ queryKey: meQueryKey });
  };
  const enroll = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/mfa/enroll")),
    onSuccess: (e) => setEnrollment(e),
  });
  const regenerate = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/mfa/recovery-codes")),
    onSuccess: (r) => {
      setCodes(r.recovery_codes);
      refresh();
    },
  });
  const disable = useMutation({
    mutationFn: () => unwrap(api.DELETE("/auth/mfa")),
    onSuccess: () => {
      setConfirmOff(false);
      refresh();
    },
  });
  const st = status.data;
  const err = status.error ?? enroll.error ?? regenerate.error;

  return (
    <Card title="Two-factor authentication">
      <div className="space-y-3 text-sm">
        {err && <Alert>{errorMessage(err)}</Alert>}
        {!st ? <p className="text-muted">Loading…</p> : st.enabled ? (
          <>
            <p>
              <Badge tone="ok">On</Badge>{" "}
              since {formatDateTime(st.enabled_at)}. Signing in needs your password and a code from your authenticator app.
            </p>
            <p className={st.recovery_codes_remaining <= 2 ? "text-warn" : "text-muted"}>
              {st.recovery_codes_remaining} unused recovery code{st.recovery_codes_remaining === 1 ? "" : "s"} left.
              {st.recovery_codes_remaining <= 2 && " Make new ones before you run out."}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => regenerate.mutate()} disabled={regenerate.isPending}>Make new recovery codes</Button>
              <Button variant="danger" onClick={() => setConfirmOff(true)}>Turn off…</Button>
            </div>
          </>
        ) : (
          <>
            <p>
              <Badge>Off</Badge>{" "}
              Signing in needs only your password. With two-factor authentication, it also needs a code from an
              authenticator app on your phone (for example Aegis, 2FAS, Google Authenticator or 1Password), so a stolen
              password alone isn't enough.
            </p>
            <Button variant="primary" onClick={() => enroll.mutate()} disabled={enroll.isPending}>
              {enroll.isPending ? "Starting…" : "Set up two-factor authentication"}
            </Button>
          </>
        )}
      </div>
      {enrollment && (
        <EnrollDialog enrollment={enrollment} onClose={() => setEnrollment(undefined)}
          onEnabled={(c) => {
            setEnrollment(undefined);
            setCodes(c);
            refresh();
          }} />
      )}
      {codes && <RecoveryCodesDialog codes={codes} onClose={() => setCodes(undefined)} />}
      {confirmOff && (
        <Dialog title="Turn off two-factor authentication?" onClose={() => setConfirmOff(false)}>
          <div className="space-y-4 text-sm">
            <p>Signing in will need only your password again, and your recovery codes stop working. Your other sessions are signed out.</p>
            {disable.error && <Alert>{errorMessage(disable.error)}</Alert>}
            <div className="flex justify-end gap-2">
              <Button onClick={() => setConfirmOff(false)}>Keep it on</Button>
              <Button variant="danger" onClick={() => disable.mutate()} disabled={disable.isPending}>Turn off</Button>
            </div>
          </div>
        </Dialog>
      )}
    </Card>
  );
}

function EnrollDialog({ enrollment, onClose, onEnabled }: {
  enrollment: Schemas["MfaEnrollment"];
  onClose: () => void;
  onEnabled: (codes: string[]) => void;
}) {
  const [code, setCode] = useState("");
  const qr = useMemo(() => qrPath(enrollment.otpauth_uri), [enrollment.otpauth_uri]);
  const confirm = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/mfa/confirm", { body: { code } })),
    onSuccess: (r) => onEnabled(r.recovery_codes),
  });
  const grouped = enrollment.secret.replace(/(.{4})/g, "$1 ").trim();
  function submit(e: FormEvent) {
    e.preventDefault();
    confirm.mutate();
  }
  return (
    <Dialog title="Set up two-factor authentication" onClose={onClose}>
      <form onSubmit={submit} className="space-y-4 text-sm">
        <ol className="list-decimal space-y-3 pl-5">
          <li>
            Scan this code with your authenticator app.
            {/* Dark on light whatever the theme: scanners expect it. */}
            <svg data-testid="mfa-qr" role="img" aria-label="QR code with the authenticator setup" viewBox={`0 0 ${qr.size} ${qr.size}`}
              className="mt-2 h-48 w-48 rounded-ui border border-line" shapeRendering="crispEdges">
              <rect width={qr.size} height={qr.size} fill="#ffffff" />
              <path d={qr.path} fill="#000000" />
            </svg>
            <p className="mt-2 text-muted">Can't scan it? Add an account by hand with this key (time-based):</p>
            <div className="mt-1 flex items-center gap-2">
              <code className="break-all font-mono text-xs" data-testid="mfa-secret">{grouped}</code>
              <CopyButton text={enrollment.secret} />
            </div>
          </li>
          <li>
            <Field label="Enter the 6-digit code the app shows" hint="This checks that the app is set up right before two-factor is turned on.">
              <Input value={code} onChange={(e) => setCode(e.target.value)} required autoComplete="one-time-code" inputMode="numeric" data-autofocus />
            </Field>
          </li>
        </ol>
        {confirm.error && <Alert>{errorMessage(confirm.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={confirm.isPending}>Turn on</Button>
        </div>
      </form>
    </Dialog>
  );
}

function RecoveryCodesDialog({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const text = codes.join("\n");
  const download = () => {
    const url = URL.createObjectURL(new Blob([`DROOL recovery codes (each works once)\n\n${text}\n`], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "drool-recovery-codes.txt";
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <Dialog title="Save your recovery codes" onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>
          If you lose your phone, each of these codes signs you in once instead of an authenticator code. Keep them somewhere
          safe, away from your password. <span className="font-medium">They are shown only now.</span>
        </p>
        <ul className="grid grid-cols-2 gap-x-6 gap-y-1 font-mono" data-testid="recovery-codes">
          {codes.map((c) => <li key={c}>{c}</li>)}
        </ul>
        <div className="flex flex-wrap justify-end gap-2">
          <CopyButton text={text} />
          <Button onClick={download}>Download</Button>
          <Button variant="primary" onClick={onClose}>I've saved them</Button>
        </div>
      </div>
    </Dialog>
  );
}
