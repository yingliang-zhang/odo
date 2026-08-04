import { useEffect, type RefObject } from "react";

// Belt D: focusable candidates inside a modal. Disabled controls are
// unreachable via Tab anyway; the explicit list keeps the trap off hidden
// element classes the dialogs never use.
const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function visibleFocusables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    // offsetParent goes null for display:none and (in non-fixed trees) for
    // elements inside a closed <details> — neither is tabbable.
    (el) => el.offsetParent !== null,
  );
}

// Modal focus trap: on mount, focus moves to the first focusable element
// (or the container itself when nothing is focusable yet, e.g. a loading
// state); Tab/Shift+Tab cycle within the container; on unmount, focus
// returns to the element that opened the dialog.
//
// Escape is deliberately NOT handled here — each modal already owns its
// window-level Escape listener.
export function useFocusTrap(ref: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const root = ref.current;
    if (!root) return;
    const previous = document.activeElement as HTMLElement | null;

    const initial = visibleFocusables(root);
    (initial[0] ?? root).focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      const items = visibleFocusables(root);
      if (items.length === 0) {
        e.preventDefault();
        return;
      }
      const active = document.activeElement as HTMLElement | null;
      const idx = active === null ? -1 : items.indexOf(active);
      // idx < 0 covers the container itself and any focus that escaped to
      // the page behind the modal: pull it back to the appropriate edge.
      if (e.shiftKey) {
        if (idx <= 0) {
          e.preventDefault();
          items[items.length - 1].focus();
        }
      } else if (idx < 0 || idx === items.length - 1) {
        e.preventDefault();
        items[0].focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      // The trigger may have unmounted meanwhile (focus() on a detached
      // element is a no-op), so this is safe unconditionally.
      previous?.focus();
    };
  }, [ref]);
}
