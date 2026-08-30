import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

// Grounded revise R2 (2026-08-29) — App-level pins for the three panel
// findings, driven against the real App with the api seam mocked (same
// proxy harness as app_keepalive.test.tsx):
//   F1  classified failure at POLL_FAIL_RESTART_THRESHOLD (20) grows the
//       reload affordance (the pre-diff banner contract); below it, none.
//   F2  focusSeq transcript-jump pin: retired once the jump lands (flash
//       settled) AND on a workstream/conversation switch (no stale-seq
//       collision pinning an unrelated group into the window).
//   F3  failure dismissal is keyed by class: A stays hidden while A keeps
//       failing, a class change re-arms (A→B→A surfaces A again), and an
//       explicit Reconnect voids the dismissal as a fresh start.
// Poll failures ride fake timers: each 16s advance fires at least one
// back-off tick (cap 15s) so ~6-8 iterations reach 20 consecutive fails.
const { stubs, PURE_API } = vi.hoisted(() => ({
  stubs: new Map<string, Mock>(),
  PURE_API: new Set(["unwrap", "errorMessage"]),
}));
vi.mock("./api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  const plain: Record<string, unknown> = { ...real };
  return new Proxy(plain, {
    get(target, key, recv) {
      if (typeof key !== "string" || PURE_API.has(key) || !(key in target)) {
        return Reflect.get(target, key, recv);
      }
      let fn = stubs.get(key);
      if (!fn) {
        fn = vi.fn(async () => ({ ok: true }));
        stubs.set(key, fn);
      }
      return fn;
    },
  });
});

import App from "./App";
import { FAILURE_TAXONOMY } from "./errors";
import { SLOT, slotSel } from "./slots";
import type { EventPayload, OdoEvent } from "./types";

// jsdom has no layout engine (keepalive precedent).
Element.prototype.scrollIntoView ??= () => {};
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const stubApi = (name: string): Mock => {
  let fn = stubs.get(name);
  if (!fn) {
    fn = vi.fn(async () => ({ ok: true }));
    stubs.set(name, fn);
  }
  return fn;
};

const PROJECT = { id: 1, name: "proj", root_path: "/tmp/proj" };
const WORKSTREAM = { id: 1, project_id: 1, name: "main", status: "ready" };
const CONVERSATION = { id: 1, workstream_id: 1, epoch: 1, state: "idle" };
const WS2 = { id: 2, project_id: 1, name: "alt", status: "ready" };
const CONVERSATION2 = { id: 2, workstream_id: 2, epoch: 1, state: "idle" };

// socket_closed / heartbeat_timeout rows proven in errors.test.ts.
const FAIL_A = new Error("send failed: daemon closed the connection without responding");
const FAIL_B = new Error("daemon did not answer on /tmp/.odo/odo.sock within 10s (see daemon.log)");

const overlay = () => document.querySelector(slotSel(SLOT.failureOverlay));
const overlayTitle = () => document.querySelector(".failure-overlay-title")?.textContent ?? null;
const reloadBtn = () => document.querySelector(slotSel(SLOT.failureReload));
const chip = () => document.querySelector(".transcript-window-chip")?.textContent ?? null;
const runsRows = () => Array.from(document.querySelectorAll<HTMLElement>(`[data-slot="${SLOT.runsRow}"]`));

function ev(seq: number, type: OdoEvent["type"], payload: EventPayload = {}, conversationId = 1): OdoEvent {
  return {
    id: seq,
    conversation_id: conversationId,
    seq,
    type,
    payload,
    created_at: `2026-08-29T10:00:${String(seq % 60).padStart(2, "0")}.000Z`,
  };
}

// `groups` run pairs → runGroups.length === groups. Row goals carry the
// prefix so a conversation switch is observable in the Runs tab.
function runEvents(groups: number, goalPrefix: string, conversationId = 1): OdoEvent[] {
  const rows: OdoEvent[] = [];
  for (let i = 1; i <= groups; i++) {
    rows.push(ev(2 * i - 1, "user_message", { text: `${goalPrefix} ${i}` }, conversationId));
    rows.push(ev(2 * i, "agent_done", { summary: `done ${i}` }, conversationId));
  }
  return rows;
}

const flush = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

// Advance in `step` chunks until cond — 16s ≥ one full back-off interval.
async function until(cond: () => boolean, step = 16_000, maxIter = 40): Promise<boolean> {
  for (let i = 0; i < maxIter && !cond(); i++) await flush(step);
  return cond();
}

async function boot() {
  render(<App />);
  const ok = await until(() => document.querySelector(".sidebar .proj-tree") != null, 100, 200);
  if (!ok) throw new Error("boot did not settle");
}

beforeEach(() => {
  vi.useFakeTimers();
  cleanup();
  stubs.clear();
  localStorage.clear();
  localStorage.setItem("odo-active-project", PROJECT.root_path);
  stubApi("listProjects").mockResolvedValue([
    { root: PROJECT.root_path, name: PROJECT.name, added: "2026-01-01" },
  ]);
  stubApi("bootstrap").mockResolvedValue({
    ok: true,
    project: PROJECT,
    workstream: WORKSTREAM,
    conversation: CONVERSATION,
    events: [],
  });
  stubApi("listWorkstreams").mockResolvedValue({ ok: true, workstreams: [WORKSTREAM] });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("F1: reload affordance in the classified lane", () => {
  it("classified failure at 20 consecutive polls grows the reload affordance; below it, none", async () => {
    stubApi("pollEvents").mockRejectedValue(FAIL_A);
    await boot();

    // The overlay arms at 3 failures — well below the restart threshold:
    // the escape hatch must NOT be present yet.
    expect(await until(() => overlay() != null, 16_000, 10)).toBe(true);
    expect(overlayTitle()).toBe(FAILURE_TAXONOMY.socket_closed.title);
    expect(reloadBtn()).toBeNull();

    // Kept failing past 20 consecutive polls, the overlay grows the same
    // reload lever the legacy banner has (pre-diff contract).
    expect(await until(() => reloadBtn() != null, 16_000, 40)).toBe(true);
  });
});

describe("F3: class-keyed dismissal", () => {
  it("unclassifiable failures have no dismissal concept: the legacy banner arms (regression: null dismissal must not swallow cls=null)", async () => {
    stubApi("pollEvents").mockRejectedValue(new Error("kaboom: something entirely novel broke"));
    await boot();
    expect(await until(() => document.querySelector(".daemon-down-banner") != null, 16_000, 10)).toBe(true);
    // Below 20 consecutive failures the banner carries no restart button,
    // and nothing classified the string so no overlay appears either.
    expect(overlay()).toBeNull();
    expect(document.querySelector(".daemon-down-restart")).toBeNull();
  });

  it("dismiss A; A recurring stays hidden; class change re-arms; A→B→A surfaces A again", async () => {
    stubApi("pollEvents").mockRejectedValue(FAIL_A);
    await boot();
    expect(await until(() => overlay() != null, 16_000, 10)).toBe(true);
    expect(overlayTitle()).toBe(FAILURE_TAXONOMY.socket_closed.title);

    fireEvent.click(screen.getByRole("button", { name: "Dismiss failure overlay" }));
    expect(overlay()).toBeNull();
    // Same class keeps failing for several back-off intervals: never re-armed.
    await flush(16_000);
    await flush(16_000);
    await flush(16_000);
    expect(overlay()).toBeNull();

    // A different class arrives: the dismissal does not cross classes.
    stubApi("pollEvents").mockRejectedValue(FAIL_B);
    expect(await until(() => overlay() != null, 16_000, 10)).toBe(true);
    expect(overlayTitle()).toBe(FAILURE_TAXONOMY.heartbeat_timeout.title);

    // Dismiss B, then the failure flaps back to A: A→B→A IS a class
    // change — A's old dismissal must not swallow it.
    fireEvent.click(screen.getByRole("button", { name: "Dismiss failure overlay" }));
    stubApi("pollEvents").mockRejectedValue(FAIL_A);
    expect(await until(() => overlay() != null, 16_000, 10)).toBe(true);
    expect(overlayTitle()).toBe(FAILURE_TAXONOMY.socket_closed.title);
  });

  it("Reconnect voids the dismissal: a dismissed class recurring after reconnect surfaces again past the threshold", async () => {
    stubApi("pollEvents").mockRejectedValue(FAIL_A);
    await boot();
    expect(await until(() => overlay() != null, 16_000, 10)).toBe(true);
    expect(overlayTitle()).toBe(FAILURE_TAXONOMY.socket_closed.title);

    fireEvent.click(screen.getByRole("button", { name: "Dismiss failure overlay" }));
    expect(overlay()).toBeNull();

    // Class flips, then the user explicitly reconnects: a fresh start —
    // counters reset AND the stale dismissal is gone.
    stubApi("pollEvents").mockRejectedValue(FAIL_B);
    expect(await until(() => overlay() != null, 16_000, 10)).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    expect(overlay()).toBeNull();

    // The pre-dismissal class A recurs past the threshold: it must surface
    // (pre-fix, the surviving dismissal hid it for the rest of the session).
    stubApi("pollEvents").mockRejectedValue(FAIL_A);
    expect(await until(() => overlay() != null, 16_000, 12)).toBe(true);
    expect(overlayTitle()).toBe(FAILURE_TAXONOMY.socket_closed.title);
  });
});

describe("F2: focusSeq jump pin lifecycle", () => {
  // Opens the Runs tab and boots; callers stub bootstrap (+workstreams)
  // BEFORE calling — their mockImplementation must win.
  const bootRuns = async () => {
    localStorage.setItem("odo-panel-open", "true");
    localStorage.setItem("odo-panel-tab", "runs");
    await boot();
    expect(await until(() => runsRows().length > 0, 200, 40)).toBe(true);
  };

  it("jump-landed retires the pin: the transcript window bound snaps back after the flash", async () => {
    // 30 groups > the 25-group window: 5 hidden → chip visible from the start.
    stubApi("bootstrap").mockResolvedValue({
      ok: true,
      project: PROJECT,
      workstream: WORKSTREAM,
      conversation: CONVERSATION,
      events: runEvents(30, "goal"),
    });
    await bootRuns();
    expect(chip()).toContain("5 earlier run groups hidden");

    // Jump to the oldest run: the pin forces its group into the window —
    // nothing is hidden while the request stands.
    const oldest = runsRows().find((r) => r.getAttribute("data-seq") === "1");
    expect(oldest).toBeTruthy();
    fireEvent.click(oldest!);
    expect(chip()).toBeNull();

    // Once the flash settles (1.6 s) ChatSurface reports the landing and
    // the App clears the request: the window bound returns.
    await flush(1_700);
    expect(chip()).toContain("5 earlier run groups hidden");
  });

  it("a workstream switch retires a standing pin — no stale-seq collision in the new conversation", async () => {
    // conv1's events (seqs 1..60): the jump pins seq 1. conv2's events
    // deliberately reuse the SAME seq range — a stale pin would collide
    // with conv2's group 0 and force it into the window.
    stubApi("bootstrap").mockImplementation((...args: unknown[]) => {
      const wsId = args[1] as number | undefined;
      return Promise.resolve(
        wsId === 2
          ? { ok: true, project: PROJECT, workstream: WS2, conversation: CONVERSATION2, events: runEvents(30, "alt-goal", 2) }
          : { ok: true, project: PROJECT, workstream: WORKSTREAM, conversation: CONVERSATION, events: runEvents(30, "goal") },
      );
    });
    stubApi("listWorkstreams").mockResolvedValue({ ok: true, workstreams: [WORKSTREAM, WS2] });
    await bootRuns();
    expect(chip()).toContain("5 earlier run groups hidden");

    const oldest = runsRows().find((r) => r.getAttribute("data-seq") === "1");
    fireEvent.click(oldest!);
    expect(chip()).toBeNull(); // pin standing

    // Switch before the 1.6 s landing flash settles: the switch itself
    // must retire the request.
    const alt = Array.from(document.querySelectorAll<HTMLElement>(".sidebar .ws-item")).find((r) =>
      r.textContent?.includes("alt"),
    );
    expect(alt).toBeTruthy();
    fireEvent.click(alt!);

    // conv2 renders (its own 30 runs, same seq space) with the bound
    // intact: the carried-over pin must NOT force group 0 into the window.
    expect(
      await until(() => runsRows().some((r) => r.textContent?.includes("alt-goal 1") ?? false), 200, 60),
    ).toBe(true);
    expect(chip()).toContain("5 earlier run groups hidden");
    // …and it stays retired past the dead timer's horizon.
    await flush(2_000);
    expect(chip()).toContain("5 earlier run groups hidden");
  });
});
