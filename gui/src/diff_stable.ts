import type { AutoDistillCountdown, Diff, DiffInfoEx, StrandedOp } from "./types";

// (tri-review P1 #4, 2026-08-24) Reference stabilization for the 350 ms
// poll loop: the daemon's JSON deserializes fresh Diff / Diff[] references
// every tick even when nothing changed, and every setState holding a new
// reference re-rendered the whole App subtree (ChatSurface's runGroups
// tree included). The poll handler routes its diff updates through these
// comparators and keeps the previous reference when the content is
// identical, so React's Object.is bailout skips the render entirely on
// quiet ticks — the same pattern App already applies to `preview` and
// `panel_progress`, factored out here for unit tests.
//
// The compared fields are the COMPLETE Diff wire shape (types.ts): id +
// status + path + content. A same-id re-patch with different content
// therefore still lands as a new reference; ordering of the array is
// significant (the daemon's list order is the render order).
//
// (tri-review P2 #5, 2026-08-24) The same stabilization now gates the
// badge-poll payloads (pendingCounts / parkedGoals / runningWorkstreams /
// autoDistill / distillingConvs → refreshPendingCounts' five setters) and
// the review-inbox rows (refreshInbox). Order semantics follow the wire
// source, verified against the daemon:
//   - running_workstreams / distilling_convs / auto_distill are built by
//     snapshotBadgeState (internal/ipc/server.go) iterating Go maps —
//     randomized order per tick — and App consumes them with includes() /
//     find(), never positionally. Those lists compare as MULTISETS, so a
//     jitter-only reorder keeps the previous reference instead of
//     re-rendering every consumer on every tick.
//   - all_pending_diffs is SQL-ordered (store.ListAllPendingDiffs: by
//     workstream id then diff id) and the inbox renders groups in wire
//     order, so it compares element-wise like sameDiffList.

export function sameDiff(a: Diff | null | undefined, b: Diff | null | undefined): boolean {
  if (a == null || b == null) return a === b;
  return (
    a.id === b.id &&
    a.status === b.status &&
    a.path === b.path &&
    a.content === b.content
  );
}

export function sameDiffList(a: Diff[], b: Diff[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (!sameDiff(a[i], b[i])) return false;
  }
  return true;
}
// Per-workstream badge maps (pending_counts / parked_goals): plain
// Records whose JSON object keys the caller converts to numbers. Key SET
// and values must match; enumeration order is irrelevant (and Object.keys
// integer-key order is ascending, never wire-dependent).
export function sameCountMap(a: Record<number, number>, b: Record<number, number>): boolean {
  const aKeys = Object.keys(a);
  if (aKeys.length !== Object.keys(b).length) return false;
  for (const k of aKeys) {
    // A key present only in `a` reads back as undefined from `b` — never
    // equal to a real count, so no separate has-key check is needed.
    if (a[Number(k)] !== b[Number(k)]) return false;
  }
  return true;
}

// Id lists from Go map iteration (running_workstreams, distilling_convs):
// multiset equality — see the module header for why order is noise here.
// Typed string | number so a decimal-literal wire id that arrives as a
// string still compares by set membership ("7" vs 7 stays unequal, which
// is correct: a type flip IS a content flip for the badge consumers).
export function sameIdList(
  a: readonly (number | string)[],
  b: readonly (number | string)[],
): boolean {
  if (a.length !== b.length) return false;
  const tally = new Map<number | string, number>();
  for (const v of a) tally.set(v, (tally.get(v) ?? 0) + 1);
  for (const v of b) {
    const n = tally.get(v);
    if (n == null) return false;
    if (n === 1) tally.delete(v);
    else tally.set(v, n - 1);
  }
  // Equal lengths + no deficit ⇒ every b id is accounted for.
  return true;
}

// Auto-distill countdowns: element-wise on the full badge-consumed shape —
// conversation_id matches an entry to its chip, eta_unix drives the
// countdown, trigger labels it, blocked_reason flips it into the blocked
// disclosure. Unordered (Go map iteration — module header), matched by
// conversation_id, which is unique per wire map.
export function sameAutoDistill(a: AutoDistillCountdown, b: AutoDistillCountdown): boolean {
  return (
    a.conversation_id === b.conversation_id &&
    a.eta_unix === b.eta_unix &&
    a.trigger === b.trigger &&
    a.blocked_reason === b.blocked_reason
  );
}

export function sameAutoDistillList(
  a: AutoDistillCountdown[],
  b: AutoDistillCountdown[],
): boolean {
  if (a.length !== b.length) return false;
  const byId = new Map<number, AutoDistillCountdown>();
  for (const e of a) byId.set(e.conversation_id, e);
  // conversation_id is a Go map key on the wire; a duplicate here means
  // the assumption broke — refuse to claim equality over collapsed data.
  if (byId.size !== a.length) return false;
  for (const e of b) {
    const prev = byId.get(e.conversation_id);
    if (prev == null || !sameAutoDistill(prev, e)) return false;
    // Consume the match (panel review, 2026-08-24): each b entry must
    // disprove a DISTINCT a entry — otherwise b=[{id:x},{id:x}] collapses
    // against the same a entry twice and a's other entries go undenied,
    // silently claiming equality over a dropped real update.
    byId.delete(e.conversation_id);
  }
  return true;
}

// Review-inbox rows: the COMPLETE DiffInfoEx wire shape (types.ts) — the
// Diff fields plus the workstream label and both owner ids. Order-
// sensitive like sameDiffList: the SQL order is the render order.
export function sameDiffInfoEx(a: DiffInfoEx, b: DiffInfoEx): boolean {
  return (
    sameDiff(a, b) &&
    a.workstream_name === b.workstream_name &&
    a.conversation_id === b.conversation_id &&
    a.workstream_id === b.workstream_id
  );
}

export function sameDiffInfoExList(a: DiffInfoEx[], b: DiffInfoEx[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (!sameDiffInfoEx(a[i], b[i])) return false;
  }
  return true;
}

// Stranded crash-recovery rows (2026-08-26 doctrine, round-3 FIX F):
// order-sensitive — the daemon sorts (conversation, layer, seq) and the
// Memory tab renders in that order.
export function sameStrandedOp(a: StrandedOp, b: StrandedOp): boolean {
  return (
    a.layer === b.layer &&
    a.receiptSeq === b.receiptSeq &&
    a.strandedConversation === b.strandedConversation &&
    a.detail === b.detail
  );
}

export function sameStrandedOpsList(a: StrandedOp[], b: StrandedOp[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (!sameStrandedOp(a[i], b[i])) return false;
  }
  return true;
}
