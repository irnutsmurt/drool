import { useSyncExternalStore } from "react";

/** Whether a CSS media query matches now (re-renders when it changes). */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia(query);
      m.addEventListener("change", cb);
      return () => m.removeEventListener("change", cb);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}

/** Below the desktop layout (the sidebar becomes a menu). */
export const narrowQuery = "(max-width: 1023.98px)";
