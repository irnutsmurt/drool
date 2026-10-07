/** Unix permissions in plain language (users shouldn't need to know octal). */

const who = ["Owner", "Group", "Everyone else"] as const;

/** "rwxr-xr-x" (with s/t for the special bits). */
export function symbolic(mode: number): string {
  const bits = ["r", "w", "x"];
  let out = "";
  for (let i = 0; i < 3; i++) {
    const v = (mode >> ((2 - i) * 3)) & 7;
    out += bits.map((b, j) => (v & (4 >> j) ? b : "-")).join("");
  }
  const chars = out.split("");
  if (mode & 0o4000) chars[2] = chars[2] === "x" ? "s" : "S";
  if (mode & 0o2000) chars[5] = chars[5] === "x" ? "s" : "S";
  if (mode & 0o1000) chars[8] = chars[8] === "x" ? "t" : "T";
  return chars.join("");
}

export function octal(mode: number): string {
  return (mode & 0o7777).toString(8).padStart(4, "0");
}

function rights(v: number, dir: boolean): string {
  const r: string[] = [];
  if (v & 4) r.push(dir ? "list" : "read");
  if (v & 2) r.push(dir ? "add/remove files" : "write");
  if (v & 1) r.push(dir ? "enter" : "run");
  return r.length ? r.join(", ") : "no access";
}

/** "Owner: read, write · Group: read · Everyone else: read". */
export function describe(mode: number, dir = false): string {
  const parts = who.map((w, i) => `${w}: ${rights((mode >> ((2 - i) * 3)) & 7, dir)}`);
  if (mode & 0o4000) parts.push(dir ? "setuid" : "runs as its owner (setuid)");
  if (mode & 0o2000) parts.push(dir ? "new files get the directory's group (setgid)" : "runs with its group (setgid)");
  if (mode & 0o1000) parts.push(dir ? "only owners can delete their files (sticky)" : "sticky");
  return parts.join(" · ");
}

/**
 * Short summary for tables, e.g. "owner: read, write · group & others: read" or
 * "only the owner: read, write". Groups with the same rights are merged.
 */
export function brief(mode: number, dir = false): string {
  const [o, g, x] = [6, 3, 0].map((sh) => (mode >> sh) & 7) as [number, number, number];
  const word = (v: number) => {
    const w: string[] = [];
    if (v & 4) w.push(dir ? "list" : "read");
    if (v & 2) w.push(dir ? "change" : "write");
    if (v & 1) w.push(dir ? "enter" : "run");
    return w.join(", ");
  };
  const parts: string[] = [];
  if (g === 0 && x === 0) parts.push(o ? `only the owner: ${word(o)}` : "no access");
  else if (g === x) parts.push(`owner: ${word(o) || "none"}`, `group & others: ${word(g)}`);
  else {
    parts.push(`owner: ${word(o) || "none"}`);
    parts.push(`group: ${word(g) || "none"}`);
    parts.push(`others: ${word(x) || "none"}`);
  }
  let s = parts.join(" · ");
  if (mode & 0o4000) s += " · setuid";
  if (mode & 0o2000) s += " · setgid";
  if (mode & 0o1000) s += " · sticky";
  return s;
}

export function parseOctal(s: string): number | null {
  const t = s.trim();
  if (!/^[0-7]{1,4}$/.test(t)) return null;
  return parseInt(t, 8);
}
