import { useEffect, useState, type ReactNode } from "react";
import { errorMessage } from "@/lib/format";
import { Alert } from "./Alert";
import { Button } from "./Button";
import { Dialog } from "./Dialog";

/** A consequential action, confirmed (Pattern B): the title names the
 * action and its target, the body says plainly what will happen, Cancel
 * and the primary action are always in sight (the body scrolls), and an
 * error is shown where the action was asked for. The server still checks
 * everything (permissions, re-authentication, locks): this dialog is never
 * the only safety check. */
export function ConfirmDialog({ title, children, confirmLabel, danger, busy, error, onConfirm, onClose, wide, testId }: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  error?: unknown;
  onConfirm: () => void;
  onClose: () => void;
  wide?: boolean;
  testId?: string;
}) {
  return (
    <Dialog title={title} onClose={onClose} wide={wide} testId={testId ?? "confirm-dialog"}
      footer={
        <>
          <Button onClick={onClose} data-autofocus>Cancel</Button>
          <Button variant={danger ? "danger" : "primary"} disabled={busy} onClick={onConfirm}>{busy ? "Working…" : confirmLabel}</Button>
        </>
      }>
      <div className="space-y-3 text-sm">
        {children}
        {error != null && <Alert>{errorMessage(error)}</Alert>}
      </div>
    </Dialog>
  );
}

export interface ConfirmOptions {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  wide?: boolean;
}

type Pending = ConfirmOptions & { resolve: (ok: boolean) => void };
let show: ((p: Pending) => void) | null = null;

/** Asks before a consequential action, in DROOL's own dialog (not the
 * browser's confirm()): resolves true when confirmed. Needs <ConfirmHost/>
 * mounted (the app shell does). */
export function confirmAction(opts: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => {
    if (show) show({ ...opts, resolve });
    else resolve(false); // no host: never act without asking
  });
}

/** Where confirmAction()'s dialogs render. */
export function ConfirmHost() {
  const [p, setP] = useState<Pending | null>(null);
  useEffect(() => {
    show = (next) => setP((cur) => {
      cur?.resolve(false); // one question at a time
      return next;
    });
    return () => {
      show = null;
    };
  }, []);
  if (!p) return null;
  const done = (ok: boolean) => {
    p.resolve(ok);
    setP(null);
  };
  return (
    <ConfirmDialog title={p.title} confirmLabel={p.confirmLabel} danger={p.danger} wide={p.wide} onConfirm={() => done(true)} onClose={() => done(false)}>
      {p.body}
    </ConfirmDialog>
  );
}
