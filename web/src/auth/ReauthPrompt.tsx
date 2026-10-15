import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, setReauthHandler, unwrap, type ReauthInfo } from "@/api/client";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { errorMessage } from "@/lib/format";
import { meQueryKey } from "./AuthContext";

/** Asks for the password (and an authenticator code) when the server says
 * a high-impact action needs the user to confirm who they are, then lets
 * the API client repeat the action. One prompt serves every request that
 * needed it at the same time. */
export function ReauthPrompt() {
  const qc = useQueryClient();
  const [info, setInfo] = useState<ReauthInfo | null>(null);
  const waiting = useRef<{ promise: Promise<boolean>; resolve: (ok: boolean) => void } | null>(null);

  useEffect(() => {
    setReauthHandler((i) => {
      if (waiting.current) return waiting.current.promise;
      let resolve!: (ok: boolean) => void;
      const promise = new Promise<boolean>((r) => (resolve = r));
      waiting.current = { promise, resolve };
      setInfo(i);
      return promise;
    });
    return () => setReauthHandler(undefined);
  }, []);

  const finish = (ok: boolean) => {
    waiting.current?.resolve(ok);
    waiting.current = null;
    setInfo(null);
    if (ok) void qc.invalidateQueries({ queryKey: meQueryKey });
  };

  if (!info) return null;
  return <ReauthDialog info={info} onDone={finish} />;
}

function ReauthDialog({ info, onDone }: { info: ReauthInfo; onDone: (ok: boolean) => void }) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const minutes = Math.round(info.window_s / 60);
  const lasts = info.window_s < 90 ? `${info.window_s} seconds` : `${minutes} minutes`;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await unwrap(api.POST("/auth/reauth", {
        body: { password, ...(info.mfa ? (useRecovery ? { recovery_code: code } : { code }) : {}) },
      }));
      onDone(true);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Dialog title="Confirm it's you" onClose={() => onDone(false)}>
      <form onSubmit={submit} className="space-y-4" data-testid="reauth-dialog">
        {info.explanation && <p className="text-sm" data-testid="reauth-reason">{info.explanation}</p>}
        <p className="text-sm text-muted">
          {info.explanation ? "DROOL" : "This is a high-impact action, so DROOL"} asks for your password{info.mfa ? " and a code from your authenticator" : ""} again.
          One confirmation lasts {lasts}: other high-impact actions in that time don't ask again.
        </p>
        {error && <Alert>{error}</Alert>}
        <Field label="Password">
          <Input data-autofocus type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </Field>
        {info.mfa && (
          <Field label={useRecovery ? "Recovery code" : "Authenticator code"}
            hint={useRecovery ? "Each recovery code works once." : "The 6-digit code your authenticator app shows now."}>
            <Input value={code} onChange={(e) => setCode(e.target.value)} required
              autoComplete="one-time-code" inputMode={useRecovery ? "text" : "numeric"} />
          </Field>
        )}
        {info.mfa && (
          <button type="button" className="text-sm text-accent underline" onClick={() => { setUseRecovery(!useRecovery); setCode(""); }}>
            {useRecovery ? "Use a code from the authenticator" : "Use a recovery code instead"}
          </button>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={() => onDone(false)}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={busy}>{busy ? "Checking…" : "Confirm"}</Button>
        </div>
      </form>
    </Dialog>
  );
}
