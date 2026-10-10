/**
 * Navigation search: finds pages and settings by their name, the
 * conventional words people use for them ("group" for Packs, "agent" for
 * Shepherds, "job" for Runs) and the questions they answer ("can A reach
 * B"). It searches destinations, not data: no hosts, jobs or records.
 */
export interface Destination {
  id: string;
  title: string;
  /** The sidebar heading it belongs under. */
  section: string;
  to: string;
  description: string;
  /** Other words for it, lower case. */
  aliases: string[];
  /** The conventional term of a themed title. */
  hint?: string;
  /** Every permission needed to view it. */
  permissions?: string[];
}

export interface SearchResult {
  dest: Destination;
  score: number;
  /** The alias that matched, when it wasn't the title. */
  via?: string;
}

const norm = (s: string) => s.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, " ").trim();

/** How well `field` matches the normalized query (0: not at all). */
function fieldScore(field: string, q: string, tokens: string[]): number {
  const f = norm(field);
  if (!f) return 0;
  if (f === q) return 100;
  if (f.startsWith(q)) return 90 - Math.min(10, f.length - q.length) / 2;
  const words = f.split(" ");
  if (words.some((_, i) => words.slice(i).join(" ").startsWith(q))) return 75;
  if (f.includes(q)) return 55;
  if (tokens.length > 1 && tokens.every((t) => words.some((w) => w.startsWith(t)))) return 50;
  return 0;
}

/** Destinations the user may view, best match first; all of them, in
 * order, for an empty query. */
export function searchDestinations(query: string, all: Destination[], can: (p: string) => boolean): SearchResult[] {
  const allowed = all.filter((d) => (d.permissions ?? []).every((p) => can(p)));
  const q = norm(query);
  if (!q) return allowed.map((dest) => ({ dest, score: 0 }));
  const tokens = q.split(" ");
  const out: SearchResult[] = [];
  for (const dest of allowed) {
    let best = fieldScore(dest.title, q, tokens);
    let via: string | undefined;
    for (const a of [...(dest.hint ? [dest.hint] : []), ...dest.aliases]) {
      // An alias is a near-synonym: slightly behind the title itself.
      const s = fieldScore(a, q, tokens) * 0.97;
      if (s > best) {
        best = s;
        via = a;
      }
    }
    // The sidebar heading ("settings", "protect") lists its destinations;
    // the description only helps when nothing else matched.
    const sec = fieldScore(dest.section, q, tokens) * 0.6;
    if (sec > best) {
      best = sec;
      via = undefined;
    }
    const d = fieldScore(dest.description, q, tokens) * 0.4;
    if (d > best) {
      best = d;
      via = undefined;
    }
    if (best > 0) out.push({ dest, score: best, via });
  }
  // Stable: equal scores keep the catalogue's order.
  return out.map((r, i) => ({ r, i })).sort((a, b) => b.r.score - a.r.score || a.i - b.i).map((x) => x.r);
}
