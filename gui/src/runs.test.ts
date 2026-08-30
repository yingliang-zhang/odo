import { describe, expect, it } from "vitest";

import { deriveRuns } from "./runs";
import type { EventPayload, OdoEvent } from "./types";

function ev(seq: number, type: OdoEvent["type"], payload: EventPayload = {}): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at: "2026-08-29T00:00:00.000Z" };
}

function usageRow(seq: number, payload: EventPayload): OdoEvent {
  return ev(seq, "loop_event", { kind: "loop_run_usage", loop_id: 1, mode: "audit", ...payload });
}

describe("deriveRuns", () => {
  it("opens a run on a plain send and leaves it running before any terminal", () => {
    const rows = deriveRuns([ev(3, "user_message", { text: "fix the bug" })]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      startSeq: 3,
      status: "running",
      goal: "fix the bug",
    });
    expect(rows[0].endSeq).toBeUndefined();
    expect(rows[0].finishedAt).toBeUndefined();
  });

  it("closes the open run ok on agent_done, error on agent_error", () => {
    const ok = deriveRuns([
      ev(1, "user_message", { text: "do a thing" }),
      ev(2, "agent_done", { summary: "did the thing" }),
    ]);
    expect(ok[0]).toMatchObject({ status: "ok", endSeq: 2, endSummary: "did the thing" });

    const err = deriveRuns([
      ev(1, "user_message", { text: "do a thing" }),
      ev(2, "agent_error", { error: "boom" }),
    ]);
    expect(err[0]).toMatchObject({ status: "error", endSeq: 2, endError: "boom" });
  });

  it("treats consecutive terminals as a defensive no-op after the close", () => {
    const rows = deriveRuns([
      ev(1, "user_message", { text: "one" }),
      ev(2, "agent_done", { summary: "closed" }),
      ev(3, "agent_done", { summary: "stray double receipt" }),
      ev(4, "agent_error", { error: "stray error" }),
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ status: "ok", endSeq: 2, endSummary: "closed" });
  });

  it("folds in seq order even when the events prop arrives unsorted", () => {
    const rows = deriveRuns([
      ev(2, "agent_done", { summary: "done" }),
      ev(1, "user_message", { text: "goal" }),
    ]);
    expect(rows[0]).toMatchObject({ status: "ok", endSeq: 2 });
  });

  it("lists runs newest-first", () => {
    const rows = deriveRuns([
      ev(1, "user_message", { text: "first" }),
      ev(2, "agent_done", { summary: "s1" }),
      ev(5, "user_message", { text: "second" }),
      ev(6, "agent_done", { summary: "s2" }),
    ]);
    expect(rows.map((r) => r.startSeq)).toEqual([5, 1]);
  });

  it("caps the list at the newest N (default 100)", () => {
    const events = [];
    for (let i = 1; i <= 120; i += 1) {
      events.push(ev(i, "user_message", { text: `goal ${i}` }));
      events.push(ev(1000 + i, "agent_done", { summary: "s" }));
    }
    expect(deriveRuns(events)).toHaveLength(100);
    expect(deriveRuns(events, 3).map((r) => r.startSeq)).toEqual([120, 119, 118]);
  });

  it("carries the send's prompt-byte receipt as the estimate row", () => {
    const rows = deriveRuns([ev(7, "user_message", { text: "goal", total_prompt_bytes: 4096 })]);
    expect(rows[0].promptBytesEst).toBe(4096);
  });

  it("never starts runs on steers or parked goals (steer_queue parity)", () => {
    const rows = deriveRuns([
      ev(1, "user_message", { text: "steered", steer: true }),
      ev(2, "user_message", { text: "queued", park: true }),
      ev(3, "user_message", { text: "   " }),
    ]);
    expect(rows).toEqual([]);
  });

  it("starts a continuation from run_prompt with an origin label", () => {
    const rows = deriveRuns([
      ev(1, "user_message", { text: "original goal" }),
      ev(2, "agent_done", { summary: "done" }),
      ev(9, "review_action", { action: "run_prompt", origin: "continuation", steer_seqs: [4, 5] }),
    ]);
    expect(rows).toHaveLength(2);
    // Newest first: the continuation leads.
    expect(rows[0]).toMatchObject({
      startSeq: 9,
      status: "running",
      origin: "continuation",
      goal: "continuation of seq 4",
    });
  });

  it("labels a parked-goal dequeue from goal_seqs", () => {
    const rows = deriveRuns([
      ev(2, "user_message", { text: "queued goal", park: true }),
      ev(5, "review_action", { action: "run_prompt", origin: "parked_goal", goal_seqs: [2] }),
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0].goal).toBe("parked goal of seq 2");
  });

  it("clips goal text to an 80-char single-line excerpt", () => {
    const long = `  ${"a".repeat(90)}\nsecond line  `;
    const rows = deriveRuns([ev(1, "user_message", { text: long })]);
    expect(rows[0].goal).toBe(`${"a".repeat(80)}…`);
  });

  it("attaches a usage receipt by exact covers_spawn_seq", () => {
    const rows = deriveRuns([
      ev(4, "user_message", { text: "first" }),
      ev(5, "agent_done", { summary: "s" }),
      usageRow(6, {
        kind_run: "fix",
        run_id: "r1",
        covers_spawn_seq: 4,
        usage_available: true,
        input_tokens: 4000,
        output_tokens: 100,
        cache_read_tokens: 9000,
        cache_write_tokens: 200,
        cost_usd: 0.05,
      }),
    ]);
    expect(rows[0].usage).toEqual({ input: 4000, output: 100, cacheRead: 9000, cacheWrite: 200, costUsd: 0.05 });
  });

  it("attaches a covers_spawn_seq:0 receipt to the run open at that seq (fallback)", () => {
    const rows = deriveRuns([
      ev(1, "user_message", { text: "first" }),
      ev(2, "agent_done", { summary: "s1" }),
      ev(3, "user_message", { text: "second" }),
      // The receipt lands while the second run is still open.
      usageRow(4, { kind_run: "implement", run_id: "r2", covers_spawn_seq: 0, usage_available: true, input_tokens: 10, output_tokens: 5 }),
      ev(5, "agent_done", { summary: "s2" }),
    ]);
    expect(rows.find((r) => r.startSeq === 3)?.usage).toEqual({ input: 10, output: 5, cacheRead: 0, cacheWrite: 0 });
    expect(rows.find((r) => r.startSeq === 1)?.usage).toBeUndefined();
  });

  it("falls back to the latest closed run before the receipt when none is open", () => {
    const rows = deriveRuns([
      ev(1, "user_message", { text: "first" }),
      ev(2, "agent_done", { summary: "s1" }),
      ev(3, "user_message", { text: "second" }),
      ev(4, "agent_done", { summary: "s2" }),
      usageRow(6, { kind_run: "fix", run_id: "r3", covers_spawn_seq: 0, usage_available: true, input_tokens: 7, output_tokens: 8 }),
    ]);
    expect(rows.find((r) => r.startSeq === 3)?.usage).toMatchObject({ input: 7, output: 8 });
    expect(rows.find((r) => r.startSeq === 1)?.usage).toBeUndefined();
  });

  it("marks usage_available:false as unavailable with the journaled reason", () => {
    const rows = deriveRuns([
      ev(4, "user_message", { text: "fix" }),
      ev(5, "agent_done", { summary: "s" }),
      usageRow(6, { kind_run: "fix", run_id: "r4", covers_spawn_seq: 4, usage_available: false, reason: "no session transcript" }),
    ]);
    expect(rows[0].usage).toBe("unavailable");
    expect(rows[0].usageReason).toBe("no session transcript");
  });

  it("folds duplicate receipts for one run newest-wins", () => {
    const rows = deriveRuns([
      ev(4, "user_message", { text: "fix" }),
      usageRow(5, { kind_run: "fix", run_id: "r5", covers_spawn_seq: 4, usage_available: true, input_tokens: 1, output_tokens: 1 }),
      usageRow(6, { kind_run: "fix", run_id: "r5", covers_spawn_seq: 4, usage_available: true, input_tokens: 2, output_tokens: 2 }),
    ]);
    expect(rows[0].usage).toMatchObject({ input: 2, output: 2 });
  });

  it("ignores receipts that predate every run", () => {
    const rows = deriveRuns([
      usageRow(2, { kind_run: "fix", run_id: "r6", covers_spawn_seq: 0, usage_available: true, input_tokens: 9, output_tokens: 9 }),
      ev(5, "user_message", { text: "later goal" }),
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0].usage).toBeUndefined();
  });
});
