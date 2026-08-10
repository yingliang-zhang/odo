// M12 (D-todo): the GUI's read-side of the durable plan layer. The daemon
// journals one review_action{action:"todo_merge"} per merge carrying the
// full live snapshot; the GUI derives its view from the journaled events
// it already holds (bootstrap replays history, poll appends) — no extra
// IPC for reads, the journal is the only truth. The stale/swept flags are
// recomputed here with the same arithmetic the daemon uses
// (internal/ipc/todo.go: TodoStateFromEvents); the snapshot itself is
// never reinterpreted, only folded with the fold markers.

import type { OdoEvent, TodoViewItem } from "./types";

// Folds that pass an open item's updated_seq before it reads stale —
// mirrors todoStaleFolds daemon-side. A reaffirm (updated_seq bump) resets
// the count; never auto-struck.
const TODO_STALE_FOLDS = 3;

// distillMarkerSeqs returns fold-marker seqs ascending (raw scan; events
// are already seq-ordered so no sort needed).
function distillMarkerSeqs(events: OdoEvent[]): number[] {
  const seqs: number[] = [];
  for (const e of events) {
    if (e.type === "review_action" && e.payload?.action === "distill") {
      seqs.push(e.seq);
    }
  }
  return seqs;
}

// foldBoundary mirrors the daemon's (internal/ipc/recall.go): the newest
// marker's payload last_seq when carried (pinned schema — an update
// journaled in the fold's committed phase, seq in (last_seq, marker_seq),
// sits ABOVE the boundary and must not sweep yet), else the marker's own
// seq (legacy). Max over all markers; events are seq-ordered.
function foldBoundary(events: OdoEvent[]): number {
  let boundary = 0;
  for (const e of events) {
    if (e.type !== "review_action" || e.payload?.action !== "distill") continue;
    const ls = e.payload?.last_seq;
    const b = ls != null && ls > 0 ? ls : e.seq;
    if (b > boundary) boundary = b;
  }
  return boundary;
}

// latestTodoSnapshot returns the newest todo_merge snapshot in id order,
// or null when the conversation has no plan yet.
function latestTodoSnapshot(events: OdoEvent[]): TodoViewItem[] | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.type !== "review_action" || e.payload?.action !== "todo_merge") continue;
    const snap = e.payload.snapshot;
    if (!Array.isArray(snap)) return null;
    const items = snap
      .filter((it) => it && typeof it.id === "string")
      .map((it) => ({
        id: it.id,
        text: it.text ?? "",
        status: (it.status === "done" || it.status === "struck" ? it.status : "open") as TodoViewItem["status"],
        origin_seq: it.origin_seq ?? 0,
        updated_seq: it.updated_seq ?? 0,
        stale: false,
        swept: false,
      }));
    items.sort((a, b) => numericId(a.id) - numericId(b.id));
    return items;
  }
  return null;
}

function numericId(id: string): number {
  const n = Number(id.startsWith("t") ? id.slice(1) : NaN);
  return Number.isFinite(n) ? n : 0;
}

// deriveTodoState folds the journaled events into the GUI's todo view —
// identical derivation to the daemon's TodoStateFromEvents: the latest
// todo_merge snapshot is the item truth; a done/struck item sweeps once
// the fold boundary reaches its updated_seq; an open item reads stale
// after TODO_STALE_FOLDS folds without an update.
export function deriveTodoState(events: OdoEvent[]): TodoViewItem[] {
  const items = latestTodoSnapshot(events);
  if (items == null) return [];
  const markers = distillMarkerSeqs(events);
  const boundary = foldBoundary(events);
  for (const it of items) {
    if (it.status !== "open" && boundary > 0 && it.updated_seq <= boundary) {
      it.swept = true;
    }
    if (it.status === "open" && !it.swept) {
      let folds = 0;
      for (const ms of markers) if (ms > it.updated_seq) folds++;
      it.stale = folds >= TODO_STALE_FOLDS;
    }
  }
  return items;
}

// visibleTodoItems is the default render set (open first, then this
// epoch's done/struck) — mirrors the daemon's visibleTodoItems so the chip
// and the injected block never disagree.
export function visibleTodoItems(items: TodoViewItem[]): TodoViewItem[] {
  const open: TodoViewItem[] = [];
  const closed: TodoViewItem[] = [];
  for (const it of items) {
    if (it.swept) continue;
    if (it.status === "open") open.push(it);
    else closed.push(it);
  }
  return [...open, ...closed];
}