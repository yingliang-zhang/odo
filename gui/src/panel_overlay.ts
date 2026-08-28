import { useLayoutEffect, useState } from "react";
import type { RefObject } from "react";

// U2 (docs/design/ui-layout-lock.md §U2) — context-panel geometry.
// U2.1: the docked↔overlay decision is driven by the REAL chat width
// (ResizeObserver on .app-main), with a 40px hysteresis band so a width
// hovering near the threshold cannot flip the panel posture back and
// forth. U2.2/U2.3: drag width persists in localStorage (clamped on
// read), default 420px. All decision logic lives here as pure functions
// (test seam, same pattern as StatusBar's computeHiddenChipKeys); only
// the ResizeObserver glue touches the DOM.

export const PANEL_MIN_WIDTH = 280;
export const PANEL_MAX_WIDTH = 720;
export const PANEL_DEFAULT_WIDTH = 420;
export const PANEL_WIDTH_KEY = "odo-panel-width";
// Below the enter width the panel floats over the chat; above the exit
// width it docks. Inside the band the current posture holds — that band
// is the hysteresis.
export const PANEL_OVERLAY_ENTER = 560;
export const PANEL_OVERLAY_EXIT = 600;
// The chat keeps at least this much room when drag-widening the panel.
const CHAT_MIN_RESERVE = 400;

// Hysteresis state machine — evaluated only on resize transitions (the
// observer callback), never per frame. Widths inside [enter, exit] keep
// the current posture, so 590/600 cannot oscillate.
export function nextPanelOverlay(current: boolean, dockedChatWidth: number): boolean {
  if (dockedChatWidth < PANEL_OVERLAY_ENTER) return true;
  if (dockedChatWidth > PANEL_OVERLAY_EXIT) return false;
  return current;
}

// Static clamp for stored/initial widths. Layout clamps harder via CSS.
export function clampPanelWidth(width: number): number {
  if (!Number.isFinite(width)) return PANEL_DEFAULT_WIDTH;
  return Math.min(PANEL_MAX_WIDTH, Math.max(PANEL_MIN_WIDTH, Math.round(width)));
}

// U2.1 drag clamp: min(720, window − sidebar − 400). The floor at MIN
// keeps a tiny window from inverting the range (CSS min-w 280 always
// applies anyway).
export function dragMaxPanelWidth(windowWidth: number, sidebarWidth: number): number {
  return Math.max(PANEL_MIN_WIDTH, Math.min(PANEL_MAX_WIDTH, windowWidth - sidebarWidth - CHAT_MIN_RESERVE));
}

// U2.2: width persists across sessions; a tampered/stale value clamps
// instead of producing an unusable panel.
export function readStoredPanelWidth(storage: Pick<Storage, "getItem"> = localStorage): number {
  const raw = storage.getItem(PANEL_WIDTH_KEY);
  if (raw == null) return PANEL_DEFAULT_WIDTH;
  const n = Number(raw); // Number("") is 0 — an empty/tampered value is no width at all
  return raw.trim() === "" || !Number.isFinite(n) ? PANEL_DEFAULT_WIDTH : clampPanelWidth(n);
}

// U2.1: measures .app-main and returns whether the panel must float.
//
// The decision uses the DOCKED-EQUIVALENT chat width: in overlay mode
// the fixed panel leaves the body grid and the chat reclaims its width,
// so a raw read would cross the exit threshold the moment the posture
// flips (overlay on → chat jumps 240→660 → docks → 240 → overlays …).
// Subtracting the floating panel's width keeps the signal on one scale
// and the hysteresis monotone in window width.
// mainEl arrives via a callback ref (App's ref={setState}): App gates the
// whole tree behind its `booted` early-return, so a plain ref object read
// at first commit is null AND an effect keyed on the (stable) ref object
// would never re-run when <main> finally mounts. Keying the effect on the
// ELEMENT makes attachment the subscription trigger.
export function usePanelOverlay(
  mainEl: HTMLElement | null,
  panelOpenRef: RefObject<boolean>,
): boolean {
  const [overlay, setOverlay] = useState(false);
  // useLayoutEffect (WorkstreamContextMenu no-flash pattern): the initial
  // measure lands before first paint, so a narrow window never flashes
  // the panel docked before floating it.
  useLayoutEffect(() => {
    if (mainEl == null || typeof ResizeObserver !== "function") return;
    const sync = () => {
      setOverlay((prev) => {
        let dockedChatWidth = mainEl.clientWidth;
        if (prev && panelOpenRef.current) {
          dockedChatWidth -=
            // offsetWidth = the reclaimed grid column, border included.
            document.querySelector<HTMLElement>(".context-panel")?.offsetWidth ?? 0;
        }
        return nextPanelOverlay(prev, dockedChatWidth);
      });
    };
    sync();
    const ro = new ResizeObserver(sync);
    ro.observe(mainEl);
    return () => ro.disconnect();
    // panelOpenRef is render-stable; the callback reads it live.
  }, [mainEl, panelOpenRef]);
  return overlay;
}
