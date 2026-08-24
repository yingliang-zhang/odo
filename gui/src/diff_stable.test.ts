import { describe, expect, it } from "vitest";
import { sameDiff, sameDiffList } from "./diff_stable";
import type { Diff } from "./types";

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
