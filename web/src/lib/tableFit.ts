/**
 * Cards when a table doesn't fit. Every table.rtable in the page — the
 * Table component's and the pages' own — shows as a stack of cards
 * (data-cards, styled in index.css) on a phone, and wherever its columns
 * need more width than it has: a column or a row's actions never sit
 * behind a sideways scroll. Decided per table as it appears and whenever
 * the space around it changes (a window resized, a side panel opened, rows
 * loaded), by laying it out as a table and measuring.
 */
const PHONE = "(max-width: 767.98px)";

/** Cards on a phone, or when the table's content is wider than its box. */
export function cardsNeeded(contentWidth: number, room: number, phone: boolean): boolean {
  return phone || contentWidth > room + 1;
}

export function watchTableFit(root: HTMLElement = document.body): () => void {
  const phone = window.matchMedia(PHONE);
  const tables = new Set<HTMLTableElement>();
  const pending = new Set<HTMLTableElement>();
  let frame = 0;

  const decide = (t: HTMLTableElement) => {
    const box = t.parentElement;
    if (!box || !t.isConnected) return;
    if (!phone.matches && box.clientWidth === 0) return; // not shown yet
    let cards = phone.matches;
    if (!cards) {
      // Its width as a table: lay it out as one, then measure what its box
      // would have to scroll (a cell's content can overflow a table that
      // itself fits).
      const was = t.hasAttribute("data-cards");
      if (was) t.removeAttribute("data-cards");
      cards = cardsNeeded(Math.max(t.offsetWidth, box.scrollWidth), box.clientWidth, false);
      if (was && cards) t.setAttribute("data-cards", "");
    }
    if (cards !== t.hasAttribute("data-cards")) t.toggleAttribute("data-cards", cards);
  };
  // Measuring changes sizes: never inside the observer's own callback.
  const later = (t: HTMLTableElement) => {
    pending.add(t);
    if (!frame) {
      frame = requestAnimationFrame(() => {
        frame = 0;
        const now = [...pending];
        pending.clear();
        now.forEach(decide);
      });
    }
  };
  // The space around the table, and the table itself (wider content).
  const ro = new ResizeObserver((entries) => {
    for (const e of entries) {
      for (const t of tables) if (t === e.target || t.parentElement === e.target) later(t);
    }
  });
  const add = (t: HTMLTableElement) => {
    if (tables.has(t)) return;
    tables.add(t);
    ro.observe(t);
    if (t.parentElement) ro.observe(t.parentElement);
    decide(t);
  };
  const scan = (n: ParentNode) => n.querySelectorAll<HTMLTableElement>("table.rtable").forEach(add);
  const mo = new MutationObserver((records) => {
    for (const r of records) {
      for (const n of r.addedNodes) {
        if (n instanceof HTMLTableElement && n.classList.contains("rtable")) add(n);
        else if (n instanceof Element) scan(n);
      }
    }
    for (const t of tables) {
      if (!t.isConnected) {
        tables.delete(t);
        ro.unobserve(t);
        if (t.parentElement) ro.unobserve(t.parentElement);
      }
    }
  });
  mo.observe(root, { childList: true, subtree: true });
  scan(root);
  const all = () => tables.forEach(decide);
  phone.addEventListener("change", all);
  return () => {
    mo.disconnect();
    ro.disconnect();
    phone.removeEventListener("change", all);
    if (frame) cancelAnimationFrame(frame);
  };
}
