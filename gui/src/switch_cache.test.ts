import { describe, expect, it } from "vitest";
import {
  captureSwitchSnapshot,
  MAX_CACHED_CONVERSATIONS,
  MAX_CACHED_EVENTS,
  mergeEvents,
  rollbackView,
  SwitchCache,
} from "./switch_cache";
import type { OdoEvent } from "./types";

function ev(seq: number): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type: "user_message", payload: {}, created_at: "" };
}

function range(from: number, to: number): OdoEvent[] {
  const out: OdoEvent[] = [];
  for (let s = from; s <= to; s++) out.push(ev(s));
  return out;
}

describe("mergeEvents", () => {
  it("dedupes by seq and sorts the union", () => {
    const prev = range(1, 4);
    const next = [ev(4), ev(6), ev(2), ev(5)];
    expect(mergeEvents(prev, next).map((e) => e.seq)).toEqual([1, 2, 3, 4, 5, 6]);
  });

  it("returns prev untouched when next is empty or fully redundant", () => {
    const prev = range(1, 3);
    expect(mergeEvents(prev, [])).toBe(prev);
    expect(mergeEvents(prev, [ev(2)])).toBe(prev);
  });

  it("restores a truncated tail to the full journal on full-replay merge", () => {
    const cache = new SwitchCache();
    cache.warm("/p", 7, range(1, MAX_CACHED_EVENTS + 10));
    const cached = cache.journal("/p", 7)!;
    expect(cached.truncated).toBe(true);
    expect(cached.events).toHaveLength(MAX_CACHED_EVENTS);
    const full = range(1, MAX_CACHED_EVENTS + 10);
    const merged = mergeEvents(cached.events, full);
    expect(merged.map((e) => e.seq)).toEqual(full.map((e) => e.seq));
  });
});

describe("SwitchCache resolution", () => {
  it("round-trips workstream → conversation and isolates roots", () => {
    const c = new SwitchCache();
    c.record("/a", 1, 101);
    c.record("/b", 1, 201); // same workstream id, other project
    c.warm("/a", 101, range(1, 3));
    c.warm("/b", 201, range(1, 5));
    expect(c.forWorkstream("/a", 1)?.conversationId).toBe(101);
    expect(c.forWorkstream("/b", 1)?.journal.events).toHaveLength(5);
  });

  it("records the default alias only for default-targeted landings", () => {
    const c = new SwitchCache();
    c.record("/a", 1, 101); // name-keyed landings must NOT alias
    expect(c.forDefault("/a")).toBeNull();
    c.record("/a", 2, 202, { defaultTarget: true });
    expect(c.forDefault("/a")).toBeNull(); // resolution without warmed journal
    c.warm("/a", 202, range(1, 2));
    expect(c.forDefault("/a")?.conversationId).toBe(202);
    // Re-targeting the default (rename replaced "main") overwrites, not chains.
    c.record("/a", 3, 303, { defaultTarget: true });
    c.warm("/a", 303, range(1, 2));
    expect(c.forDefault("/a")?.conversationId).toBe(303);
  });

  it("forgets nothing on workstream re-record but needs a warmed journal", () => {
    const c = new SwitchCache();
    c.record("/a", 1, 101);
    expect(c.forWorkstream("/a", 1)).toBeNull(); // resolution without journal
    c.warm("/a", 101, range(1, 2));
    expect(c.forWorkstream("/a", 1)?.journal.lastSeq).toBe(2);
  });
});

describe("SwitchCache LRU + truncation", () => {
  it("evicts the least recently used conversation beyond the cap", () => {
    const c = new SwitchCache();
    for (let i = 1; i <= MAX_CACHED_CONVERSATIONS; i++) c.warm("/a", i, range(1, 1));
    // Refresh conversation 1 so 2 becomes the LRU entry.
    expect(c.journal("/a", 1)).toBeDefined();
    c.warm("/a", MAX_CACHED_CONVERSATIONS + 1, range(1, 1));
    expect(c.journal("/a", 1)).toBeDefined();
    expect(c.journal("/a", 2)).toBeUndefined();
    expect(c.journal("/a", MAX_CACHED_CONVERSATIONS + 1)).toBeDefined();
  });

  it("keeps the full high-water seq on truncation but stores the tail", () => {
    const c = new SwitchCache();
    c.warm("/a", 1, range(1, MAX_CACHED_EVENTS + 50));
    const j = c.journal("/a", 1)!;
    expect(j.truncated).toBe(true);
    expect(j.events[0].seq).toBe(51);
    expect(j.lastSeq).toBe(MAX_CACHED_EVENTS + 50);
  });

  it("does not truncate small journals", () => {
    const c = new SwitchCache();
    c.warm("/a", 1, range(1, 10));
    const j = c.journal("/a", 1)!;
    expect(j.truncated).toBe(false);
    expect(j.events).toHaveLength(10);
  });
});

describe("switch snapshot / rollback", () => {
  it("restores the pre-flip journal with the cached high-water seq", () => {
    const c = new SwitchCache();
    c.warm("/a", 9, range(1, 12));
    const snap = captureSwitchSnapshot(c, "/a", 9, 12);
    const view = rollbackView(snap);
    expect(view.events.map((e) => e.seq)).toEqual(range(1, 12).map((e) => e.seq));
    expect(view.lastSeq).toBe(12);
  });

  it("clamps lastSeq below the tail start so the poll refills the elided middle", () => {
    const c = new SwitchCache();
    c.warm("/a", 9, range(1, MAX_CACHED_EVENTS + 20));
    const snap = captureSwitchSnapshot(c, "/a", 9, MAX_CACHED_EVENTS + 20);
    const view = rollbackView(snap);
    expect(view.events[0].seq).toBe(21);
    expect(view.lastSeq).toBe(20); // tail starts at 21 → refetch from 20
  });

  it("falls back to a full refetch when nothing was cached", () => {
    const snap = captureSwitchSnapshot(new SwitchCache(), "/a", 9, 42);
    const view = rollbackView(snap);
    expect(view.events).toEqual([]);
    expect(view.lastSeq).toBe(0);
  });
});
