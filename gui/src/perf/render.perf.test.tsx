// P3 (docs/design/adoption-lock.md): render-cost gate for the hot GUI
// surfaces. Direct component mounts (never full App), timed with
// performance.now() around act()-flushed renders; per surface: warmup 1,
// N measured iterations with a fresh mount each (cleanup between), p95
// across the samples, asserted against the committed limits in
// baseline.json (= measured p95 × multiplier, rounded up to the next
// 10ms). Deterministic iteration count, no fake timers, no wall-clock
// sleeps.
//
//   npx vitest run --config vitest.perf.config.ts                  # gate
//   PERF_UPDATE=1 npx vitest run --config vitest.perf.config.ts    # re-baseline
//
// PERF_UPDATE=1 skips the assertions and regenerates baseline.json's
// measuredP95Ms + limits from THIS machine's numbers.

import { readFileSync, writeFileSync } from "node:fs";
import { afterAll, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render } from "@testing-library/react";
import type { RenderResult } from "@testing-library/react";
import ChatSurface from "../components/ChatSurface";
import ContextPanel from "../components/ContextPanel";
import MessageBubble from "../components/MessageBubble";
import RunsPanel from "../components/RunsPanel";
import type { PanelTab } from "../contrib";
import { ev } from "../dev/fixtures";
import type { OdoEvent } from "../types";

// jsdom gaps the surfaces hit at mount/switch (contextpanel.test.tsx
// stubs the same trio): the tab strip scrolls the active tab into view,
// ResizeObserver drives the overflow arrows, the drag grip captures
// pointers. Silent no-ops — geometry is e2e's job.
Element.prototype.scrollIntoView ??= () => {};
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
HTMLElement.prototype.setPointerCapture = () => {};

// ---------- Gate plumbing ----------

interface SurfaceBaseline {
  mountMs: number;
  interactMs: number | null;
  measuredP95Ms: { mount: number; interact: number | null };
  multiplier: number;
}

const MULTIPLIER = 3;
const ITERATIONS = 8;
const UPDATE = process.env.PERF_UPDATE === "1";
// cwd-relative, the repo's test convention (vitest runs with cwd=gui/).
const BASELINE_PATH = "src/perf/baseline.json";
const BASELINE = JSON.parse(readFileSync(BASELINE_PATH, "utf8")) as Record<string, SurfaceBaseline>;
// Filled by each gate() call; PERF_UPDATE writes it back out at the end.
const REPORT: Record<string, { mount: number; interact: number | null }> = {};

function round1(n: number): number {
  return Math.round(n * 10) / 10;
}

// Committed limits: p95 × multiplier, rounded UP to the next 10ms (the
// generous clipping the lock asks for — noise never trips the gate, an
// order-of-magnitude regression always does).
function limitOf(p95Ms: number): number {
  return Math.ceil((p95Ms * MULTIPLIER) / 10) * 10;
}

function p95(samples: number[]): number {
  const sorted = [...samples].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil(0.95 * sorted.length) - 1)];
}

// Warmup + N fresh-mount iterations; cleanup() between mounts, p95 of
// the mount and interaction samples.
function measureCycles(
  cycle: () => { mountMs: number; interactMs: number | null },
): { mount: number; interact: number | null } {
  cycle(); // warmup — first-import JIT cost never lands in the samples
  cleanup();
  const mounts: number[] = [];
  const interacts: number[] = [];
  for (let i = 0; i < ITERATIONS; i++) {
    const r = cycle();
    mounts.push(r.mountMs);
    if (r.interactMs != null) interacts.push(r.interactMs);
    cleanup();
  }
  return { mount: p95(mounts), interact: interacts.length === 0 ? null : p95(interacts) };
}

// Time one render pass (RTL's render/rerender/fireEvent act-wrap and
// flush effects themselves — measured cost covers the full commit; an
// outer act() would only nest scopes).
function timed(fn: () => void): number {
  const t0 = performance.now();
  fn();
  return performance.now() - t0;
}

function gate(surface: string, cycles: { mount: number; interact: number | null }): void {
  REPORT[surface] = {
    mount: round1(cycles.mount),
    interact: cycles.interact == null ? null : round1(cycles.interact),
  };
  if (UPDATE) return; // numbers recorded; limits refreshed in afterAll
  const base = BASELINE[surface];
  expect(
    base,
    `perf gate: baseline.json has no entry for "${surface}" — regenerate with PERF_UPDATE=1 npx vitest run --config vitest.perf.config.ts`,
  ).toBeDefined();
  expect(
    cycles.mount,
    `perf gate "${surface}" MOUNT p95 ${round1(cycles.mount)}ms exceeds limit ${base.mountMs}ms ` +
      `(committed baseline ${base.measuredP95Ms.mount}ms × ${base.multiplier}; regenerate: PERF_UPDATE=1 npx vitest run --config vitest.perf.config.ts)`,
  ).toBeLessThanOrEqual(base.mountMs);
  if (cycles.interact != null && base.interactMs != null) {
    expect(
      cycles.interact,
      `perf gate "${surface}" INTERACT p95 ${round1(cycles.interact)}ms exceeds limit ${base.interactMs}ms ` +
        `(committed baseline ${base.measuredP95Ms.interact}ms × ${base.multiplier}; regenerate: PERF_UPDATE=1 npx vitest run --config vitest.perf.config.ts)`,
    ).toBeLessThanOrEqual(base.interactMs);
  }
}

afterAll(() => {
  if (!UPDATE) return;
  const next: Record<string, SurfaceBaseline> = {};
  for (const [surface, m] of Object.entries(REPORT)) {
    next[surface] = {
      mountMs: limitOf(m.mount),
      interactMs: m.interact == null ? null : limitOf(m.interact),
      measuredP95Ms: { mount: m.mount, interact: m.interact },
      multiplier: MULTIPLIER,
    };
  }
  writeFileSync(BASELINE_PATH, JSON.stringify(next, null, 2) + "\n");
  // eslint-disable-next-line no-console
  console.log(`[perf] PERF_UPDATE=1 — baseline.json regenerated; limits = ceil(p95 × ${MULTIPLIER}) up to the next 10ms`);
});

beforeEach(cleanup);

// ---------- Fixtures ----------

// 50 runs × 4 rows (user / agent text / tool call / tool result) — the
// fixture.ts ev() builder keeps journal shape (gap-free per-conv seqs,
// OdoEvent field set). At 50 run groups ChatSurface's 25-group render
// window mounts the newest ~100 events per mount.
const CHAT_EVENTS: OdoEvent[] = (() => {
  const out: OdoEvent[] = [];
  for (let i = 0; i < 50; i++) {
    out.push(ev("user_message", { text: `Prompt ${i}: implement the edit batch for slice ${i}` }));
    out.push(ev("agent_text", { text: `Working slice ${i} — tracing the affected modules, applying the edits, then reporting back.` }));
    out.push(ev("agent_tool_call", { tool: "read_file", args: { path: `src/slice-${i}.ts` } }));
    out.push(ev("agent_tool_result", { tool: "read_file", result: `${200 + i} lines read from slice ${i}` }));
  }
  return out;
})();
const CHAT_EVENTS_APPENDED = [...CHAT_EVENTS, ev("agent_text", { text: "Appended follow-up note after the last run." })];

// 50 folded runs for deriveRuns: plain sends closed by agent_done, half
// carrying a prompt-byte estimate, every third run followed by a
// measured loop_run_usage receipt (round/task fallback resolution).
const RUNS_EVENTS: OdoEvent[] = (() => {
  const out: OdoEvent[] = [];
  for (let i = 0; i < 50; i++) {
    out.push(ev("user_message", {
      text: `Run goal ${i}`,
      ...(i % 2 === 0 ? { total_prompt_bytes: 4096 + i } : {}),
    }));
    out.push(ev("agent_done", { summary: `run ${i} finished` }));
    if (i % 3 === 0) {
      out.push(ev("loop_event", {
        kind: "loop_run_usage",
        loop_id: 1,
        kind_run: "fix",
        run_id: `r${i}`,
        covers_spawn_seq: 0,
        usage_available: true,
        input_tokens: 4000 + i,
        output_tokens: 100 + i,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
      }));
    }
  }
  return out;
})();
const RUNS_EVENTS_APPENDED = [
  ...RUNS_EVENTS,
  ev("user_message", { text: "Run goal 50 — the appended prompt" }),
];

// P1.4 inline-diff path (tooldiff.test.tsx fixture shape): the tool
// result carries a 5-file unified diff, so the hunk parser + diff-row
// render are on the clock instead of the plain <pre> branch.
const DIFF_EVENT: OdoEvent = {
  id: 1,
  conversation_id: 1,
  seq: 1,
  type: "agent_tool_result",
  payload: {
    tool: "run_command",
    result: Array.from({ length: 5 }, (_, f) =>
      [
        `diff --git a/src/file-${f}.ts b/src/file-${f}.ts`,
        "index 111..222 100644",
        `--- a/src/file-${f}.ts`,
        `+++ b/src/file-${f}.ts`,
        `@@ -${10 * f + 1},4 +${10 * f + 1},4 @@`,
        ` const base${f} = ${f};`,
        `-export const oldName${f} = base${f};`,
        `+export const newName${f} = base${f};`,
        " tail();",
      ].join("\n"),
    ).join("\n"),
  },
  created_at: "2026-08-29T10:00:00Z",
};

// ---------- Surfaces ----------

describe("render-cost gate (P3)", () => {
  it("chat transcript with 200 events mounts and appends within budget", () => {
    const props = {
      agentRunning: false,
      sendDisabled: false,
      onSend: async () => {},
      onCancel: () => {},
      epoch: 1,
      conversationId: 1,
      onOpenNote: () => {},
      searchOpen: false,
      searchQuery: "",
      onSearchQueryChange: () => {},
      onSearchClose: () => {},
    };
    gate("chat-transcript-200", measureCycles(() => {
      let util!: RenderResult;
      const mountMs = timed(() => {
        util = render(<ChatSurface {...props} events={CHAT_EVENTS} />);
      });
      // Interaction: one poll delta lands — the transcript window folds
      // in one new event; memo'd bubbles for the older window bail.
      const interactMs = timed(() => {
        util.rerender(<ChatSurface {...props} events={CHAT_EVENTS_APPENDED} />);
      });
      util.unmount();
      return { mountMs, interactMs };
    }));
  });

  it("context panel with 8 tabs switches within budget", () => {
    gate("context-panel-tabs", measureCycles(() => {
      let activeTab: PanelTab = "changes";
      const onTabChange = (tab: PanelTab) => {
        activeTab = tab;
      };
      let util!: RenderResult;
      const mountMs = timed(() => {
        util = render(
          <ContextPanel open activeTab="changes" onTabChange={onTabChange}>
            <div className="panel-body-child">tab body</div>
          </ContextPanel>,
        );
      });
      // Interaction: click through all eight tab buttons, each click
      // followed by the App-side rerender the switch produces (the body
      // subtree remounts under RunGroupBoundary's new resetKey).
      const tabs = [...util.container.querySelectorAll<HTMLElement>('[role="tab"]')];
      expect(tabs).toHaveLength(8);
      const interactMs = timed(() => {
        for (const tabEl of tabs) {
          fireEvent.click(tabEl);
          util.rerender(
            <ContextPanel open activeTab={activeTab} onTabChange={onTabChange}>
              <div className="panel-body-child">tab body</div>
            </ContextPanel>,
          );
        }
      });
      util.unmount();
      return { mountMs, interactMs };
    }));
  });

  it("runs panel with 50 runs re-derives within budget", () => {
    // Same-identity rerender (the memo bail on quiet poll ticks) is
    // measured alongside the append and reported; it must stay a
    // fraction of the append budget, never the regression hiding spot.
    const bailSamples: number[] = [];
    const cycles = measureCycles(() => {
      let util!: RenderResult;
      const mountMs = timed(() => {
        util = render(<RunsPanel events={RUNS_EVENTS} projectRoot={null} active={true} />);
      });
      // Interaction: one new journaled event — deriveRuns re-folds the
      // stream and the 50-row table rebuilds.
      const interactMs = timed(() => {
        util.rerender(<RunsPanel events={RUNS_EVENTS_APPENDED} projectRoot={null} active={true} />);
      });
      bailSamples.push(timed(() => {
        util.rerender(<RunsPanel events={RUNS_EVENTS_APPENDED} projectRoot={null} active={true} />);
      }));
      util.unmount();
      return { mountMs, interactMs };
    });
    const bailP95 = p95(bailSamples);
    gate("runs-panel-50", cycles);
    if (UPDATE) {
      process.stderr.write(`[perf] runs-panel-50 memo-bail p95 ${round1(bailP95)}ms (same-identity rerender)\n`);
    } else {
      expect(
        bailP95,
        `perf gate "runs-panel-50" MEMO-BAIL p95 ${round1(bailP95)}ms exceeds the interact budget ${BASELINE["runs-panel-50"].interactMs}ms — the memo no longer bails on same-identity rerenders`,
      ).toBeLessThanOrEqual(BASELINE["runs-panel-50"].interactMs ?? Infinity);
    }
  });

  it("message bubble renders a tool-result inline diff within budget", () => {
    gate("messagebubble-diff", measureCycles(() => {
      let util!: RenderResult;
      const mountMs = timed(() => {
        util = render(<MessageBubble event={DIFF_EVENT} />);
      });
      // Interaction: stream replace — a fresh event object forces the
      // memo'd bubble through the diff path again (hunk parse + rows).
      const interactMs = timed(() => {
        util.rerender(<MessageBubble event={{ ...DIFF_EVENT }} />);
      });
      util.unmount();
      return { mountMs, interactMs };
    }));
  });
});
