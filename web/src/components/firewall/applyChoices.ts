import type { FwPlan } from "./queries";

// What review and apply does to each host, from the host's mode and the
// mode chosen for it in the review. Only Enforce applies the policy, and a
// host is switched to Enforce only where Enforce was chosen for it: never
// by default, never silently. Choosing Off, Monitor or Guided changes the
// mode and enforces nothing; keeping the mode leaves the host as it is.

export type Mode = FwPlan["config"]["mode"];
/** "keep" leaves the host's mode as it is. */
export type ModeChoice = "keep" | Mode;

export type Decision =
  /** In the run: the saved policy is applied (after switching the host to
   * Enforce when switch is set). */
  | { kind: "apply"; switch: boolean }
  /** Only the mode changes: nothing is applied. */
  | { kind: "mode"; to: Mode }
  /** Nothing happens on this host, and why. */
  | { kind: "none"; why: string };

/** One host's decision. refused is why the server would refuse applying
 * there now (or null). */
export function decide(p: FwPlan, choice: ModeChoice | undefined, refused: string | null): Decision {
  const current = p.config.mode;
  if (current === "enforce") return refused ? { kind: "none", why: refused } : { kind: "apply", switch: false };
  if (!choice || choice === "keep" || choice === current) return { kind: "none", why: `its mode stays ${current}: the policy isn't applied` };
  if (choice === "enforce") return refused ? { kind: "none", why: refused } : { kind: "apply", switch: true };
  return { kind: "mode", to: choice };
}

/** The run a review starts: the hosts applied to (and which of them switch
 * to Enforce), the mode-only changes, and the hosts left as they are. */
export function plan(plans: FwPlan[], decisions: Decision[]) {
  const apply: FwPlan[] = [];
  const switching: FwPlan[] = [];
  const modes: { p: FwPlan; to: Mode }[] = [];
  const untouched: FwPlan[] = [];
  plans.forEach((p, i) => {
    const d = decisions[i]!;
    if (d.kind === "apply") {
      apply.push(p);
      if (d.switch) switching.push(p);
    } else if (d.kind === "mode") modes.push({ p, to: d.to });
    else untouched.push(p);
  });
  return { apply, switching, modes, untouched };
}

/** The confirming button's words: exactly what it starts. */
export function actionLabel(r: ReturnType<typeof plan>, modeLabel: (m: Mode) => string): string {
  const n = r.apply.length;
  const hosts = (k: number) => `${k} host${k === 1 ? "" : "s"}`;
  const applyPart = n === 0 ? ""
    : r.switching.length > 0 ? (n === 1 ? "Switch to Enforce and apply" : `Switch to Enforce and apply to ${hosts(n)}`)
      : n === 1 ? "Apply policy" : `Apply to ${hosts(n)}`;
  const m = r.modes.length;
  const modePart = m === 0 ? ""
    : m === 1 ? `change ${r.modes[0]!.p.host_name}'s mode to ${modeLabel(r.modes[0]!.to)}` : `change the mode of ${hosts(m)}`;
  if (applyPart && modePart) return `${applyPart} and ${modePart}`;
  if (modePart) return modePart[0]!.toUpperCase() + modePart.slice(1);
  return applyPart || "Apply policy";
}
