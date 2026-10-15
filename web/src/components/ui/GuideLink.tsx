import type { ReactNode } from "react";
import { plainLink } from "./links";

/** DROOL's user guide (the GitHub wiki, published from docs/wiki). */
export const guideBase = "https://github.com/irnutsmurt/drool/wiki";

/** A link to a user guide page (and anchor), opened in a new tab: where the
 * UI's hints point for step-by-step help (not the README, the project's
 * front door). */
export function GuideLink({ page, anchor, children }: { page: string; anchor?: string; children: ReactNode }) {
  return (
    <a className={plainLink} href={`${guideBase}/${page}${anchor ? `#${anchor}` : ""}`} target="_blank" rel="noreferrer">{children}</a>
  );
}
