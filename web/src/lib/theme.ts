import { useSyncExternalStore } from "react";

/** The user's appearance choice. "system" follows the OS/browser setting live. */
export type ThemeChoice = "light" | "dark" | "system";
export type Resolved = "light" | "dark";

const KEY = "drool.theme";
const media = () => window.matchMedia("(prefers-color-scheme: dark)");

export function storedTheme(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    /* storage unavailable: fall back to the system setting */
  }
  return "system";
}

const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());

/** Applies and persists a choice; no reload needed (CSS reads data-theme). */
export function setTheme(t: ThemeChoice) {
  try {
    localStorage.setItem(KEY, t);
  } catch {
    /* not persisted (private mode): still applies for this page */
  }
  document.documentElement.dataset.theme = t;
  notify();
}

export function resolveTheme(t: ThemeChoice = storedTheme()): Resolved {
  if (t === "system") return media().matches ? "dark" : "light";
  return t;
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  const m = media();
  m.addEventListener("change", cb);
  // Another tab changed the choice.
  const onStorage = (e: StorageEvent) => {
    if (e.key === KEY) {
      document.documentElement.dataset.theme = storedTheme();
      cb();
    }
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(cb);
    m.removeEventListener("change", cb);
    window.removeEventListener("storage", onStorage);
  };
}

/** The current choice and what it resolves to (re-renders on changes). */
export function useTheme(): { choice: ThemeChoice; resolved: Resolved; setTheme: (t: ThemeChoice) => void } {
  const choice = useSyncExternalStore(subscribe, storedTheme, () => "system" as ThemeChoice);
  const resolved = useSyncExternalStore(subscribe, () => resolveTheme(choice), () => "light" as Resolved);
  return { choice, resolved, setTheme };
}
