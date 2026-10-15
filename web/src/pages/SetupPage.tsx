import { useState, type FormEvent } from "react";
import { Navigate, useNavigate } from "react-router";
import { useAuth, useSetupStatus } from "@/auth/AuthContext";
import { Button } from "@/components/ui/Button";
import { Field, Input } from "@/components/ui/Field";
import { Alert } from "@/components/ui/Alert";
import { GuideLink } from "@/components/ui/GuideLink";
import { errorMessage } from "@/lib/format";

/** One-time first-admin setup using the code printed in the controller's logs. */
export function SetupPage() {
  const { me, claim } = useAuth();
  const status = useSetupStatus();
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  if (me) return <Navigate to="/" replace />;
  if (status.isPending) return <div className="p-8 text-muted">Loading…</div>;
  if (status.data && !status.data.setup_required) return <Navigate to="/login" replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(undefined);
    if (password !== confirm) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await claim(code, username, password);
      navigate("/", { replace: true });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex h-full items-center justify-center p-4">
      <form onSubmit={submit} className="w-full max-w-sm space-y-4 rounded-ui border border-line bg-surface p-6">
        <div>
          <h1 className="text-lg font-semibold">Set up DROOL</h1>
          <p className="mt-1 text-sm text-muted">
            Enter the setup code from the controller's logs and create the administrator account.
          </p>
        </div>
        {status.data?.data_volume && (
          <Alert tone="warn">
            <span data-testid="setup-new-volume">
              This DROOL has no data yet: it starts new in the volume <code>{status.data.data_volume}</code>. Expected your existing
              DROOL? Don&apos;t set up a new one: its data is in another volume.{" "}
              <GuideLink page="Compose-Data-Volume">How to find it</GuideLink>
            </span>
          </Alert>
        )}
        {status.data && !status.data.code_available && (
          <Alert tone="warn">
            No valid setup code. Restart the controller or run <code>drool setup code</code> to get a new one.
          </Alert>
        )}
        {error && <Alert>{error}</Alert>}
        <Field label="Setup code">
          <Input autoFocus autoComplete="one-time-code" inputMode="text" value={code}
            onChange={(e) => setCode(e.target.value)} required />
        </Field>
        <Field label="Admin username">
          <Input autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </Field>
        <Field label="Password" hint="At least 12 characters.">
          <Input type="password" autoComplete="new-password" minLength={12} value={password}
            onChange={(e) => setPassword(e.target.value)} required />
        </Field>
        <Field label="Confirm password">
          <Input type="password" autoComplete="new-password" value={confirm}
            onChange={(e) => setConfirm(e.target.value)} required />
        </Field>
        <Button type="submit" variant="primary" className="w-full" disabled={busy}>
          {busy ? "Creating…" : "Create administrator"}
        </Button>
      </form>
    </div>
  );
}
