import { useEffect, useRef, useState, type CSSProperties } from "react";

/** A row that scrolls sideways (tabs on a phone) shows that there is more:
 * the edge with more fades out. The current entry ([aria-current]) is
 * scrolled into view, so the row never hides where you are. */
export function useScrollCue<T extends HTMLElement>(): { ref: React.RefObject<T | null>; style: CSSProperties } {
  const ref = useRef<T>(null);
  const [cue, setCue] = useState({ start: false, end: false });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => setCue({ start: el.scrollLeft > 1, end: el.scrollLeft + el.clientWidth < el.scrollWidth - 1 });
    const cur = el.querySelector<HTMLElement>('[aria-current="page"]');
    if (cur && el.scrollWidth > el.clientWidth) {
      const r = cur.getBoundingClientRect(), box = el.getBoundingClientRect();
      if (r.right > box.right || r.left < box.left) el.scrollLeft += r.left - box.left - (box.width - r.width) / 2;
    }
    update();
    el.addEventListener("scroll", update, { passive: true });
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    ro?.observe(el);
    return () => {
      el.removeEventListener("scroll", update);
      ro?.disconnect();
    };
  }, []);
  if (!cue.start && !cue.end) return { ref, style: {} };
  const mask = `linear-gradient(to right, ${cue.start ? "transparent 0, #000 2rem" : "#000 0"}, ${cue.end ? "#000 calc(100% - 2rem), transparent 100%" : "#000 100%"})`;
  return { ref, style: { maskImage: mask, WebkitMaskImage: mask } };
}
