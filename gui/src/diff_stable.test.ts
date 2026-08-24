import { describe, expect, it } from "vitest";
import { sameAutoDistillList, sameCountMap, sameDiff, sameDiffInfoExList, sameDiffList, sameIdList } from "./diff_stable";
import type { AutoDistillCountdown, Diff, DiffInfoEx } from "./types";

const row = (over: Partial<Diff> = {}): Diff => ({
  id: 7,
  status: "pending",
  path: "src/a.ts",
  content: "--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1 +1 @@",
  ...over,
});

// (tri-review P1 #4, 2026-08-24) These comparators gate every 350 ms
// poll tick's setDiff/setDiffs: a wrong "same" verdict drops a real
// change from the UI, a wrong "different" verdict defeats the render
// stabilization.
describe("sameDiff", () => {
  it("treats null pairs by identity", () => {
    const d = row();
    expect(sameDiff(null, null)).toBe(true);
    expect(sameDiff(d, null)).toBe(false);
    expect(sameDiff(null, d)).toBe(false);
  });

  it("matches distinct references with identical content", () => {
    expect(sameDiff(row(), row())).toBe(true);
  });

  it.each([
    ["id", { id: 8 }],
    ["status", { status: "accepted" as const }],
    ["path", { path: "src/b.ts" }],
    ["content", { content: "@@ -2 +2 @@" }],
  ])("detects a changed %s", (_key, over) => {
    expect(sameDiff(row(), row(over))).toBe(false);
  });
});

describe("sameDiffList", () => {
  it("treats two empty arrays as equal — the hot no-pending-diffs tick", () => {
    expect(sameDiffList([], [])).toBe(true);
  });

  it("matches element-wise equal lists regardless of reference", () => {
    const a = [row(), row({ id: 8, path: "src/b.ts" })];
    expect(sameDiffList(a, a.map((d) => ({ ...d })))).toBe(true);
  });

  it("rejects length changes", () => {
    expect(sameDiffList([row()], [])).toBe(false);
    expect(sameDiffList([], [row()])).toBe(false);
  });

  it("rejects reordering — the daemon's order is the render order", () => {
    const a = row();
    const b = row({ id: 8, path: "src/b.ts" });
    expect(sameDiffList([a, b], [b, a])).toBe(false);
  });

  it("rejects a content rewrite under a same id", () => {
    expect(sameDiffList([row()], [row({ content: "rewritten" })])).toBe(false);
  });
});

// (tri-review P2 #5, 2026-08-24) The badge-poll + inbox comparators gate
// refreshPendingCounts'/refreshInbox's prev-bails: a wrong "same" verdict
// drops a real badge/countdown update from the UI, a wrong "different"
// verdict re-renders every memo'd keep-alive panel on each poll tick.

describe("sameCountMap", () => {
  it("treats two empty maps as equal — the hot no-pending-diffs tick", () => {
    expect(sameCountMap({}, {})).toBe(true);
  });

  it("matches identical content regardless of reference", () => {
    expect(sameCountMap({ 1: 2, 3: 1 }, { 1: 2, 3: 1 })).toBe(true);
  });

  it("matches maps built in any insertion order — integer keys enumerate ascending regardless, values are the content", () => {
    const a: Record<number, number> = { 1: 2, 3: 1 };
    const b: Record<number, number> = {};
    b[3] = 1;
    b[1] = 2;
    expect(sameCountMap(a, b)).toBe(true);
  });

  it("detects a changed count", () => {
    expect(sameCountMap({ 1: 2 }, { 1: 3 })).toBe(false);
  });

  it("detects an added or removed workstream", () => {
    expect(sameCountMap({ 1: 2 }, { 1: 2, 4: 1 })).toBe(false);
    expect(sameCountMap({ 1: 2, 4: 1 }, { 1: 2 })).toBe(false);
    expect(sameCountMap({ 1: 2 }, { 4: 2 })).toBe(false);
  });
});

describe("sameIdList", () => {
  it("treats two empty lists as equal", () => {
    expect(sameIdList([], [])).toBe(true);
  });

  it("matches identical content regardless of reference", () => {
    expect(sameIdList([1, 2], [1, 2])).toBe(true);
  });

  it("ignores reordering — snapshotBadgeState emits Go-map iteration order, and App consumes with includes()", () => {
    expect(sameIdList([1, 2, 3], [3, 1, 2])).toBe(true);
  });

  it("rejects length changes", () => {
    expect(sameIdList([1, 2], [1])).toBe(false);
    expect(sameIdList([1], [1, 2])).toBe(false);
  });

  it("rejects a substituted id", () => {
    expect(sameIdList([1, 2], [1, 3])).toBe(false);
  });

  it("respects multiplicity — [1,2,2] is not [1,1,2]", () => {
    expect(sameIdList([1, 2, 2], [1, 1, 2])).toBe(false);
  });

  it("treats a string/number type flip as a change", () => {
    expect(sameIdList([7], ["7"])).toBe(false);
  });
});

const cd = (over: Partial<AutoDistillCountdown> = {}): AutoDistillCountdown => ({
  conversation_id: 3,
  eta_unix: 1_761_700_000,
  trigger: "window_bytes",
  ...over,
});

describe("sameAutoDistillList", () => {
  it("treats two empty lists as equal — no scheduled auto-distills", () => {
    expect(sameAutoDistillList([], [])).toBe(true);
  });

  it("matches element-wise equal lists regardless of reference", () => {
    const a = [cd(), cd({ conversation_id: 9 })];
    expect(sameAutoDistillList(a, a.map((c) => ({ ...c })))).toBe(true);
  });

  it("ignores reordering — autoPending emits Go-map iteration order, and App consumes with find()", () => {
    const a = cd();
    const b = cd({ conversation_id: 9 });
    expect(sameAutoDistillList([a, b], [{ ...b }, { ...a }])).toBe(true);
  });

  it.each([
    ["conversation_id", { conversation_id: 4 }],
    ["eta_unix", { eta_unix: 1_761_700_605 }],
    ["trigger", { trigger: "window_events" }],
    ["blocked_reason", { blocked_reason: "window over budget", eta_unix: 0 }],
  ])("detects a changed %s", (_key, over) => {
    expect(sameAutoDistillList([cd()], [cd(over)])).toBe(false);
  });

  it("detects a blocked chip resolving back to scheduled", () => {
    const blocked = cd({ blocked_reason: "window over budget", eta_unix: 0 });
    expect(sameAutoDistillList([blocked], [cd()])).toBe(false);
  });

  it("rejects length changes", () => {
    expect(sameAutoDistillList([cd()], [])).toBe(false);
    expect(sameAutoDistillList([], [cd()])).toBe(false);
  });
  // Panel review (2026-08-24): a duplicate conversation_id in EITHER list
  // must refuse equality — conversation_id is a Go map key on the wire, so
  // a dup means the uniqueness assumption broke; claiming "same" over
  // collapsed data would freeze a real badge update.
  it("refuses a duplicate id in the previous list (collapsed map evidence)", () => {
    const a = [cd(), cd()];
    const b = [cd(), cd({ conversation_id: 9 })];
    expect(sameAutoDistillList(a, b)).toBe(false);
  });

  it("refuses a duplicate id in the next list even at equal length and content", () => {
    const a = [cd(), cd({ conversation_id: 9 })];
    const b = [cd(), cd()];
    expect(sameAutoDistillList(a, b)).toBe(false);
  });

  it("refuses when the duplicate twin in the next list drifts", () => {
    const a = [cd(), cd({ conversation_id: 9 })];
    const b = [cd(), cd({ eta_unix: 1_761_700_999 })];
    expect(sameAutoDistillList(a, b)).toBe(false);
  });
});

const ex = (over: Partial<DiffInfoEx> = {}): DiffInfoEx => ({
  ...row(),
  workstream_name: "fix-login",
  conversation_id: 11,
  workstream_id: 2,
  ...over,
});

describe("sameDiffInfoExList", () => {
  it("treats two empty lists as equal — the hot zero-pending inbox", () => {
    expect(sameDiffInfoExList([], [])).toBe(true);
  });

  it("matches element-wise equal lists regardless of reference", () => {
    const a = [ex(), ex({ id: 8, workstream_id: 5, workstream_name: "docs" })];
    expect(sameDiffInfoExList(a, a.map((d) => ({ ...d })))).toBe(true);
  });

  it("rejects reordering — the SQL order (workstream, diff id) is the inbox's render order", () => {
    const a = ex();
    const b = ex({ id: 8, workstream_id: 5, workstream_name: "docs" });
    expect(sameDiffInfoExList([a, b], [b, a])).toBe(false);
  });

  it.each([
    ["id", { id: 8 }],
    ["status", { status: "accepted" as const }],
    ["path", { path: "src/c.ts" }],
    ["content", { content: "rewritten" }],
    ["workstream_name", { workstream_name: "docs" }],
    ["conversation_id", { conversation_id: 12 }],
    ["workstream_id", { workstream_id: 5 }],
  ])("detects a changed %s", (_key, over) => {
    expect(sameDiffInfoExList([ex()], [ex(over)])).toBe(false);
  });

  it("rejects length changes", () => {
    expect(sameDiffInfoExList([ex()], [])).toBe(false);
    expect(sameDiffInfoExList([], [ex()])).toBe(false);
  });
});
