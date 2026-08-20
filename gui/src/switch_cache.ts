import type { OdoEvent } from "./types";

// Seq-keyed union over two journal slices. The journal is append-only per
// conversation and seqs restart per conversation, so this is lossless
// exactly when both slices belong to the SAME conversation — callers
// guarantee that (poll captures (cid, root) at fire time and discards
// stale responses; bootstrap merges only when ids match).
export function mergeEvents(prev: OdoEvent[], next: OdoEvent[]): OdoEvent[] {
  if (next.length === 0) return prev;
  const seen = new Set(prev.map((e) => e.seq));
  const fresh = next.filter((e) => !seen.has(e.seq));
  if (fresh.length === 0) return prev;
  return [...prev, ...fresh].sort((a, b) => a.seq - b.seq);
}

// Stale-while-revalidate switch cache: a repeat project/workstream switch
// renders its cached journal synchronously, the authoritative bootstrap
// merges its tail on landing, and a failure restores the pre-flip view.
//
// Two maps keyed by `${root}\0…` so workstream ids (which restart per
// project) never collide across projects.

// Bounds: LRU over conversations keeps memory bounded; per-conversation
// truncation bounds a single heavyweight journal (months of agent_output
// payloads). Both bounds drop references only — the daemon's journal is
// the source of truth and refills anything an eviction cost us.
export const MAX_CACHED_CONVERSATIONS = 24;
export const MAX_CACHED_EVENTS = 500;

export interface CachedJournal {
  // The rendered slice: the full journal, or the newest MAX_CACHED_EVENTS
  // rows when truncated.
  events: OdoEvent[];
  // High-water seq of the FULL journal (not of the stored tail) — the
  // poll loop's afterSeq cursor and the bootstrap tail hint both read it.
  lastSeq: number;
  // True when events holds only a tail: bootstrap must replay fully (the
  // afterSeq hint would silently elide the middle), but mergeEvents on
  // landing still restores the complete journal from the full replay.
  truncated: boolean;
}

// Target of an optimistic (cached) switch.
export interface FlipTarget {
  conversationId: number;
  journal: CachedJournal;
}

// Everything a failed switch must restore, captured synchronously at the
// moment of the flip (closure state would be stale by the time the catch
// runs — the root-compare must read these refs, not component state).
export interface SwitchSnapshot {
  root: string | null;
  conversationId: number | null;
  lastSeq: number;
  journal: CachedJournal | null;
}

export function captureSwitchSnapshot(
  cache: SwitchCache,
  root: string | null,
  conversationId: number | null,
  lastSeq: number,
): SwitchSnapshot {
  return {
    root,
    conversationId,
    lastSeq,
    journal:
      root != null && conversationId != null
        ? (cache.journal(root, conversationId) ?? null)
        : null,
  };
}

// The view a failed switch restores. lastSeq clamps so the poll loop
// refetches anything the snapshot cannot show:
// - truncating snapshot → below the tail start (the elided middle refills;
//   mergeEvents dedupes the overlap)
// - missing snapshot (never observed post-boot, but cheap to make safe) →
//   0, a full refetch
export function rollbackView(snap: SwitchSnapshot): { events: OdoEvent[]; lastSeq: number } {
  const journal = snap.journal;
  if (journal == null) return { events: [], lastSeq: 0 };
  if (journal.truncated && journal.events.length > 0) {
    return { events: journal.events, lastSeq: Math.min(snap.lastSeq, journal.events[0].seq - 1) };
  }
  return { events: journal.events, lastSeq: Math.min(snap.lastSeq, journal.lastSeq) };
}

export class SwitchCache {
  // Insertion-ordered (LRU) conversation journals: root\0cid → snapshot.
  private readonly journals = new Map<string, CachedJournal>();
  // Stable workstream→conversation resolution plus the project-default
  // alias (bootstrap is the only production creator of conversations, and
  // only when none is active, so a recorded mapping stays valid; an epoch
  // fold replaces the id, self-healing on the next landing).
  private readonly resolutions = new Map<string, number>();

  private static convKey(root: string, conversationId: number): string {
    return `${root}\0${conversationId}`;
  }
  private static wsKey(root: string, workstreamId: number): string {
    return `${root}\0ws:${workstreamId}`;
  }
  private static defaultKey(root: string): string {
    return `${root}\0default`;
  }

  // record is called from bootstrap landings. `defaultTarget` records the
  // project's default alias — TRUE only when the bootstrap request carried
  // no workstream id (the daemon resolved its own default), so a renamed
  // "main" can never leave the alias keyed to a stale name.
  record(
    root: string,
    workstreamId: number,
    conversationId: number,
    opts?: { defaultTarget?: boolean },
  ): void {
    this.resolutions.set(SwitchCache.wsKey(root, workstreamId), conversationId);
    if (opts?.defaultTarget) {
      this.resolutions.set(SwitchCache.defaultKey(root), conversationId);
    }
  }

  // warm stores the currently rendered journal (bootstrap replaces, merges
  // and poll appends all flow through it). Touching the key refreshes its
  // LRU position.
  warm(root: string, conversationId: number, events: OdoEvent[]): void {
    const key = SwitchCache.convKey(root, conversationId);
    this.journals.delete(key); // refresh insertion order (LRU touch)
    const truncated = events.length > MAX_CACHED_EVENTS;
    const lastSeq = events.reduce((m, e) => Math.max(m, e.seq), 0);
    this.journals.set(key, {
      events: truncated ? events.slice(-MAX_CACHED_EVENTS) : events,
      lastSeq,
      truncated,
    });
    // Eviction is FIFO over insertion order = true LRU because every get
    // below re-inserts. Drops references only.
    while (this.journals.size > MAX_CACHED_CONVERSATIONS) {
      this.journals.delete(this.journals.keys().next().value as string);
    }
  }

  // journal looks a snapshot up and refreshes its LRU position (a switch
  // to it IS a use).
  journal(root: string, conversationId: number): CachedJournal | undefined {
    const key = SwitchCache.convKey(root, conversationId);
    const hit = this.journals.get(key);
    if (hit != null) {
      this.journals.delete(key);
      this.journals.set(key, hit);
    }
    return hit;
  }

  forWorkstream(root: string, workstreamId: number): FlipTarget | null {
    const cid = this.resolutions.get(SwitchCache.wsKey(root, workstreamId));
    if (cid == null) return null;
    const journal = this.journal(root, cid);
    return journal != null ? { conversationId: cid, journal } : null;
  }

  forDefault(root: string): FlipTarget | null {
    const cid = this.resolutions.get(SwitchCache.defaultKey(root));
    if (cid == null) return null;
    const journal = this.journal(root, cid);
    return journal != null ? { conversationId: cid, journal } : null;
  }
}
