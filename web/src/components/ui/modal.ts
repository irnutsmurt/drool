import { useLayoutEffect, type RefObject } from "react";

/** Open modal layers (dialogs, the command palette), in the order they
 * opened: the last one is on top. Stacking, which layer answers the keyboard
 * and its backdrop, and what is inert are all decided from this order, never
 * from where a layer sits in the DOM. (They used to share one z-index, so a
 * dialog opened from another one — the re-authentication prompt, mounted
 * before the router — could paint underneath it, with focus inside the
 * hidden one.) Layers render into document.body, outside #root, which is
 * inert while any layer is open; layers below the top one are inert too. */
const layers: HTMLElement[] = [];

/** The bottom layer's z-index: above the shell's own overlays (menus, the
 * phone navigation drawer, tooltips). Each layer above adds one. */
export const MODAL_BASE_Z = 100;

function apply() {
  layers.forEach((el, i) => {
    el.style.zIndex = String(MODAL_BASE_Z + i);
    el.inert = i !== layers.length - 1;
  });
  const root = document.getElementById("root");
  if (root) root.inert = layers.length > 0;
}

/** Registers a layer on top; the returned function removes it. */
export function pushLayer(el: HTMLElement): () => void {
  layers.push(el);
  apply();
  return () => {
    const i = layers.indexOf(el);
    if (i >= 0) layers.splice(i, 1);
    apply();
  };
}

/** Whether el is the topmost open layer (the only one that reacts). */
export function isTopLayer(el: HTMLElement | null): boolean {
  return el !== null && layers[layers.length - 1] === el;
}

/** How many layers are open (tests). */
export function openLayers(): number {
  return layers.length;
}

function topLayer(): HTMLElement | undefined {
  return layers[layers.length - 1];
}

/** Makes ref's element a modal layer while mounted: on top of every open
 * layer, focus moved into it (focusFirst picks where), and focus given back
 * to where it was when it closes — after the layer below is interactive
 * again, so that works for nested layers too. When that element can't take
 * focus (a button disabled while its request waits for the prompt), focus
 * goes to the dialog now on top instead of being lost. */
export function useModalLayer(ref: RefObject<HTMLElement | null>, focusFirst: () => HTMLElement | null | undefined) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const pop = pushLayer(el);
    focusFirst()?.focus();
    return () => {
      pop();
      if (before?.isConnected) before.focus();
      const top = topLayer();
      if (top && document.activeElement !== before) (top.querySelector<HTMLElement>('[role="dialog"]') ?? top).focus();
    };
  }, []); // once per mount; focusFirst reads refs
}
