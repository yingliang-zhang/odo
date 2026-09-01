// P3.4 (docs/design/adoption-lock.md): panel contribution registry pins —
// the strip is data now, so these tests lock the data: exact tab set and
// order, badge derivations, PanelTab union parity, and ContextPanel's
// render-from-registry path (titles, icons, badges, parked marker).
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { K8S_CONTRIBUTION, PANEL_CONTRIBUTIONS, PANEL_TAB_IDS, badgeFor, type PanelBadgeInput } from "./contrib";
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
      // UX-1 D2: the plan layer's surface opens the strip (default tab).
      "tasks",
      "changes",
      "review",
      "wiki",
      "memory",
      "skills",
      "runs",
      "preview",
      // D9-W3: learning control-plane observability tab, appended last.
      "learning",
    ]);
  });

  // A3-3 strip budget (ux-batch-lock-amendment-a3): the strip stays at
  // or below the measured no-arrow fit — 9 badge-laden tabs ≈ 665px vs
  // the 703px client at the 720px MAX (measured live 2026-09-01; UX-1
  // D2's 10 tabs ≈ 730px overflowed the 659px with-controls client at
  // rest). A new entry must pay for its tab width or the strip regresses
  // to the arrows posture.
  it("holds the A3-3 strip budget: at most 9 entries fit the MAX panel without arrows", () => {
    expect(PANEL_CONTRIBUTIONS.length).toBeLessThanOrEqual(9);
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
      openTodos: 6,
      activeJobs: 2,
      activeBatches: 1,
    };
    const byId = Object.fromEntries(
      PANEL_CONTRIBUTIONS.map((c) => [c.id, badgeFor(c, input)]),
    );
    expect(byId).toEqual({
      tasks: 6,
      changes: 3,
      review: 2,
      wiki: 5,
      memory: 4,
      skills: undefined,
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
      openTodos: 0,
      activeJobs: 0,
      activeBatches: 0,
    };
    for (const c of PANEL_CONTRIBUTIONS) expect(badgeFor(c, input) ?? null).toBeNull();
    // Wiki is a passthrough of pending_counts: an explicit 0 is a real
    // count, not "unknown" — the strip's count>0 gate hides it, not the derivation.
    const wiki = PANEL_CONTRIBUTIONS.find((c) => c.id === "wiki");
    expect(wiki ? badgeFor(wiki, { ...input, wikiNotes: 0 }) : null).toBe(0);
  });
});

describe("K8S_CONTRIBUTION (D5b / A2-5)", () => {
  it("stays OUT of the static 9 — the allowlist must not admit jobs while k8s is off", () => {
    expect(PANEL_TAB_IDS).not.toContain("jobs");
    expect(PANEL_CONTRIBUTIONS).toHaveLength(9);
    expect(K8S_CONTRIBUTION.id).toBe("jobs");
    expect(K8S_CONTRIBUTION.title).toBe("Jobs");
  });

  it("badge = active jobs + active batches (A2-5), zero renders none", () => {
    const base: PanelBadgeInput = {
      pendingDiffs: 0,
      pendingReview: 0,
      wikiNotes: null,
      memoryProposals: 0,
      openTodos: 0,
      activeJobs: 0,
      activeBatches: 0,
    };
    expect(badgeFor(K8S_CONTRIBUTION, base) ?? null).toBeNull();
    expect(badgeFor(K8S_CONTRIBUTION, { ...base, activeJobs: 2 })).toBe(2);
    expect(badgeFor(K8S_CONTRIBUTION, { ...base, activeBatches: 1 })).toBe(1);
    expect(badgeFor(K8S_CONTRIBUTION, { ...base, activeJobs: 2, activeBatches: 3 })).toBe(5);
  });

  it("the gated strip renders TEN tabs with a jobs badge (App's k8s-on posture)", () => {
    const { container } = render(
      <ContextPanel
        open
        activeTab="jobs"
        onTabChange={() => {}}
        contributions={[...PANEL_CONTRIBUTIONS, K8S_CONTRIBUTION]}
        badgeInput={{ pendingDiffs: 0, pendingReview: 0, wikiNotes: null, memoryProposals: 0, openTodos: 0, activeJobs: 2, activeBatches: 1 }}
      />,
    );
    const tabs = [...container.querySelectorAll<HTMLElement>(".panel-tab")];
    expect(tabs.length).toBe(10);
    const jobsTab = tabs.find((b) => b.textContent?.includes("Jobs")) ?? null;
    expect(jobsTab).not.toBeNull();
    expect(jobsTab?.querySelector(".panel-tab-badge")?.textContent).toBe("3");
  });

  it("the default strip (k8s off posture) still renders 9 tabs and no jobs entry", () => {
    const { container } = render(
      <ContextPanel open activeTab="tasks" onTabChange={() => {}} />,
    );
    expect(container.querySelectorAll(".panel-tab").length).toBe(9);
    expect([...container.querySelectorAll<HTMLElement>(".panel-tab")].some((b) => b.textContent?.includes("Jobs"))).toBe(false);
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
        badgeInput={{ pendingDiffs: 7, pendingReview: 0, wikiNotes: 6, memoryProposals: 1, openTodos: 2, activeJobs: 0, activeBatches: 0 }}
      />,
    );
    expect(badgesFor(container, "Changes")).toBe("7");
    expect(badgesFor(container, "Tasks")).toBe("2");
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
        parked={new Set(["runs", "learning"])}
      />,
    );
    const tabs = [...container.querySelectorAll<HTMLElement>(".panel-tab")];
    const parkedOf = (title: string) =>
      tabs.find((b) => b.textContent?.includes(title))?.querySelector("[data-slot='parked-badge']") != null;
    expect(parkedOf("Runs")).toBe(true);
    expect(parkedOf("Learning")).toBe(true);
    expect(parkedOf("Changes")).toBe(false);
    expect(parkedOf("Skills")).toBe(false);
  });
});
