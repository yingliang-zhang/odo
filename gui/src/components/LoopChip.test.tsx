import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

// LoopChip design gate (loopDesignCtl GUI entry): the chip folds the
// journal (deriveLoopStates) exactly like production — fixtures are
// journal prefixes, not hand-built state — and dispatches the daemon's
// loop_ctl verbs. api is mocked at the module seam; everything above it
// (fold → render → click → dispatch) runs for real.

const { loopCtlMock } = vi.hoisted(() => ({ loopCtlMock: vi.fn() }));
vi.mock("../api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  return { ...real, loopCtl: loopCtlMock };
});

import LoopChip from "./LoopChip";
import { deriveLoopStates } from "../loop";
import type { EventPayload, OdoEvent } from "../types";

const T = "2026-08-31T00:00:00.000Z";
const ev = (seq: number, payload: EventPayload): OdoEvent =>
  ({ id: seq, conversation_id: 1, seq, type: "loop_event", payload, created_at: T } as OdoEvent);

const started = (seq: number, extra: EventPayload = {}) =>
  ev(seq, { kind: "loop_started", loop_id: 0, mode: "tasks", base: "a97bd3d", max_rounds: 10, budget_tokens: 2_000_000, ...extra });

// A tasks loop parked at task 1's design gate.
const gatedJournal = [
  started(3, { tasks: ["fix a", "fix b"] }),
  ev(4, { kind: "loop_design_lock", loop_id: 3, mode: "tasks", task: 1 }),
];
// The gate consumed: the approve journaled task 1's spawn.
const approvedJournal = [...gatedJournal, ev(5, { kind: "loop_task_spawn", loop_id: 3, mode: "tasks", task: 1 })];
// A plain audit-mode loop never holds a design gate.
const auditJournal = [
  ev(2, { kind: "loop_started", loop_id: 0, mode: "audit", base: "a97bd3d", max_rounds: 10, budget_tokens: 2_000_000 }),
];

function renderChip(events: OdoEvent[], overrides: Partial<Parameters<typeof LoopChip>[0]> = {}) {
  return render(
    <LoopChip
      conversationId={1}
      projectRoot={null}
      loops={deriveLoopStates(events)}
      onChanged={vi.fn()}
      onError={vi.fn()}
      {...overrides}
    />,
  );
}

afterEach(() => {
  cleanup();
  loopCtlMock.mockReset();
});

describe("LoopChip design gate", () => {
  it("a pending design lock renders the gate label and both verbs", () => {
    renderChip(gatedJournal);
    expect(screen.getByText(/design gate · task 1/)).toBeInTheDocument();
    expect(screen.getByLabelText("Approve task 1 design")).toBeInTheDocument();
    expect(screen.getByLabelText("Veto task 1 design")).toBeInTheDocument();
    // Parked at the gate means the loop is waiting, not working.
    const spinner = document.querySelector(".loop-chip svg");
    expect(spinner?.classList.contains("spin")).toBe(false);
  });

  it("approve dispatches loop_ctl approve_design and nudges the poll", async () => {
    loopCtlMock.mockResolvedValue({ ok: true });
    const onChanged = vi.fn();
    renderChip(gatedJournal, { onChanged });
    fireEvent.click(screen.getByLabelText("Approve task 1 design"));
    expect(loopCtlMock).toHaveBeenCalledWith(1, "approve_design", { projectRoot: undefined });
    await vi.waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
  });

  it("veto dispatches loop_ctl veto_design", async () => {
    loopCtlMock.mockResolvedValue({ ok: true, event: {} });
    renderChip(gatedJournal);
    fireEvent.click(screen.getByLabelText("Veto task 1 design"));
    expect(loopCtlMock).toHaveBeenCalledWith(1, "veto_design", { projectRoot: undefined });
  });

  it("a daemon refusal surfaces through onError (gate consumed by another client)", async () => {
    loopCtlMock.mockResolvedValue({ ok: false, error: "loop_ctl: no design lock is awaiting the gate" });
    const onError = vi.fn();
    renderChip(gatedJournal, { onError });
    fireEvent.click(screen.getByLabelText("Approve task 1 design"));
    await vi.waitFor(() =>
      expect(onError).toHaveBeenCalledWith("loop_ctl: no design lock is awaiting the gate"),
    );
  });

  it("no gate: tasks loop after the spawn is plain working state", () => {
    renderChip(approvedJournal);
    expect(screen.queryByLabelText("Approve task 1 design")).toBeNull();
    expect(screen.getByText(/seeding/)).toBeInTheDocument();
    const spinner = document.querySelector(".loop-chip svg");
    expect(spinner?.classList.contains("spin")).toBe(true);
  });

  it("audit-mode loops never gain design-gate verbs", () => {
    renderChip(auditJournal);
    expect(screen.queryByText(/design gate/)).toBeNull();
    expect(screen.getByLabelText("Stop loop")).toBeInTheDocument();
  });
});
