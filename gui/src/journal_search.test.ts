import { describe, expect, it } from "vitest";

// P1.1 (docs/design/adoption-lock.md): palette journal-search support —
// hit tagging/merge order and the row snippet window.

import { JOURNAL_HIT_CAP, mergeHits, snippetFor, type JournalHit } from "./journal_search";
import type { SearchResult } from "./types";

function result(seq: number, text: string, createdAt: string, ws = "main"): SearchResult {
  return {
    event: {
      id: seq,
      conversation_id: 1,
      seq,
      type: "user_message",
      payload: { text },
      created_at: createdAt,
    },
    workstream_id: 1,
    workstream_name: ws,
    conversation_id: 1,
  };
}

function hit(root: string, name: string, r: SearchResult): JournalHit {
  return { root, projectName: name, result: r };
}

describe("journal search helpers (P1.1)", () => {
  it("mergeHits orders newest-first across projects and caps the group", () => {
    const a = [hit("/r1", "odo", result(1, "older", "2026-08-20T10:00:00Z")), hit("/r1", "odo", result(3, "newest", "2026-08-28T10:00:00Z"))];
    const b = [hit("/r2", "splat", result(2, "middle", "2026-08-24T10:00:00Z", "feat"))];
    const merged = mergeHits([a, b]);
    expect(merged.map((m) => m.result.event.seq)).toEqual([3, 2, 1]);
    expect(merged[1].projectName).toBe("splat");
    expect(merged[1].result.workstream_name).toBe("feat");
  });

  it("mergeHits caps at JOURNAL_HIT_CAP", () => {
    const many = Array.from({ length: JOURNAL_HIT_CAP + 5 }, (_, i) =>
      hit("/r1", "odo", result(i + 1, `row ${i}`, new Date(2026, 0, i + 1).toISOString())),
    );
    expect(mergeHits([many])).toHaveLength(JOURNAL_HIT_CAP);
    // …and the cap keeps the NEWEST rows, not the first-appended.
    expect(mergeHits([many])[0].result.event.seq).toBe(JOURNAL_HIT_CAP + 5);
  });

  it("snippetFor prefers the payload's prose field and windows the match", () => {
    const body = `${"lorem ipsum dolor sit amet ".repeat(4)}fold markers${" consectetur adipiscing".repeat(4)}`;
    const s = snippetFor({ text: body }, "fold markers");
    expect(s).toContain("fold markers");
    expect(s.startsWith("…")).toBe(true);
    expect(s.endsWith("…")).toBe(true);
    expect(s.length).toBeLessThan(body.length);
  });

  it("snippetFor flattens whitespace and passes short bodies through", () => {
    expect(snippetFor({ text: "short\nmultiline body" }, "multiline")).toBe("short multiline body");
  });

  it("snippetFor falls back to JSON for payload without prose fields", () => {
    const s = snippetFor({ diff_id: 3, action: "accept" }, "accept");
    expect(s).toContain('"accept"');
  });

  it("snippetFor tolerates an empty payload and an absent match", () => {
    expect(snippetFor(undefined, "x")).toBe("{}");
    // No index hit → deterministic head truncation past the 2×radius cap.
    const long = "no match anywhere in this body — it just runs past the eighty-character cap so the row gets ellipsized";
    expect(snippetFor({ text: long }, "zzz")).toBe(long.slice(0, 80) + "…");
  });
});
