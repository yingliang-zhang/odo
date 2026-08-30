// P2.6 (docs/design/adoption-lock.md, 2026-08-29) — keep-alive LRU park
// for ContextPanel tabs. Today every panel tab mounts lazily on first
// activation and NEVER unmounts (App's mountedTabs Set), so a long session
// accumulates every tab's React subtree. P2.4 caps keep-alive at PARK_CAP:
// the active tab plus the most-recently-activated others stay mounted;
// deeper tabs unmount (their React state dies) and their tab button shows
// a parked badge until re-activation, which remounts and refetches through
// the existing `active`-prop activation-edge contract. Memory/Wiki drafts
// are exempt: App knows which tabs hold unsaved input and passes that set
// in; exempt tabs are never parked and mount OUTSIDE the cap.
//
// This module is the pure decision core (test seam, same pattern as
// panel_overlay.ts): an immutable state machine over tab ids with no
// React/DOM knowledge. App holds one ParkState per panel, folds every tab
// activation through `activate`, and derives mount/badge decisions from
// `mountedSet`/`isParked`. Generic over the PanelTab union so the machine
// never learns about specific tabs.

export const PARK_CAP = 3;

export interface ParkState<T extends string> {
  // Every tab activated this session, most-recent-first. mru[0] is the
  // active tab; activation inserts never-before-seen tabs at the head.
  readonly mru: readonly T[];
  // Previously-mounted tabs currently unmounted (drives the tab badge).
  // `activate` maintains the invariant parked = the non-exempt tabs at
  // mru depth ≥ PARK_CAP — it never contains the active tab, and never a
  // draft-exempt one.
  readonly parked: ReadonlySet<T>;
}

// Seed state for a panel whose first tab has just activated: that tab is
// mounted and active, and nothing is parked. The seed is what guarantees
// mru can never be empty — a panel always has exactly one active tab.
export function initialParkState<T extends string>(seed: T): ParkState<T> {
  return { mru: [seed], parked: new Set<T>() };
}

// Tabs whose React subtree must stay mounted: the PARK_CAP most recent of
// mru PLUS every draft-exempt tab. Honest semantics — the cap bounds the
// number of mounted NON-EXEMPT tabs only; exempt tabs are always mounted,
// outside the cap (a session with three drafts open mounts PARK_CAP + 3).
export function mountedSet<T extends string>(
  s: ParkState<T>,
  draftExempt: ReadonlySet<T>,
): ReadonlySet<T> {
  return new Set<T>([...s.mru.slice(0, PARK_CAP), ...draftExempt]);
}

// Fold one activation: `tab` moves to the mru head (inserted if never
// mounted), its badge clears, and every non-exempt tab beyond PARK_CAP
// depth parks. The newly active tab sits at depth 0, so the park sweep —
// which starts at PARK_CAP — can never touch it. Exempt tabs are also swept
// OUT of the badge set, which keeps the invariant honest when App's
// exemption knowledge changes between activations. Untouched pieces keep
// identity so React bails out: a no-op activation returns the same state
// object, and a move that changes no badge keeps the same `parked` Set.
export function activate<T extends string>(
  s: ParkState<T>,
  tab: T,
  draftExempt: ReadonlySet<T>,
): ParkState<T> {
  const mru =
    s.mru[0] === tab ? s.mru : [tab, ...s.mru.filter((t) => t !== tab)];

  const swept = new Set<T>();
  for (let i = PARK_CAP; i < mru.length; i++) {
    if (!draftExempt.has(mru[i])) swept.add(mru[i]);
  }
  const parked = sameMembers(swept, s.parked) ? s.parked : swept;

  return mru === s.mru && parked === s.parked ? s : { mru, parked };
}

// Whether `tab` is currently unmounted with a parked badge pending.
export function isParked<T extends string>(s: ParkState<T>, tab: T): boolean {
  return s.parked.has(tab);
}

function sameMembers<T>(a: ReadonlySet<T>, b: ReadonlySet<T>): boolean {
  if (a.size !== b.size) return false;
  for (const v of a) if (!b.has(v)) return false;
  return true;
}
