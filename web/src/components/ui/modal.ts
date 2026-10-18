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

/** A control disabled while it has the focus — a button waiting for its own
 * request, a field while it saves — loses it: browsers move the focus to the
 * document and don't give it back when the control is enabled again
 * (measured in Chromium and Firefox), so the focus ring disappears and a
 * screen reader loses its place. Such a control is parked here, and gets
 * the focus back when it is enabled again, if the focus hasn't gone
 * anywhere else meanwhile: it is still on the document, or on the holder it
 * was put on (the main content, after a dialog closed). */
let parked: { el: HTMLElement; holder: Element | null } | null = null;

const isDisabled = (el: Element) => (el as HTMLButtonElement).disabled === true;

/** Makes ref's element a modal layer while mounted: on top of every open
 * layer, focus moved into it (focusFirst picks where), and focus given back
 * to where it was when it closes — after the layer below is interactive
 * again, so that works for nested layers too. When that element can't take
 * focus (a button disabled while its request waits for the prompt), focus
 * goes to the dialog now on top instead of being lost, or to the page's
 * main content and back to the element once it is enabled again; when it
 * is gone (the row a confirmed Delete removed), to the page's main content,
 * so the next Tab continues there rather than at the top of the document. */
export function useModalLayer(ref: RefObject<HTMLElement | null>, focusFirst: () => HTMLElement | null | undefined) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Opened while the control that led here waits, disabled, for its
    // request (a dialog its success opens): focus returns to that control.
    const at = document.activeElement;
    const before = at instanceof HTMLElement && at !== document.body ? at : (parked?.el ?? null);
    const pop = pushLayer(el);
    focusFirst()?.focus();
    return () => {
      pop();
      if (before?.isConnected) before.focus();
      const top = topLayer();
      if (top && document.activeElement !== before) (top.querySelector<HTMLElement>('[role="dialog"]') ?? top).focus();
      else if (!top && document.activeElement !== before) {
        const main = document.getElementById("main");
        main?.focus({ preventScroll: true });
        if (before?.isConnected && isDisabled(before)) parked = { el: before, holder: main };
      }
    };
  }, []); // once per mount; focusFirst reads refs
}

/** When what has focus disappears — the row a confirmed removal took away
 * once the list refreshed, a button replaced by its result — focus goes to
 * the dialog on top if one is open, else to the page's main content: never
 * to the document, where the next Tab would start again at the top. A
 * control disabled under the focus is parked (above) and gets it back when
 * it is enabled again. */
export function watchFocusLoss(): () => void {
  let last: Element | null = null;
  const onIn = (e: FocusEvent) => {
    last = e.target as Element;
    if (parked && e.target !== parked.holder) parked = null; // the focus moved on
  };
  const onOut = (e: FocusEvent) => {
    // Disabled under the focus: the browser moves it to the document.
    if (e.target instanceof HTMLElement && e.relatedTarget === null && isDisabled(e.target)) parked = { el: e.target, holder: null };
  };
  const onPointer = () => { parked = null; }; // a click elsewhere decides where focus goes
  document.addEventListener("focusin", onIn);
  document.addEventListener("focusout", onOut);
  document.addEventListener("pointerdown", onPointer, true);
  const mo = new MutationObserver(() => {
    const p = parked;
    if (p && !p.el.isConnected) parked = null;
    else if (p && !isDisabled(p.el)) {
      const at = document.activeElement;
      parked = null;
      if ((at === null || at === document.body || at === p.holder) && !p.el.closest("[inert]")) {
        p.el.focus({ preventScroll: true });
        return;
      }
    }
    if (!last || last.isConnected || (document.activeElement && document.activeElement !== document.body)) return;
    last = null;
    const top = topLayer();
    const to = top ? (top.querySelector<HTMLElement>('[role="dialog"]') ?? top) : document.getElementById("main");
    to?.focus({ preventScroll: true });
  });
  mo.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["disabled"] });
  return () => {
    document.removeEventListener("focusin", onIn);
    document.removeEventListener("focusout", onOut);
    document.removeEventListener("pointerdown", onPointer, true);
    mo.disconnect();
  };
}
