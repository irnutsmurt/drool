import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { Field, Input } from "@/components/ui/Field";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { errorMessage } from "@/lib/format";
import { ThemeSwitcher } from "@/components/ui/ThemeSwitcher";
import { TwoFactorCard } from "@/components/auth/TwoFactorCard";

export function AccountPage() {
  const { me } = useAuth();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [localError, setLocalError] = useState<string>();
  const mut = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/password", { body: { current_password: current, new_password: next } })),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setConfirm("");
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setLocalError(undefined);
    if (next !== confirm) {
      setLocalError("New passwords do not match.");
      return;
    }
    mut.mutate();
  }

  return (
    <div className="grid max-w-xl gap-4">
      <Card title="Profile">
        <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[8rem_1fr] gap-y-2 text-sm">
          <dt className="text-muted">Username</dt>
          <dd>{me?.user.username}</dd>
          <dt className="text-muted">Role</dt>
          <dd>{me?.user.role}</dd>
        </dl>
      </Card>
      <Card title="Appearance">
        <ThemeSwitcher />
        <p className="mt-2 text-xs text-muted">Saved in this browser. System follows your operating system's light or dark setting and changes with it.</p>
      </Card>
      {me?.auth_kind === "session" && <TwoFactorCard />}
      <Card title="Change password">
        <form onSubmit={submit} className="space-y-4">
          {(localError || mut.error) && <Alert>{localError ?? errorMessage(mut.error)}</Alert>}
          {mut.isSuccess && <Alert tone="ok">Password changed. Your other sessions were signed out.</Alert>}
          <Field label="Current password">
            <Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
          </Field>
          <Field label="New password" hint="At least 12 characters.">
            <Input type="password" autoComplete="new-password" minLength={12} value={next} onChange={(e) => setNext(e.target.value)} required />
          </Field>
          <Field label="Confirm new password">
            <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
          </Field>
          <Button type="submit" variant="primary" disabled={mut.isPending}>Change password</Button>
        </form>
      </Card>
    </div>
  );
}
