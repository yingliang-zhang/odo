import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

// P1.2 (docs/design/adoption-lock.md): the central selector map — typed
// consumption (import-typing) pins, plus one render proof that a data-slot
// attribute actually lands in the DOM (the rest are covered by e2e specs
// that import SLOT/slotSel rather than duplicating literals).

import { SLOT, slotSel } from "./slots";
import ContextPanel from "./components/ContextPanel";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
Element.prototype.scrollIntoView ??= () => {};

describe("SLOT map (P1.2)", () => {
  it("covers the P1 touch points", () => {
    expect(SLOT).toEqual({
      composer: "composer",
      statusbar: "statusbar",
      panelTabs: "panel-tabs",
      diffCard: "diff-card",
      palette: "palette",
      shortcuts: "shortcuts",
    });
  });

  it("values are unique, kebab-case DOM tokens", () => {
    const values = Object.values(SLOT);
    expect(new Set(values).size).toBe(values.length);
    for (const v of values) expect(v).toMatch(/^[a-z][a-z0-9-]*$/);
  });

  it("slotSel is the ONLY selector form specs need", () => {
    expect(slotSel(SLOT.palette)).toBe('[data-slot="palette"]');
    expect(slotSel(SLOT.diffCard)).toBe('[data-slot="diff-card"]');
  });

  it("panel-tabs data-slot lands in the rendered DOM", () => {
    const { container } = render(<ContextPanel open activeTab="changes" onTabChange={() => {}} />);
    expect(container.querySelector(slotSel(SLOT.panelTabs))).not.toBeNull();
    expect(container.querySelector(`${slotSel(SLOT.panelTabs)}.panel-tabs`)).not.toBeNull();
  });
});
