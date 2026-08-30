import { describe, expect, it } from "vitest";

// P2.6 (docs/design/adoption-lock.md) — keep-alive LRU park state machine:
// cap-3 keep-alive, LRU eviction badge, draft-exempt protection, order
// determinism, and the immutability/identity guarantees App's React
// integration (memo bails on untouched structures) relies on.

import {
  PARK_CAP,
  activate,
  initialParkState,
  isParked,
  mountedSet,
} from "./lru";
import type { ParkState } from "./lru";

type Tab = "a" | "b" | "c" | "d" | "e" | "x";
const none: ReadonlySet<Tab> = new Set();
const exempt = (...tabs: Tab[]): ReadonlySet<Tab> => new Set(tabs);

// Fold an activation sequence from a seed, the way App folds tab clicks
// into its single ParkState.
const walk = (
  seed: Tab,
  order: readonly Tab[],
  ex: ReadonlySet<Tab> = none,
): ParkState<Tab> => order.reduce((st, t) => activate(st, t, ex), initialParkState<Tab>(seed));

describe("initialParkState", () => {
  it("seeds a single mounted, active tab with an empty badge set", () => {
    const s = initialParkState<Tab>("a");
    expect(s.mru).toEqual(["a"]); // mru is never empty — the seed IS the active tab
    expect(s.parked.size).toBe(0);
    expect(mountedSet(s, none)).toEqual(new Set(["a"]));
    expect(isParked(s, "a")).toBe(false);
  });

  it("treats re-activating the seed as a no-op (same state reference)", () => {
    const s = initialParkState<Tab>("a");
    expect(activate(s, "a", none)).toBe(s);
  });
});

describe("activate: cap enforcement and LRU eviction", () => {
  // PARK_CAP is the P2.4/panel-governance decision — pinned so a change is
  // a deliberate spec revision, not a drive-by.
  it("caps keep-alive at 3 mounted non-exempt tabs", () => {
    expect(PARK_CAP).toBe(3);
  });

  it("parks the least-recent tab once activation depth crosses the cap", () => {
    const s = walk("a", ["b", "c", "d"]);
    expect(s.mru).toEqual(["d", "c", "b", "a"]);
    expect(s.parked).toEqual(new Set(["a"]));
    expect(mountedSet(s, none)).toEqual(new Set(["d", "c", "b"]));
  });

  it("re-activating a parked tab remounts it and evicts the new LRU tail", () => {
    const s1 = walk("a", ["b", "c", "d"]); // [d,c,b,a], a parked
    const s2 = activate(s1, "a", none);
    expect(s2.mru).toEqual(["a", "d", "c", "b"]);
    // a's badge cleared on reactivation; b is now the beyond-cap tail.
    expect(s2.parked).toEqual(new Set(["b"]));
    expect(isParked(s2, "a")).toBe(false);
    expect(mountedSet(s2, none)).toEqual(new Set(["a", "d", "c"]));
  });

  it("keeps every beyond-cap non-exempt tab badged as history grows", () => {
    const s = walk("a", ["b", "c", "d", "e"]); // [e,d,c,b,a]
    expect(s.parked).toEqual(new Set(["b", "a"]));
    expect(mountedSet(s, none)).toEqual(new Set(["e", "d", "c"]));
  });

  it("never badges the active tab, across a mixed park/remount walk", () => {
    let s = initialParkState<Tab>("a");
    for (const t of ["b", "c", "d", "a", "b"] as const) {
      s = activate(s, t, none);
      expect(isParked(s, t)).toBe(false);
      expect(s.mru[0]).toBe(t);
    }
  });
});

describe("draft-exempt tabs (P2.6 Memory/Wiki protection)", () => {
  it("never parks an exempt tab — it mounts outside the cap", () => {
    const s = walk("a", ["b", "c", "d"], exempt("a"));
    expect(s.mru).toEqual(["d", "c", "b", "a"]);
    // a sits beyond cap depth but is exempt: no badge, stays mounted, and
    // d (the newest non-exempt) keeps its in-cap seat — 4 tabs mounted.
    expect(s.parked.size).toBe(0);
    expect(mountedSet(s, exempt("a"))).toEqual(new Set(["a", "b", "c", "d"]));
  });

  it("keeps an exempt tab mounted even when pushed well beyond cap depth", () => {
    const s = walk("a", ["b", "c", "d", "e"], exempt("a")); // [e,d,c,b,a]
    expect(isParked(s, "a")).toBe(false);
    expect(s.parked).toEqual(new Set(["b"])); // b is the deepest non-exempt
    expect(mountedSet(s, exempt("a"))).toEqual(new Set(["e", "d", "c", "a"]));
  });

  it("sweeps a tab out of the badge set when it becomes draft-exempt", () => {
    const s1 = walk("a", ["b", "c", "d"]); // [d,c,b,a], a parked
    const s2 = activate(s1, "d", exempt("a")); // d already active; a newly exempt
    expect(s2).not.toBe(s1);
    expect(s2.mru).toBe(s1.mru); // order untouched → same array reference
    expect(s2.parked.size).toBe(0);
    expect(isParked(s2, "a")).toBe(false);
    expect(mountedSet(s2, exempt("a"))).toEqual(new Set(["a", "d", "c", "b"]));
  });

  it("mounts an exempt tab even before its first activation", () => {
    // Honest ∪ semantics: App only marks tabs it actually knows hold a draft.
    expect(mountedSet(initialParkState<Tab>("a"), exempt("e"))).toEqual(new Set(["a", "e"]));
  });
});

describe("immutability and identity", () => {
  it("never mutates the input state", () => {
    const s1 = walk("a", ["b", "c"]); // [c,b,a]
    const mruBefore = [...s1.mru];
    const parkedBefore = new Set(s1.parked);
    activate(s1, "d", none);
    expect(s1.mru).toEqual(mruBefore);
    expect(s1.parked).toEqual(parkedBefore);
  });

  it("returns the identical state object for a no-op activation at depth 0", () => {
    const s1 = walk("a", ["b", "c", "d"]); // [d,c,b,a], parked {a}
    expect(activate(s1, "d", none)).toBe(s1);
  });

  it("keeps the parked Set reference when a transition changes no badge", () => {
    const s1 = walk("a", ["b", "c", "d"]); // [d,c,b,a], parked {a}
    const s2 = activate(s1, "b", none); // [b,d,c,a], a still the only badge
    expect(s2.mru).toEqual(["b", "d", "c", "a"]);
    expect(s2.mru).not.toBe(s1.mru);
    expect(s2.parked).toBe(s1.parked);
  });
});

describe("order determinism", () => {
  it("activation order fully determines mru order", () => {
    const forward = walk("x", ["a", "b", "c"]);
    const reverse = walk("x", ["c", "b", "a"]);
    expect(forward.mru).toEqual(["c", "b", "a", "x"]);
    expect(reverse.mru).toEqual(["a", "b", "c", "x"]);
    expect(forward.mru).not.toEqual(reverse.mru);
  });
});

describe("untracked tab activation", () => {
  it("inserts a never-mounted tab at the head with nothing parked under cap", () => {
    const s = activate(initialParkState<Tab>("a"), "b", none);
    expect(s.mru).toEqual(["b", "a"]);
    expect(s.parked.size).toBe(0);
    expect(mountedSet(s, none)).toEqual(new Set(["a", "b"]));
    expect(isParked(s, "a")).toBe(false);
  });
});

describe("isParked", () => {
  it("is true only for previously-mounted tabs beyond cap depth", () => {
    const s = walk("a", ["b", "c", "d"]); // [d,c,b,a], parked {a}
    expect(isParked(s, "a")).toBe(true); // evicted tail
    expect(isParked(s, "d")).toBe(false); // active
    expect(isParked(s, "c")).toBe(false); // in-cap
    expect(isParked(s, "e")).toBe(false); // never activated
  });
});
