import { describe, expect, it } from "vitest";

import { deriveLoopStates, loopMode, loopPhase, actionableLoop } from "./loop";
import type { EventPayload, OdoEvent } from "./types";

// Table-driven pins for the TS mirror of the daemon's fold
// (internal/ipc/loop_journal.go). Each case is a journal prefix; the
// expected fields assert the same state the Go fold derives. Cross-checks
// stay shallow (status/phase/verdict/spent) — the Go tests pin the deep
// wire facts; this table guards drift on the facts the GUI consumes.

const T = "2026-08-18T00:00:00.000Z";

function ev(seq: number, type: OdoEvent["type"], payload: EventPayload = {}): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at: T };
}

const loop = (seq: number, payload: EventPayload) => ev(seq, "loop_event", payload);

const started = (seq: number, extra: EventPayload = {}) =>
  loop(seq, { kind: "loop_started", loop_id: 0, mode: "audit", base: "a97bd3d", max_rounds: 10, budget_tokens: 2_000_000, ...extra });

describe("deriveLoopStates", () => {
  it("returns nothing on a loop-free journal", () => {
    expect(deriveLoopStates([])).toEqual([]);
    expect(deriveLoopStates([ev(1, "user_message", { text: "hi" })])).toEqual([]);
  });

  it("started only: active, seeding, id = the started row's seq", () => {
    const [st] = deriveLoopStates([started(7)]);
    expect(st.id).toBe(7);
    expect(st.status).toBe("active");
    expect(st.mode).toBe("audit");
    expect(st.maxRounds).toBe(10);
    expect(st.phase).toBe("seeding");
    expect(st.spentTokens).toBe(0);
    expect(st.latestVerdict).toBe("");
  });

  it("audit round with no verdict: auditing that round, spend is max-wins", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1, subject_sha16: "abc", spent_tokens: 4000 }),
      loop(5, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 2, subject_sha16: "def", spent_tokens: 9000 }),
      // A stale row with a LOWER ledger must never rewind the cumulative.
      loop(6, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 3, subject_sha16: "ghi", spent_tokens: 5000 }),
    ]);
    expect(st.rounds.map((r) => r.round)).toEqual([1, 2, 3]);
    expect(st.spentTokens).toBe(9000);
    expect(st.phase).toBe("auditing round 3");
  });

  it("verdict fix marks the round and waits for the fix spawn only once", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1, spent_tokens: 100 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "fix", spent_tokens: 100 }),
    ]);
    expect(st.latestVerdict).toBe("fix");
    expect(st.awaitingFixSpawn).toBe(true);
    expect(st.phase).toBe("fixing");
  });

  it("fix spawn + accept closes the phase landed and counts the land", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "fix" }),
      loop(6, { kind: "loop_fix_spawn", loop_id: 3, mode: "audit", round: 1, findings_count: 2, prompt_tokens_est: 8000, spent_tokens: 8100 }),
      ev(7, "review_action", { action: "accept", actor: "auto_loop", diff_id: 21 }),
    ]);
    expect(st.fixesLanded).toBe(1);
    expect(st.fixOpen).toBe(false);
    expect(st.fixOutcome).toBe("landed");
    expect(st.phase).toBe("auditing round 2");
  });

  it("auto_land_blocked{loop_*} closes the phase unlanded", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "fix" }),
      loop(6, { kind: "loop_fix_spawn", loop_id: 3, mode: "audit", round: 1 }),
      ev(7, "review_action", { action: "auto_land_blocked", actor: "auto_loop", diff_id: 21, reason: "loop_verify_failed" }),
    ]);
    expect(st.fixOpen).toBe(false);
    expect(st.fixOutcome).toBe("unlanded");
  });

  it("pipeline rows attribute to the newest live loop only", () => {
    const states = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_completed", loop_id: 3, mode: "audit", rounds: 1, fixes_landed: 0 }),
      started(5),
      loop(6, { kind: "loop_audit_round", loop_id: 5, mode: "audit", round: 1 }),
      loop(7, { kind: "loop_verdict", loop_id: 5, mode: "audit", round: 1, verdict: "fix" }),
      loop(8, { kind: "loop_fix_spawn", loop_id: 5, mode: "audit", round: 1 }),
      ev(9, "review_action", { action: "accept", actor: "auto_loop", diff_id: 30 }),
    ]);
    expect(states[0].status).toBe("completed");
    expect(states[0].fixOutcome).toBe("");
    expect(states[1].fixOutcome).toBe("landed");
    expect(states[1].fixesLanded).toBe(1);
  });

  it("clean verdict then completed: terminal, kind recorded once", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "clean" }),
      loop(6, { kind: "loop_completed", loop_id: 3, mode: "audit", rounds: 1, fixes_landed: 2, spent_tokens: 42_000 }),
    ]);
    expect(st.status).toBe("completed");
    expect(st.phase).toBe("completed");
    expect(st.terminalKinds).toEqual(["loop_completed"]);
    expect(st.spentTokens).toBe(42_000);
  });

  it("suspended carries the cause and closes any open fix", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "fix" }),
      loop(6, { kind: "loop_fix_spawn", loop_id: 3, mode: "audit", round: 1 }),
      loop(7, { kind: "loop_suspended", loop_id: 3, mode: "audit", cause: "stall", spent_tokens: 12_000 }),
    ]);
    expect(st.status).toBe("suspended");
    expect(st.cause).toBe("stall");
    expect(st.phase).toBe("suspended: stall");
    expect(st.fixOpen).toBe(false);
    expect(st.awaitingFixSpawn).toBe(false);
    expect(st.terminalKinds).toEqual(["loop_suspended"]);
  });

  it("budget exceeded folds as suspended with the derived cause", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_budget_exceeded", loop_id: 3, mode: "audit", spent_tokens: 2_100_000, budget_tokens: 2_000_000 }),
    ]);
    expect(st.status).toBe("suspended");
    expect(st.cause).toBe("budget_exceeded");
    expect(st.phase).toBe("suspended: budget_exceeded");
    expect(st.terminalKinds).toEqual(["loop_budget_exceeded"]);
  });

  it("stopped is terminal", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_stopped", loop_id: 3, mode: "audit", detail: "stopped from the GUI", origin: "loop_ctl" }),
    ]);
    expect(st.status).toBe("stopped");
    expect(st.phase).toBe("stopped");
    expect(st.terminalKinds).toEqual(["loop_stopped"]);
  });

  it("resume clears the cause; a non-respawn cause converts the fix to a re-audit", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "fix" }),
      loop(6, { kind: "loop_suspended", loop_id: 3, mode: "audit", cause: "stall" }),
      loop(7, { kind: "loop_resumed", loop_id: 3, mode: "audit", cause: "stall", origin: "loop_ctl" }),
    ]);
    expect(st.status).toBe("active");
    expect(st.cause).toBe("");
    expect(st.fixOutcome).toBe("unlanded");
    // fixOutcome resolved → no longer "fixing": the next audit runs.
    expect(st.phase).toBe("auditing round 2");
  });

  it("resume with a respawn cause re-arms the fix spawn (fix_no_diff)", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_audit_round", loop_id: 3, mode: "audit", round: 1 }),
      loop(5, { kind: "loop_verdict", loop_id: 3, mode: "audit", round: 1, verdict: "fix" }),
      loop(6, { kind: "loop_suspended", loop_id: 3, mode: "audit", cause: "fix_no_diff" }),
      loop(7, { kind: "loop_resumed", loop_id: 3, mode: "audit", cause: "fix_no_diff" }),
    ]);
    expect(st.awaitingFixSpawn).toBe(true);
    expect(st.fixOutcome).toBe("");
    expect(st.phase).toBe("fixing");
  });

  it("resume budget raise replaces the fold's budget", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_budget_exceeded", loop_id: 3, mode: "audit" }),
      loop(5, { kind: "loop_resumed", loop_id: 3, mode: "audit", cause: "budget_exceeded", budget: 5_000_000 }),
    ]);
    expect(st.status).toBe("active");
    expect(st.budgetTokens).toBe(5_000_000);
  });

  it("a re-suspend of a notified kind never re-notifies; distinct kinds do", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_suspended", loop_id: 3, mode: "audit", cause: "stall" }),
      loop(5, { kind: "loop_notified", loop_id: 3, mode: "audit", terminal_kind: "loop_suspended", origin: "loop_ctl" }),
      loop(6, { kind: "loop_resumed", loop_id: 3, mode: "audit", cause: "stall" }),
      loop(7, { kind: "loop_suspended", loop_id: 3, mode: "audit", cause: "human_interleave" }),
      loop(8, { kind: "loop_budget_exceeded", loop_id: 3, mode: "audit" }),
    ]);
    expect(st.notifiedKinds).toEqual(["loop_suspended"]);
    expect(st.terminalKinds).toEqual(["loop_suspended", "loop_budget_exceeded"]);
  });

  it("duplicate notified rows fold idempotently", () => {
    const [st] = deriveLoopStates([
      started(3),
      loop(4, { kind: "loop_completed", loop_id: 3, mode: "audit", rounds: 2, fixes_landed: 1 }),
      loop(5, { kind: "loop_notified", loop_id: 3, mode: "audit", terminal_kind: "loop_completed", origin: "loop_ctl" }),
      loop(6, { kind: "loop_notified", loop_id: 3, mode: "audit", terminal_kind: "loop_completed", origin: "loop_ctl" }),
    ]);
    expect(st.notifiedKinds).toEqual(["loop_completed"]);
  });

  it("rows of an unknown loop id are skipped, never crash the fold", () => {
    const states = deriveLoopStates([
      loop(2, { kind: "loop_suspended", loop_id: 99, cause: "stall" }),
      started(3),
    ]);
    expect(states).toHaveLength(1);
    expect(states[0].status).toBe("active");
  });

  it("tasks mode flips to tasks_final once every task is done", () => {
    const [st] = deriveLoopStates([
      started(3, { mode: "tasks", tasks: ["fix a", "fix b"] }),
      loop(4, { kind: "loop_design_lock", loop_id: 3, mode: "tasks", task: 1 }),
      loop(5, { kind: "loop_task_spawn", loop_id: 3, mode: "tasks", task: 1 }),
      loop(6, { kind: "loop_task_done", loop_id: 3, mode: "tasks", task: 1, status: "landed" }),
      loop(7, { kind: "loop_task_done", loop_id: 3, mode: "tasks", task: 2, status: "vetoed" }),
    ]);
    expect(st.mode).toBe("tasks");
    expect(st.finalAudit).toBe(true);
    expect(loopMode(st)).toBe("tasks_final");
    expect(st.tasks[0].doneStatus).toBe("landed");
    expect(st.tasks[0].designLockSeq).toBe(0); // spawn cleared the gate marker
    expect(st.phase).toBe("seeding"); // final audit hasn't run round 1 yet
  });
});

describe("loopPhase render tokens", () => {
  it("covers the design lock's closed phase enum", () => {
    const mk = (patch: Partial<Parameters<typeof loopPhase>[0]>) =>
      loopPhase({
        id: 1,
        mode: "audit",
        status: "active",
        cause: "",
        base: "",
        maxRounds: 10,
        budgetTokens: 0,
        tasks: [],
        finalAudit: false,
        rounds: [],
        latestVerdict: "",
        awaitingFixSpawn: false,
        fixOpen: false,
        fixOutcome: "",
        fixesLanded: 0,
        spentTokens: 0,
        notifiedKinds: [],
        terminalKinds: [],
        lastKind: "",
        lastSeq: 0,
        phase: "",
        ...patch,
      });
    expect(mk({ status: "stopped" })).toBe("stopped");
    expect(mk({ status: "completed" })).toBe("completed");
    expect(mk({ status: "suspended", cause: "subject_too_large" })).toBe("suspended: subject_too_large");
    expect(mk({})).toBe("seeding");
    expect(mk({ rounds: [{ seq: 2, round: 1, subjectSha16: "", verdict: "" }] })).toBe("auditing round 1");
    expect(mk({ rounds: [{ seq: 2, round: 1, subjectSha16: "", verdict: "clean" }] })).toBe("auditing round 2");
    expect(mk({ rounds: [{ seq: 2, round: 1, subjectSha16: "", verdict: "fix" }], awaitingFixSpawn: true })).toBe("fixing");
  });
});

describe("actionableLoop render gate", () => {
  it("picks the newest active-or-suspended loop, none past the terminal pair", () => {
    const events = [
      started(3),
      loop(4, { kind: "loop_completed", loop_id: 3, mode: "audit", rounds: 1, fixes_landed: 0 }),
      started(5),
      loop(6, { kind: "loop_suspended", loop_id: 5, mode: "audit", cause: "stall" }),
    ];
    const loops = deriveLoopStates(events);
    expect(actionableLoop(loops)?.id).toBe(5);
    expect(actionableLoop(deriveLoopStates(events.slice(0, 2)))).toBeNull();
    expect(actionableLoop([])).toBeNull();
  });
});
