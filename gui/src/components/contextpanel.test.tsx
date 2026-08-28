import { readFileSync } from "node:fs";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { MockInstance } from "vitest";
import { cleanup, fireEvent, render } from "@testing-library/react";
import type { ComponentProps } from "react";

// U2 (docs/design/ui-layout-lock.md §U2) — ContextPanel geometry: width
// persistence (U2.2), 420px default (U2.3), chat-width overlay posture
// (U2.1) with scrim, and the U2.4 prose-measure CSS contract. The hysteresis machine and drag math itself lives in panel_overlay.test.ts.
// Mock layout: jsdom reports 0 for every box; tests inject widths via
// data-mock-cw (statusbar.test.tsx pattern) so the sidebar term of the
// U2.1 drag clamp is real.
let clientSpy: MockInstance;
let offsetSpy: MockInstance;
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

import ContextPanel from "./ContextPanel";

// jsdom has no layout engine — scrollIntoView is unimplemented, and the
// panel's tab strip scrolls the active tab into view on every switch.
Element.prototype.scrollIntoView ??= () => {};
// Silent ResizeObserver: jsdom reports 0 for every size, so the tab strip
// never overflows in these tests (the arrow rendering is e2e's job).
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
// The drag grip captures the pointer — jsdom never fires real pointers.
HTMLElement.prototype.setPointerCapture = () => {};

type Props = ComponentProps<typeof ContextPanel>;
const BASE: Props = {
  open: true,
  activeTab: "changes",
  onTabChange: () => {},
};

const panelWidthOf = (aside: HTMLElement): string =>
  aside.style.getPropertyValue("--panel-width");

beforeEach(() => {
  cleanup();
  localStorage.clear();
});

function renderPanel(over: Partial<Props> = {}) {
  const utils = render(<ContextPanel {...BASE} {...over} />);
  const aside = utils.container.querySelector<HTMLElement>(".context-panel");
  if (aside == null) throw new Error("panel did not mount");
  return { aside, ...utils };
}

describe("panel width (U2.2/U2.3)", () => {
  it("defaults to 420px when nothing is stored", () => {
    const { aside } = renderPanel();
    expect(panelWidthOf(aside)).toBe("420px");
  });

  it("reads the stored width, clamped to 280–720", () => {
    localStorage.setItem("odo-panel-width", "500");
    expect(panelWidthOf(renderPanel().aside)).toBe("500px");
    cleanup();
    localStorage.setItem("odo-panel-width", "99999");
    expect(panelWidthOf(renderPanel().aside)).toBe("720px");
    cleanup();
    localStorage.setItem("odo-panel-width", "junk");
    expect(panelWidthOf(renderPanel().aside)).toBe("420px");
  });

  it("persists across unmount/remount", () => {
    localStorage.setItem("odo-panel-width", "512");
    const first = renderPanel();
    expect(panelWidthOf(first.aside)).toBe("512px");
    first.unmount();
    // A remount must come back at the stored width, not the default.
    const second = renderPanel();
    expect(panelWidthOf(second.aside)).toBe("512px");
  });

  it("writes the dragged width, which the next mount restores", () => {
    const { aside, unmount } = renderPanel(); // starts at the 420 default
    const grip = aside.querySelector<HTMLElement>(".panel-resize")!;
    fireEvent.pointerDown(grip, { pointerId: 1, clientX: 500 });
    // Drag left 40px → the right-docked panel widens by 40px.
    fireEvent.pointerMove(grip, { pointerId: 1, clientX: 460 });
    fireEvent.pointerUp(grip, { pointerId: 1, clientX: 460 });
    expect(panelWidthOf(aside)).toBe("460px");
    expect(localStorage.getItem("odo-panel-width")).toBe("460");
    unmount();
    expect(panelWidthOf(renderPanel().aside)).toBe("460px");
  });

  it("clamps the drag at min(720, window − sidebar − 400)", () => {
    render(<div className="sidebar" data-mock-cw="240" />);
    const { aside } = renderPanel(); // jsdom innerWidth is 1024: min(720, 1024−240−400) = 384
    const grip = aside.querySelector<HTMLElement>(".panel-resize")!;
    fireEvent.pointerDown(grip, { pointerId: 1, clientX: 500 });
    fireEvent.pointerMove(grip, { pointerId: 1, clientX: -1000 });
    fireEvent.pointerUp(grip, { pointerId: 1, clientX: -1000 });
    expect(panelWidthOf(aside)).toBe("384px");
  });
});

describe("overlay posture (U2.1)", () => {
  it("docks by default: no fixed geometry, no scrim", () => {
    const { aside, container } = renderPanel({ overlay: false });
    expect(aside.classList.contains("fixed")).toBe(false);
    expect(container.querySelector(".panel-scrim")).toBeNull();
  });

  it("floats with fixed geometry and a click-through scrim when overlay", () => {
    const { aside, container } = renderPanel({ overlay: true });
    // Token-exact class checks (diff-#97 dissent #3: no substring tests).
    expect(aside.classList.contains("fixed")).toBe(true);
    expect(aside.classList.contains("top-[var(--topbar-height)]")).toBe(true);
    expect(aside.classList.contains("right-0")).toBe(true);
    expect(aside.classList.contains("bottom-[var(--statusbar-height)]")).toBe(true);
    expect(aside.classList.contains("z-[90]")).toBe(true);
    const scrim = container.querySelector<HTMLElement>(".panel-scrim");
    expect(scrim).not.toBeNull();
    expect(scrim!.classList.contains("pointer-events-none")).toBe(true);
    expect(scrim!.classList.contains("bg-black/20")).toBe(true);
    expect(scrim!.classList.contains("z-[89]")).toBe(true);
    expect(scrim!.getAttribute("aria-hidden")).toBe("true");
  });

  it("keeps no window-width breakpoint classes on the aside", () => {
    const { aside } = renderPanel({ overlay: true });
    const breakpointed = [...aside.classList].filter((c) => c.startsWith("max-["));
    expect(breakpointed).toEqual([]);
  });

  it("contains no max-[999px] selector anywhere in the component source", () => {
    // The lock's literal U2.1 requirement: the window-width mechanism is
    // DELETED, not shadowed — one mechanism (measured chat width) only.
    const src = readFileSync("src/components/ContextPanel.tsx", "utf8");
    expect(src).not.toContain("max-[999px]");
  });
});

// U2.4 measure cap is asserted on rendered styles, not source text:
// inject app.css into the jsdom CSSOM (statusbar.test.tsx pattern — the
// Tailwind vite plugin swallows ?raw under vitest) and probe computed
// styles on real node trees.
const APP_CSS = readFileSync("src/styles/app.css", "utf8");

function withAppCss<T>(probe: () => T): T {
  const style = document.createElement("style");
  style.textContent = APP_CSS;
  document.head.appendChild(style);
  try {
    return probe();
  } finally {
    style.remove();
  }
}

const maxInlineOf = (el: HTMLElement): string =>
  getComputedStyle(el).getPropertyValue("max-inline-size");

describe("U2 app.css contract", () => {
  it("declares the 72ch cap on the bubble-scoped prose selector (U2.4)", () => {
    const style = document.createElement("style");
    style.textContent = APP_CSS;
    document.head.appendChild(style);
    const rule = [...(style.sheet?.cssRules ?? [])].find(
      (r): r is CSSStyleRule =>
        r instanceof CSSStyleRule &&
        r.selectorText.replace(/\s+/g, " ").trim() ===
          ".bubble .bubble-text :where(p, li, ul, ol, blockquote, h1, h2, h3, h4)",
    );
    style.remove();
    expect(rule?.style.getPropertyValue("max-inline-size").trim()).toBe("72ch");
  });

  it("--panel-width defaults to 420px (U2.3)", () => {
    const style = document.createElement("style");
    style.textContent = APP_CSS;
    document.head.appendChild(style);
    const root = [...(style.sheet?.cssRules ?? [])].find(
      (r): r is CSSStyleRule =>
        r instanceof CSSStyleRule && r.selectorText === ":root",
    );
    style.remove();
    expect(root?.style.getPropertyValue("--panel-width").trim()).toBe("420px");
  });

  it("caps chat-bubble prose at 72ch, exempts pre, leaves wiki untouched (U2.4)", () => {
    withAppCss(() => {
      const bubble = document.createElement("div");
      bubble.className = "bubble";
      const text = document.createElement("div");
      text.className = "bubble-text";
      const p = document.createElement("p");
      const li = document.createElement("li");
      const pre = document.createElement("pre");
      text.append(p, li, pre);
      bubble.appendChild(text);

      const wiki = document.createElement("div");
      wiki.className = "wiki-content";
      const wikiP = document.createElement("p");
      wiki.appendChild(wikiP);

      document.body.append(bubble, wiki);
      // The rule declares max-inline-size: 72ch (checked via the CSSOM
      // below); jsdom's ch→px conversion varies between environments,
      // so the computed assertion pins PRESENCE, not the conversion.
      expect(maxInlineOf(p)).not.toBe("none");
      expect(maxInlineOf(li)).not.toBe("none");
      expect(maxInlineOf(pre)).toBe("none"); // pre/table/code exempt
      expect(maxInlineOf(wikiP)).toBe("none"); // wiki keeps its own measure
      bubble.remove();
      wiki.remove();
    });
  });
});
