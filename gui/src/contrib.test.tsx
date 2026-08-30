// P3.4 (docs/design/adoption-lock.md): panel contribution registry pins —
// the strip is data now, so these tests lock the data: exact tab set and
// order, badge derivations, PanelTab union parity, and ContextPanel's
// render-from-registry path (titles, icons, badges, parked marker).
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { PANEL_CONTRIBUTIONS, PANEL_TAB_IDS, badgeFor, type PanelBadgeInput } from "./contrib";
import ContextPanel from "./components/ContextPanel";

// jsdom has no layout engine — the tab strip scrolls the active tab into
// view and measures overflow with ResizeObserver (contextpanel.test.tsx
// stubs these; the strip never overflows here).
Element.prototype.scrollIntoView ??= () => {};
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
HTMLElement.prototype.setPointerCapture = () => {};

describe("PANEL_CONTRIBUTIONS", () => {
  it("declares exactly the 9 shipped panels, in strip order", () => {
    expect(PANEL_TAB_IDS).toEqual([
      "changes",
      "review",
      "wiki",
      "memory",
      "skills",
      "ledger",
      "runs",
      "preview",
      // D9-W3: learning control-plane observability tab, appended last.
      "learning",
    ]);
  });

  it("ids are unique and titles/icons present on every entry", () => {
    expect(new Set(PANEL_TAB_IDS).size).toBe(PANEL_TAB_IDS.length);
    for (const c of PANEL_CONTRIBUTIONS) {
      expect(c.title.trim().length).toBeGreaterThan(0);
      expect(c.icon).toBeTruthy();
    }
  });

  it("derives badges from raw counts (the old inline JSX rules, verbatim)", () => {
    const input: PanelBadgeInput = {
      pendingDiffs: 3,
      pendingReview: 2,
      wikiNotes: 5,
      memoryProposals: 4,
    };
    const byId = Object.fromEntries(
      PANEL_CONTRIBUTIONS.map((c) => [c.id, badgeFor(c, input)]),
    );
    expect(byId).toEqual({
      changes: 3,
      review: 2,
      wiki: 5,
      memory: 4,
      skills: undefined,
      ledger: undefined,
      runs: undefined,
      preview: undefined,
      learning: undefined,
    });
  });

  it("zero / null counts yield no badge", () => {
    const input: PanelBadgeInput = {
      pendingDiffs: 0,
      pendingReview: 0,
      wikiNotes: null,
      memoryProposals: 0,
    };
    for (const c of PANEL_CONTRIBUTIONS) expect(badgeFor(c, input) ?? null).toBeNull();
    // Wiki is a passthrough of pending_counts: an explicit 0 is a real
    // count, not "unknown" — the strip's count>0 gate hides it, not the derivation.
    const wiki = PANEL_CONTRIBUTIONS.find((c) => c.id === "wiki");
    expect(wiki ? badgeFor(wiki, { ...input, wikiNotes: 0 }) : null).toBe(0);
  });
});

describe("ContextPanel renders from the registry", () => {
  const badgesFor = (container: HTMLElement, tabId: string) => {
    const tab = [...container.querySelectorAll<HTMLElement>(".panel-tab")];
    const btn = tab.find((b) => b.textContent?.includes(tabId) ?? false);
    return btn?.querySelector<HTMLElement>(".panel-tab-badge")?.textContent ?? null;
  };

  it("renders one tab per registry entry, in order, with the registered titles", () => {
    const { container } = render(
      <ContextPanel open activeTab="changes" onTabChange={() => {}} />,
    );
    const tabs = [...container.querySelectorAll<HTMLElement>(".panel-tab")];
    expect(tabs.length).toBe(PANEL_CONTRIBUTIONS.length);
    expect(tabs.map((t) => t.textContent)).toEqual(
      PANEL_CONTRIBUTIONS.map((c) => c.title),
    );
  });

  it("renders badges via the registry derivation when badgeInput is given", () => {
    const { container } = render(
      <ContextPanel
        open
        activeTab="changes"
        onTabChange={() => {}}
        badgeInput={{ pendingDiffs: 7, pendingReview: 0, wikiNotes: 6, memoryProposals: 1 }}
      />,
    );
    expect(badgesFor(container, "Changes")).toBe("7");
    expect(badgesFor(container, "Review")).toBeNull(); // zero → no badge
    expect(badgesFor(container, "Wiki")).toBe("6");
    expect(badgesFor(container, "Memory")).toBe("1");
  });

  it("omitted badgeInput funnels to NO_BADGES — no badges, no crash", () => {
    const { container } = render(
      <ContextPanel open activeTab="wiki" onTabChange={() => {}} />,
    );
    expect(container.querySelector(".panel-tab-badge")).toBeNull();
  });

  it("parked marker still keys off the registry id", () => {
    const { container } = render(
      <ContextPanel
        open
        activeTab="changes"
        onTabChange={() => {}}
        parked={new Set(["ledger", "runs"])}
      />,
    );
    const tabs = [...container.querySelectorAll<HTMLElement>(".panel-tab")];
    const parkedOf = (title: string) =>
      tabs.find((b) => b.textContent?.includes(title))?.querySelector("[data-slot='parked-badge']") != null;
    expect(parkedOf("Ledger")).toBe(true);
    expect(parkedOf("Runs")).toBe(true);
    expect(parkedOf("Changes")).toBe(false);
    expect(parkedOf("Skills")).toBe(false);
  });
});
