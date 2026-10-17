import { useEffect, useRef, useState } from "react";
import { useLocation } from "react-router";

/** The page's name from what it shows: its heading, a host workspace's
 * section and the view picked in the page's own navigation, most specific
 * first ("Metrics – Monitoring – web-01"). Null until the heading is there. */
export function titleOf(main: ParentNode): string | null {
  const h1 = main.querySelector("h1")?.textContent?.trim();
  if (!h1) return null;
  const parts = [h1];
  const section = main.querySelector('[data-testid="host-sections"] a[aria-current="page"]')?.textContent?.trim();
  if (section && section !== "Overview") parts.unshift(section);
  const view = [...main.querySelectorAll<HTMLElement>('nav a[aria-current="page"]')]
    .find((a) => !a.closest('[data-testid="host-sections"], [aria-label="Breadcrumb"]'));
  if (view) {
    // A tab's count ("Open 4") isn't part of its name.
    const name = [...view.childNodes].filter((n) => !(n instanceof HTMLElement && n.dataset.count !== undefined)).map((n) => n.textContent).join("").trim();
    if (name && !parts.includes(name)) parts.unshift(name);
  }
  return parts.join(" – ");
}

/** Every page has a title (the browser tab, history, the first thing a
 * screen reader says), kept in step with the page as it loads. Moving
 * inside DROOL leaves focus where it was — on the link just used — so the
 * new page's name is also said once, politely; a filter or a refresh that
 * doesn't change the name says nothing. */
export function PageTitle() {
  const { pathname, search } = useLocation();
  const [said, setSaid] = useState("");
  const last = useRef<string | null>(null);
  useEffect(() => {
    const main = document.getElementById("main");
    if (!main) return;
    let timer: number | undefined;
    const update = () => {
      const t = titleOf(main);
      if (!t) return;
      const full = `${t} – DROOL`;
      if (document.title !== full) document.title = full;
      // The first page is announced by the browser itself.
      if (last.current !== null && last.current !== t) setSaid(t);
      last.current = t;
    };
    const obs = new MutationObserver(() => {
      window.clearTimeout(timer);
      timer = window.setTimeout(update, 150);
    });
    obs.observe(main, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ["aria-current"] });
    timer = window.setTimeout(update, 50);
    return () => {
      obs.disconnect();
      window.clearTimeout(timer);
    };
  }, [pathname, search]);
  return <div className="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="page-announcer">{said}</div>;
}
