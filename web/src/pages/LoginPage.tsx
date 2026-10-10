import { useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router";
import { useAuth, useSetupStatus } from "@/auth/AuthContext";
import { Button } from "@/components/ui/Button";
import { Field, Input } from "@/components/ui/Field";
import { Alert } from "@/components/ui/Alert";
import { errorMessage } from "@/lib/format";
import { ApiError } from "@/api/client";

export function LoginPage() {
  const { me, login, completeLogin } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from ?? "/";
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  // Two-factor step (M16): the password was right, a code is next.
  const [challenge, setChallenge] = useState<string>();
  const [code, setCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const setup = useSetupStatus();

  if (me) return <Navigate to={from} replace />;
  if (setup.data?.setup_required) return <Navigate to="/setup" replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (challenge) {
        await completeLogin(challenge, useRecovery ? { recovery_code: code } : { code });
        navigate(from, { replace: true });
        return;
      }
      const next = await login(username, password);
      if (next) {
        setChallenge(next.challenge);
        setPassword("");
      } else {
        navigate(from, { replace: true });
      }
    } catch (err) {
      if (err instanceof ApiError && err.problem?.code === "challenge_expired") {
        // Start over: the attempt expired or ran out of tries.
        setChallenge(undefined);
        setCode("");
      }
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex h-full items-center justify-center p-4">
      <form onSubmit={submit} className="w-full max-w-sm space-y-4 rounded-ui border border-line bg-surface p-6">
        <h1 className="text-lg font-semibold">Sign in to DROOL</h1>
        {error && <Alert>{error}</Alert>}
        {!challenge ? (
          <>
            <Field label="Username">
              <Input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
            </Field>
            <Field label="Password">
              <Input type="password" autoComplete="current-password" value={password}
                onChange={(e) => setPassword(e.target.value)} required />
            </Field>
            <Button type="submit" variant="primary" className="w-full" disabled={busy}>
              {busy ? "Signing in…" : "Sign in"}
            </Button>
          </>
        ) : (
          <>
            <p className="text-sm text-muted">
              {useRecovery
                ? <>Enter one of the recovery codes you saved when you set up two-factor authentication. Each works once.</>
                : <>Enter the 6-digit code from your authenticator app for <span className="font-medium text-fg">{username}</span>.</>}
            </p>
            <Field label={useRecovery ? "Recovery code" : "Authenticator code"}>
              <Input autoFocus value={code} onChange={(e) => setCode(e.target.value)} required
                autoComplete="one-time-code" inputMode={useRecovery ? "text" : "numeric"} />
            </Field>
            <Button type="submit" variant="primary" className="w-full" disabled={busy}>
              {busy ? "Checking…" : "Sign in"}
            </Button>
            <div className="flex flex-wrap justify-between gap-2 text-sm">
              <button type="button" className="text-accent underline" onClick={() => { setUseRecovery(!useRecovery); setCode(""); }}>
                {useRecovery ? "Use the authenticator" : "Use a recovery code"}
              </button>
              <button type="button" className="text-muted underline" onClick={() => { setChallenge(undefined); setCode(""); setError(undefined); }}>
                Start over
              </button>
            </div>
          </>
        )}
      </form>
    </div>
  );
}
