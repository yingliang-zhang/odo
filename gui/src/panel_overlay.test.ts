import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { MockInstance } from "vitest";
import { act, renderHook } from "@testing-library/react";

// U2 (docs/design/ui-layout-lock.md §U2.1–U2.3) — panel overlay hysteresis
// machine, drag clamp, width persistence, and the ResizeObserver hook.
// The hook test drives jsdom's missing layout via data-mock-cw widths and
// a captured mock ResizeObserver (statusbar.test.tsx pattern).

import {
  PANEL_DEFAULT_WIDTH,
  PANEL_MAX_WIDTH,
  PANEL_MIN_WIDTH,
  PANEL_OVERLAY_ENTER,
  PANEL_OVERLAY_EXIT,
  PANEL_WIDTH_KEY,
  clampPanelWidth,
  dragMaxPanelWidth,
  nextPanelOverlay,
  readStoredPanelWidth,
  usePanelOverlay,
} from "./panel_overlay";

describe("nextPanelOverlay (U2.1 hysteresis state machine)", () => {
  it("walks the lock's sequence 700→540→580→620 without oscillation", () => {
    let overlay = false;
    overlay = nextPanelOverlay(overlay, 700); // stays docked
    expect(overlay).toBe(false);
    overlay = nextPanelOverlay(overlay, 540); // < 560 → overlay on
    expect(overlay).toBe(true);
    overlay = nextPanelOverlay(overlay, 580); // inside the band → holds
    expect(overlay).toBe(true);
    overlay = nextPanelOverlay(overlay, 620); // > 600 → docks again
    expect(overlay).toBe(false);
    overlay = nextPanelOverlay(overlay, 540); // re-enters
    expect(overlay).toBe(true);
  });

  it("holds the current posture across the whole 560–600 dead band", () => {
    for (const w of [560, 570, 590, 600]) {
      expect(nextPanelOverlay(false, w)).toBe(false);
      expect(nextPanelOverlay(true, w)).toBe(true);
    }
  });

  it("flips only outside the band — 559 overlays, 601 docks", () => {
    expect(nextPanelOverlay(false, PANEL_OVERLAY_ENTER - 1)).toBe(true);
    expect(nextPanelOverlay(true, PANEL_OVERLAY_ENTER - 1)).toBe(true);
    expect(nextPanelOverlay(true, PANEL_OVERLAY_EXIT + 1)).toBe(false);
    expect(nextPanelOverlay(false, PANEL_OVERLAY_EXIT + 1)).toBe(false);
  });
});

describe("dragMaxPanelWidth (U2.1 drag clamp)", () => {
  it("is min(720, window − sidebar − 400) with a MIN-width floor", () => {
    // 1280×720 window, expanded 240px sidebar (default Playwright viewport).
    expect(dragMaxPanelWidth(1280, 240)).toBe(640);
    // Huge window clamps at the CSS max.
    expect(dragMaxPanelWidth(2000, 240)).toBe(PANEL_MAX_WIDTH);
    // Collapsed 48px rail buys the panel room.
    expect(dragMaxPanelWidth(1280, 48)).toBe(PANEL_MAX_WIDTH);
    // A tiny window would invert the range — floor at MIN.
    expect(dragMaxPanelWidth(700, 240)).toBe(PANEL_MIN_WIDTH);
    // Sidebar element missing (jsdom) treats its width as 0.
    expect(dragMaxPanelWidth(1280, 0)).toBe(PANEL_MAX_WIDTH);
  });
});

describe("panel width storage (U2.2/U2.3)", () => {
  it("clampPanelWidth clamps to the static 280–720 range", () => {
    expect(clampPanelWidth(100)).toBe(PANEL_MIN_WIDTH);
    expect(clampPanelWidth(PANEL_MIN_WIDTH)).toBe(PANEL_MIN_WIDTH);
    expect(clampPanelWidth(500)).toBe(500);
    expect(clampPanelWidth(50000)).toBe(PANEL_MAX_WIDTH);
    expect(clampPanelWidth(359.6)).toBe(360);
    expect(clampPanelWidth(Number.NaN)).toBe(PANEL_DEFAULT_WIDTH);
  });

  it("readStoredPanelWidth defaults to 420 without a stored key", () => {
    expect(PANEL_DEFAULT_WIDTH).toBe(420);
    expect(readStoredPanelWidth({ getItem: () => null })).toBe(420);
  });

  it("readStoredPanelWidth parses and clamps on read", () => {
    expect(readStoredPanelWidth({ getItem: () => "500" })).toBe(500);
    expect(readStoredPanelWidth({ getItem: () => "100" })).toBe(PANEL_MIN_WIDTH);
    expect(readStoredPanelWidth({ getItem: () => "99999" })).toBe(PANEL_MAX_WIDTH);
    expect(readStoredPanelWidth({ getItem: () => "not-a-number" })).toBe(PANEL_DEFAULT_WIDTH);
    expect(readStoredPanelWidth({ getItem: () => "" })).toBe(PANEL_DEFAULT_WIDTH);
  });
});

// --- usePanelOverlay hook: mock layout + captured ResizeObserver ---------

let offsetSpy: MockInstance;
let clientSpy: MockInstance;
beforeAll(() => {
  offsetSpy = vi
    .spyOn(HTMLElement.prototype, "offsetWidth", "get")
    .mockImplementation(function (this: HTMLElement) {
      return Number(this.dataset.mockCw ?? "0");
    });
  clientSpy = vi
    .spyOn(HTMLElement.prototype, "clientWidth", "get")
    .mockImplementation(function (this: HTMLElement) {
      return Number(this.dataset.mockCw ?? "0");
    });
});
afterAll(() => {
  clientSpy.mockRestore();
  offsetSpy.mockRestore();
});

class MockResizeObserver {
  static instances: MockResizeObserver[] = [];
  static fire(): void {
    for (const i of MockResizeObserver.instances) {
      i.cb([], i as unknown as ResizeObserver);
    }
  }
  private cb: ResizeObserverCallback;
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
    MockResizeObserver.instances.push(this);
  }
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
globalThis.ResizeObserver = MockResizeObserver as unknown as typeof ResizeObserver;

beforeEach(() => {
  MockResizeObserver.instances = [];
  document.body.innerHTML = "";
});

function setup(chatWidth: number, panelOpen: boolean) {
  const main = document.createElement("main");
  main.className = "app-main";
  main.dataset.mockCw = String(chatWidth);
  document.body.appendChild(main);
  const openRef = { current: panelOpen };
  const hook = renderHook(() => usePanelOverlay(main, openRef));
  return { main, hook };
}

describe("usePanelOverlay (U2.1 ResizeObserver glue)", () => {
  it("drives docked→overlay→hold→docked through the spec's widths", () => {
    const { main, hook } = setup(700, true);
    expect(hook.result.current).toBe(false); // initial sync at a wide chat

    main.dataset.mockCw = "540";
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(true);

    main.dataset.mockCw = "580"; // dead band — no flip
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(true);

    main.dataset.mockCw = "620";
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(false);

    main.dataset.mockCw = "540";
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(true);
  });

  it("does not oscillate when the overlay itself widens the chat", () => {
    // Overlay entered at a 240px docked chat; going fixed leaves the grid
    // and .app-main reclaims the 420px panel width (660px). A raw read
    // would cross 600 and flip straight back — the hook subtracts the
    // floating panel's width so the decision stays on the docked scale.
    // (mockCw drives BOTH the clientWidth and offsetWidth spies here —
    // the hook reads the panel's offsetWidth for the correction.)
    const aside = document.createElement("aside");
    aside.className = "context-panel";
    aside.dataset.mockCw = "420";
    document.body.appendChild(aside);

    const { main, hook } = setup(240, true);
    expect(hook.result.current).toBe(true);

    main.dataset.mockCw = "660"; // chat reclaimed the panel width
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(true); // holds — 660−420 = 240 < 560

    main.dataset.mockCw = "1030"; // window widened: 1030−420 = 610 > 600
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(false);
  });

  it("skips the docked-equivalent correction while the panel is closed", () => {
    const { main, hook } = setup(500, false);
    // Panel closed: a raw 500px read is already the docked width.
    expect(hook.result.current).toBe(true);
    main.dataset.mockCw = "700";
    act(() => MockResizeObserver.fire());
    expect(hook.result.current).toBe(false);
  });

  it("re-measures on mount without an observer event", () => {
    const { hook } = setup(400, false);
    expect(hook.result.current).toBe(true);
    expect(MockResizeObserver.instances).toHaveLength(1);
  });
});

// PANEL_WIDTH_KEY is the U2.2 persistence key — pinned so a rename is a
// deliberate migration, not a drive-by.
it("uses the odo-panel-width storage key", () => {
  expect(PANEL_WIDTH_KEY).toBe("odo-panel-width");
});
