import { readFileSync } from "node:fs";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { MockInstance } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ComponentProps } from "react";

// U1 (docs/design/ui-layout-lock.md §U1) — StatusBar overflow fold, type
// tokens, focus/z-order CSS. The fold's pure engine is tested directly;
// the component tests drive jsdom's missing layout via dataset-injected
// mock widths (jsdom reports 0 otherwise) and a mock ResizeObserver.
//
// Panel dissent on diff #97 folded in: the +N fold is classified with the
// token-exact `chip-hidden` class (classList.contains) — a substring match
// on "hidden" would also hit the running chip's `overflow-hidden` utility.

const { ompUsageMock } = vi.hoisted(() => ({ ompUsageMock: vi.fn() }));
vi.mock("../api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  return { ...real, ompUsage: ompUsageMock };
});

import type { OmpUsageResponse } from "../types";

const OMP_OK: OmpUsageResponse = {
  ok: true,
  omp_usage: {
    usage: {
      reports: [
        { provider: "t9s", fetchedAt: 0, limits: [] },
        { provider: "solo", fetchedAt: 0, limits: [] },
      ],
    },
    grievances: [{ id: "g1", title: "flaky endpoint" }],
  },
};

import StatusBar, { computeHiddenChipKeys, OVERFLOW_RANK } from "./StatusBar";
import ModelPill from "./ModelPill";

// Mock layout: jsdom has no renderer, so offsetWidth/clientWidth come from
// dataset injected by the test (mock-w / mock-cw) — the same way e2e would
// observe real pixel widths, but deterministic.
let offsetSpy: MockInstance;
let clientSpy: MockInstance;
beforeAll(() => {
  offsetSpy = vi
    .spyOn(HTMLElement.prototype, "offsetWidth", "get")
    .mockImplementation(function (this: HTMLElement) {
      return Number(this.dataset.mockW ?? "0");
    });
  clientSpy = vi
    .spyOn(HTMLElement.prototype, "clientWidth", "get")
    .mockImplementation(function (this: HTMLElement) {
      return Number(this.dataset.mockCw ?? "0");
    });
});
afterAll(() => {
  offsetSpy.mockRestore();
  clientSpy.mockRestore();
});

// Captured mock — tests fire the callback to simulate a footer resize.
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

type Props = ComponentProps<typeof StatusBar>;
const BASE: Props = {
  workstreamName: "main",
  conversationId: 1,
  epoch: 3,
  projectRoot: "/tmp/proj",
  agentRunning: false,
  turnStartedAt: null,
  backgroundRuns: [],
  bgNotice: null,
  onJumpWorkstream: vi.fn(),
  lastPrompt: null,
  events: [],
  codingModel: "t9s/kimi-k3",
  reviewPanel: [],
  pipelineStates: [],
  pendingDiffs: 0,
  wikiNoteCount: null,
  pendingMemoryProposals: 0,
  onBadgeClick: vi.fn(),
  gateDrift: { drifted: false, seq: 0 },
};

// Full house minus pipeline: bgruns, ctx, panel, omp + three count chips.
const CHIPS_A: Partial<Props> = {
  backgroundRuns: [
    { id: 2, name: "feat-x" },
    { id: 3, name: "fix-y" },
  ],
  lastPrompt: { bytes: 1_204_000, seq: 9, layers: ["system"] },
  reviewPanel: [
    { model: "kimi-k3", provider: "t9s" },
    { model: "glm-5.2", provider: "solo" },
  ],
  pipelineStates: [],
  pendingDiffs: 3,
  wikiNoteCount: 5,
  pendingMemoryProposals: 2,
};
const WIDTHS_A: Record<string, number> = {
  bgruns: 130,
  ctx: 90,
  panel: 100,
  omp: 90,
  diffs: 40,
  wiki: 40,
  memory: 40,
};

// Everything from CHIPS_A plus the pipeline chip (the extreme fold).
const CHIPS_B: Partial<Props> = {
  ...CHIPS_A,
  pipelineStates: [{ diffId: 7, phase: "in_flight", lastSeq: 10 }],
};
const WIDTHS_B: Record<string, number> = { ...WIDTHS_A, pipeline: 110 };

/** Inject mock layout onto the rendered footer's chips. */
function sizeLayout(
  container: HTMLElement,
  widths: Record<string, number>,
  footerWidth: number,
  factWidth = 300,
): void {
  const footer = container.querySelector("footer")!;
  footer.dataset.mockCw = String(footerWidth);
  (footer.querySelector(".status-fact-btn") as HTMLElement).dataset.mockW = String(factWidth);
  for (const [key, w] of Object.entries(widths)) {
    const el = footer.querySelector<HTMLElement>(`[data-chip="${key}"]`);
    if (el != null) el.dataset.mockW = String(w);
  }
}

/** Render, inject layout, then rerender so the post-render measure converges. */
function setup(props: Partial<Props>, widths: Record<string, number>, footerWidth: number) {
  const all: Props = { ...BASE, ...props };
  const utils = render(<StatusBar {...all} />);
  sizeLayout(utils.container, widths, footerWidth);
  act(() => utils.rerender(<StatusBar {...all} />));
  return utils;
}

const foldOf = (container: HTMLElement, key: string): boolean =>
  container.querySelector(`[data-chip="${key}"]`)?.classList.contains("chip-hidden") ?? false;

beforeEach(() => {
  MockResizeObserver.instances = [];
  ompUsageMock.mockReset();
  ompUsageMock.mockResolvedValue(OMP_OK);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("computeHiddenChipKeys (pure fold engine)", () => {
  it("keeps everything when the zone fits", () => {
    const hidden = computeHiddenChipKeys(
      [
        { key: "ctx", width: 100 },
        { key: "diffs", width: 100 },
      ],
      216, // 100 + 100 + 2 gaps exactly
      46,
    );
    expect(hidden.size).toBe(0);
  });

  it("folds by spec order: ctx → omp → panel before any actionable chip", () => {
    const hidden = computeHiddenChipKeys(
      [
        { key: "bgruns", width: 50 },
        { key: "ctx", width: 50 },
        { key: "panel", width: 50 },
        { key: "omp", width: 50 },
        { key: "diffs", width: 50 },
      ],
      // after folding ctx/omp/panel: bgruns + diffs + +N(40) + 3 gaps = 164
      164,
      40,
    );
    expect([...hidden].sort()).toEqual(["ctx", "omp", "panel"]);
  });

  it("accounts for the +N chip's own width", () => {
    // diffs(100) + +N(40) + 2 gaps = 156 ≤ 190 → only ctx folds…
    expect(
      computeHiddenChipKeys(
        [
          { key: "ctx", width: 100 },
          { key: "diffs", width: 100 },
        ],
        190,
        40,
      ).size,
    ).toBe(1);
    // …but +N(120): diffs(100) + 120 + 2 gaps = 236 > 190 → both fold.
    const both = computeHiddenChipKeys(
      [
        { key: "ctx", width: 100 },
        { key: "diffs", width: 100 },
      ],
      190,
      120,
    );
    expect(both.size).toBe(2);
  });

  it("breaks rank ties by DOM order", () => {
    // pipeline and running share rank 3; running is earlier in the DOM, so
    // it folds first when exactly one slot must go.
    const hidden = computeHiddenChipKeys(
      [
        { key: "running", width: 100 },
        { key: "pipeline", width: 100 },
      ],
      190,
      40,
    );
    expect([...hidden]).toEqual(["running"]);
  });

  it("collapses everything when the zone cannot fit any chip", () => {
    const hidden = computeHiddenChipKeys(
      [
        { key: "ctx", width: 50 },
        { key: "bgruns", width: 50 },
        { key: "diffs", width: 50 },
      ],
      0,
      46,
    );
    expect(hidden.size).toBe(3);
  });

  it("rebound: widening the zone empties the hidden set", () => {
    const chips = [
      { key: "ctx", width: 90 },
      { key: "bgruns", width: 130 },
      { key: "diffs", width: 40 },
    ] as const;
    const narrow = computeHiddenChipKeys(chips, 100, 46);
    expect(narrow.size).toBeGreaterThan(0);
    expect(computeHiddenChipKeys(chips, 500, 46).size).toBe(0);
  });

  it("rank constants pin the spec's hide order", () => {
    expect(OVERFLOW_RANK.ctx).toBeLessThan(OVERFLOW_RANK.omp);
    expect(OVERFLOW_RANK.omp).toBeLessThan(OVERFLOW_RANK.panel);
    expect(OVERFLOW_RANK.panel).toBeLessThan(OVERFLOW_RANK.running);
    expect(OVERFLOW_RANK.running).toBe(OVERFLOW_RANK.pipeline);
    expect(OVERFLOW_RANK.pipeline).toBeLessThan(OVERFLOW_RANK.finished);
    expect(OVERFLOW_RANK.finished).toBe(OVERFLOW_RANK.bgruns);
    expect(OVERFLOW_RANK.bgruns).toBeLessThan(OVERFLOW_RANK.diffs);
    expect(OVERFLOW_RANK.diffs).toBe(OVERFLOW_RANK.wiki);
    expect(OVERFLOW_RANK.wiki).toBe(OVERFLOW_RANK.memory);
  });
});

describe("StatusBar overflow fold (component)", () => {
  it("folds ctx → omp → panel first; count chips and bg-runs stay", async () => {
    const onBadgeClick = vi.fn();
    const { container } = setup(
      { ...CHIPS_A, onBadgeClick },
      WIDTHS_A,
      700, // available = 700 − 24(pad) − 300(fact) − 8(gap) = 368
    );
    await screen.findByText("OMP · 2p"); // omp fetch settled

    // Telemetry folds, token-exact classification (no substring "hidden" —
    // `.status-run` carries `overflow-hidden` and must never match).
    expect(foldOf(container, "ctx")).toBe(true);
    expect(foldOf(container, "omp")).toBe(true);
    expect(foldOf(container, "panel")).toBe(true);
    expect(foldOf(container, "bgruns")).toBe(false);
    expect(foldOf(container, "diffs")).toBe(false);
    expect(foldOf(container, "wiki")).toBe(false);
    expect(foldOf(container, "memory")).toBe(false);

    // Mount invariant: folded chips stay attached for e2e hooks.
    expect(document.querySelector(".ctx-meter")).not.toBeNull();
    expect(document.querySelector(".panel-chip")).not.toBeNull();
    expect(document.querySelector(".omp-usage-chip")).not.toBeNull();

    // +N chip appears with the right count; popover shows live values.
    fireEvent.click(screen.getByLabelText("Hidden status items"));
    const dialog = await screen.findByRole("dialog", { name: "Hidden status items" });
    expect(within(dialog).getByText("~86% of context")).toBeInTheDocument();
    expect(within(dialog).getByText("Panel ×2")).toBeInTheDocument();
    expect(within(dialog).getByText("kimi-k3, glm-5.2")).toBeInTheDocument();
    expect(within(dialog).getByText("OMP · 2p")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("1 grievance")).toBeInTheDocument();
  });

  it("extreme fold: actionable chips navigate from the +N popover", async () => {
    const onBadgeClick = vi.fn();
    const onJumpWorkstream = vi.fn();
    const { container } = setup(
      { ...CHIPS_B, onBadgeClick, onJumpWorkstream },
      WIDTHS_B,
      500, // available = 500 − 24 − 300 − 8 = 172
    );
    await screen.findByText("OMP · 2p");

    // Only wiki + memory survive; everything else is folded into +6.
    expect(foldOf(container, "wiki")).toBe(false);
    expect(foldOf(container, "memory")).toBe(false);
    for (const key of ["ctx", "omp", "panel", "pipeline", "bgruns", "diffs"]) {
      expect(foldOf(container, key)).toBe(true);
    }

    const moreTrigger = screen.getByLabelText("Hidden status items");
    expect(moreTrigger).toHaveTextContent("+6");
    fireEvent.click(moreTrigger);
    const dialog = await screen.findByRole("dialog", { name: "Hidden status items" });

    // Live values: ctx pct, panel composition, pipeline label, per-run rows.
    expect(within(dialog).getByText("~86% of context")).toBeInTheDocument();
    expect(within(dialog).getByText("Panel ×2")).toBeInTheDocument();
    expect(within(dialog).getByText("verify → panel…")).toBeInTheDocument();
    expect(within(dialog).getByText("feat-x")).toBeInTheDocument();
    expect(within(dialog).getByText("fix-y")).toBeInTheDocument();

    // Actionable rows navigate exactly like the chips they mirror.
    fireEvent.click(within(dialog).getByText("3 pending diffs"));
    expect(onBadgeClick).toHaveBeenCalledWith("changes");
    expect(screen.queryByRole("dialog", { name: "Hidden status items" })).toBeNull();

    // Reopen: the workstream jump is one click per run row.
    fireEvent.click(moreTrigger);
    const dialog2 = await screen.findByRole("dialog", { name: "Hidden status items" });
    fireEvent.click(within(dialog2).getByText("feat-x"));
    expect(onJumpWorkstream).toHaveBeenCalledWith(2);
    expect(screen.queryByRole("dialog", { name: "Hidden status items" })).toBeNull();
  });

  it("gate-drift alert: renders when latched, routes to review, never folds", async () => {
    const onBadgeClick = vi.fn();
    const { container } = setup(
      {
        ...CHIPS_B,
        onBadgeClick,
        gateDrift: { drifted: true, seq: 12, detail: "internal/ipc/gatepolicy.go sha16 drift" },
      },
      WIDTHS_B,
      500,
    );
    await screen.findByText("OMP · 2p");

    const chip = screen.getByLabelText("gate drift — landing frozen");
    expect(chip.classList.contains("gate-drift-chip")).toBe(true);
    // The title carries the verbatim drift detail + the remediation steps.
    expect(chip.title).toContain("internal/ipc/gatepolicy.go sha16 drift");
    expect(chip.title).toContain("odo gate re-pin");
    // No data-chip attribute — the overflow engine can never fold it.
    expect(chip.getAttribute("data-chip")).toBeNull();
    fireEvent.click(chip);
    expect(onBadgeClick).toHaveBeenCalledWith("review");
    expect(container.querySelector(".gate-drift-chip")).not.toBeNull();
  });

  it("gate-drift alert width subtracts from the fold zone", async () => {
    // Mirror the rebound case's inline pattern: arming the banner's mock
    // width then re-rendering re-measures via the post-render effect.
    const all: Props = { ...BASE, ...CHIPS_B, gateDrift: { drifted: true, seq: 12 } };
    const { container, rerender } = render(<StatusBar {...all} />);
    sizeLayout(container, WIDTHS_B, 500);
    act(() => rerender(<StatusBar {...all} />));
    await screen.findByText("OMP · 2p");
    expect(screen.getByLabelText("Hidden status items")).toHaveTextContent("+6");
    // Available drops 168px (160 + the banner's gap): every chip folds.
    (container.querySelector(".gate-drift-chip") as HTMLElement).dataset.mockW = "160";
    act(() => rerender(<StatusBar {...all} />));
    expect(screen.getByLabelText("Hidden status items")).toHaveTextContent("+8");
    // Folded or not, the alarm itself is never the hidden one.
    expect(container.querySelector(".gate-drift-chip")).not.toBeNull();
  });

  it("clear posture renders no drift chip", () => {
    const { container } = setup({ gateDrift: { drifted: false, seq: 9 } }, {}, 1000);
    expect(container.querySelector(".gate-drift-chip")).toBeNull();
    expect(screen.queryByLabelText("gate drift — landing frozen")).toBeNull();
  });

  it("rebound: widening the footer un-folds every chip (no self-lock)", async () => {
    const all: Props = { ...BASE, ...CHIPS_A };
    const { container, rerender } = render(<StatusBar {...all} />);
    sizeLayout(container, WIDTHS_A, 700);
    act(() => rerender(<StatusBar {...all} />));
    await screen.findByText("OMP · 2p");
    expect(container.querySelector(".chip-hidden")).not.toBeNull();

    sizeLayout(container, WIDTHS_A, 1600);
    act(() => rerender(<StatusBar {...all} />));
    expect(container.querySelector(".chip-hidden")).toBeNull();
    expect(container.querySelector("[data-chip-more]")).toBeNull();
    expect(screen.queryByLabelText("Hidden status items")).toBeNull();
  });

  it("resize without a rerender folds via ResizeObserver + 50ms debounce", async () => {
    const all: Props = { ...BASE, ...CHIPS_A };
    const { container, rerender } = render(<StatusBar {...all} />);
    sizeLayout(container, WIDTHS_A, 1600);
    act(() => rerender(<StatusBar {...all} />));
    await screen.findByText("OMP · 2p");
    expect(container.querySelector(".chip-hidden")).toBeNull();

    // Pure layout change — no React rerender, no fold until the RO fires
    // and the debounce elapses.
    sizeLayout(container, WIDTHS_A, 700);
    vi.useFakeTimers();
    act(() => MockResizeObserver.fire());
    act(() => {
      vi.advanceTimersByTime(40);
    });
    expect(container.querySelector(".chip-hidden")).toBeNull();
    act(() => {
      vi.advanceTimersByTime(20);
    });
    expect(foldOf(container, "ctx")).toBe(true);
    expect(foldOf(container, "omp")).toBe(true);
    expect(foldOf(container, "panel")).toBe(true);
    expect(foldOf(container, "bgruns")).toBe(false);
  });

  it("chip chrome uses the micro/nano type tokens (U1.2)", async () => {
    setup(CHIPS_A, WIDTHS_A, 1600); // wide — nothing folds
    await screen.findByText("OMP · 2p");
    const chip = document.querySelector(".omp-usage-chip")!;
    expect(chip.className).toContain("text-[length:var(--text-micro)]");
    expect(chip.className).not.toContain("text-[10px]");
    const badge = await screen.findByLabelText("1 grievance");
    expect(badge.className).toContain("text-[length:var(--text-nano)]");
    expect(badge.className).not.toContain("text-[9px]");
  });
});

// app.css rule assertions. Under vitest the Tailwind plugin swallows a
// ?raw import, so read the file via node:fs and inject it into the jsdom
// CSSOM; var() references are asserted on rule declarations (cssstyle does
// not resolve them to pixels — e2e covers the rendered result).
const APP_CSS = readFileSync("src/styles/app.css", "utf8"); // vitest root = gui/

function appCssRules(): CSSStyleRule[] {
  const style = document.createElement("style");
  style.id = "app-css-test-injection";
  style.textContent = APP_CSS;
  document.head.appendChild(style);
  const rules = [...(style.sheet?.cssRules ?? [])].filter(
    (r): r is CSSStyleRule => r instanceof CSSStyleRule,
  );
  style.remove();
  return rules;
}
// jsdom's CSSOM preserves the source newline between comma selectors —
// collapse whitespace before comparing.
const findRule = (rules: CSSStyleRule[], sel: string): CSSStyleRule | undefined =>
  rules.find((r) => r.selectorText.replace(/\s+/g, " ").trim() === sel);

describe("U1 app.css contract", () => {
  it("--text-nano is declared and the 9px sites consume it", () => {
    const rules = appCssRules();
    expect(findRule(rules, ":root")?.style.getPropertyValue("--text-nano").trim()).toBe("10px");
    // The two 9px sites the diff had to migrate — .queue-next-tag was the
    // site diff #97 missed (panel dissent #2).
    expect(findRule(rules, ".queue-next-tag")?.style.getPropertyValue("font-size").trim()).toBe(
      "var(--text-nano)",
    );
  });

  it("hardcoded sizes map onto the semantic scale", () => {
    const rules = appCssRules();
    expect(findRule(rules, ".mono")?.style.getPropertyValue("font-size").trim()).toBe(
      "var(--text-caption)",
    );
    expect(findRule(rules, ".topbar-action")?.style.getPropertyValue("font-size").trim()).toBe(
      "var(--text-caption)",
    );
    expect(findRule(rules, ".settings-title")?.style.getPropertyValue("font-size").trim()).toBe(
      "var(--text-heading)",
    );
    expect(
      findRule(rules, ".diff-toggle button, .theme-toggle button")
        ?.style.getPropertyValue("font-size")
        .trim(),
    ).toBe("var(--text-label)");
  });

  it("U1.3: the global .truncate cap is gone", () => {
    const rules = appCssRules();
    expect(
      rules.some((r) =>
        r.selectorText
          .split(",")
          .map((s) => s.trim())
          .includes(".truncate"),
      ),
    ).toBe(false);
  });

  it("U1.4: the outline kill is scoped to the composer textarea only", () => {
    const rules = appCssRules();
    const textareaKill = findRule(rules, ".chat-input textarea:focus");
    expect(textareaKill?.style.getPropertyValue("outline").trim()).toBe("none");
    // The blanket descendant rules must not exist anywhere — they killed
    // ModelPill-trigger and menu-item focus rings.
    expect(
      rules.some((r) => r.selectorText === ".chat-input :focus, .chat-input :focus-visible"),
    ).toBe(false);
    expect(
      rules.some((r) => r.selectorText === ".chat-input :focus" || r.selectorText === ".chat-input :focus-visible"),
    ).toBe(false);
    // The generic ring survives for everything else.
    expect(findRule(rules, ":focus-visible:not([role=\"dialog\"])")).toBeDefined();
  });

  it("U1.5: toasts stack above the chat-width-driven panel overlay (z-90)", () => {
    const rules = appCssRules();
    expect(Number(findRule(rules, ".toast-viewport")?.style.getPropertyValue("z-index"))).toBe(95);
  });
});

const CHAT_SURFACE_SRC = readFileSync(
  "src/components/ChatSurface.tsx",
  "utf8",
);

describe("composer focus treatment (structural contract, rendered pixels via e2e)", () => {
  it("focus-within border is the 55% user-accent tint", () => {
    expect(CHAT_SURFACE_SRC).toContain(
      "focus-within:border-[color-mix(in_srgb,var(--accent-user)_55%,transparent)]",
    );
  });
  it("slash/mention menu rows carry the explicit 220px cap", () => {
    expect(CHAT_SURFACE_SRC).toContain("slash-desc max-w-[220px] truncate");
    expect(CHAT_SURFACE_SRC).toContain("at-detail max-w-[220px] truncate");
  });
});

describe("ModelPill focus ring (U1.4)", () => {
  it("trigger carries the CVA Button focus-visible ring", () => {
    render(<ModelPill projectRoot={null} currentModel="sudo/t9s/kimi-k3" />);
    const trigger = screen.getByRole("button", { name: /coding model/i });
    expect(trigger.className).toContain("focus-visible:outline-none");
    expect(trigger.className).toContain("focus-visible:ring-2");
    expect(trigger.className).toContain("focus-visible:ring-[var(--accent-user)]");
  });
});
