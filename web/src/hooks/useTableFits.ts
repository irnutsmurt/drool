import { useLayoutEffect, useRef, useState } from "react";

/**
 * Whether a wide table fits the width it has (a 1024 px window beside the
 * sidebar may not): a page with a compact list for phones shows that list
 * instead of a table that would scroll sideways. The table's width is
 * measured while it is shown; while the list is up, the width the table
 * needed decides, so more room brings the table back. Put `outer` on an
 * element as wide as the table could be, `inner` on the table's scroll box.
 */
export function useTableFits() {
  const [outer, setOuter] = useState<HTMLDivElement | null>(null);
  const inner = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [needed, setNeeded] = useState(0);
  useLayoutEffect(() => {
    if (!outer) return;
    const ro = new ResizeObserver(() => setWidth(outer.clientWidth));
    ro.observe(outer);
    setWidth(outer.clientWidth);
    return () => ro.disconnect();
  }, [outer]);
  useLayoutEffect(() => {
    const el = inner.current;
    if (el && el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 1 && el.scrollWidth !== needed) setNeeded(el.scrollWidth);
  });
  return { outer: setOuter, inner, fits: needed === 0 || width >= needed };
}
