// P3.3 (docs/design/adoption-lock.md): the Esc priority registry.
//
// Before this file, Esc handling had ~12 bespoke gates in two leak lanes:
//   (a) App's window keydown ladder — a hardcoded chain of DOM-class
//       querySelector gates (.ws-context-menu/.at-menu/.slash-menu) ahead
//       of search-close → panel-close → agent-cancel → blur;
//   (b) per-component window listeners (the three context menus) with
//       their own stopPropagation, racing App's listener on DOM order.
// Components now DECLARE their Esc consumer here — a priority, an
// optional active predicate, and the close behavior — and ONE window
// listener (App's, and components' React onKeyDown stopPropagation stays
// as the focused-input lane, see below) asks this registry which layer
// owns the keystroke.
//
// Priority ladder (high wins): overlay > menu > panel > global-cancel.
// Within one priority, earliest REGISTERED layer wins (stable insertion
// order — App registers search before panel exactly to reproduce the old
// nested-if order).
//
// Lane contract (unchanged from the pre-registry design):
//   - React onKeyDown handlers in inputs (composer, rename fields,
//     commit editor …) still stopPropagation — a focused Esc never
//     reaches the registry, so the input's own consumer always wins.
//   - Radix layers (Dialog/DropdownMenu/Popover wrappers) keep their
//     capture-phase stopPropagation — they preempt even this registry by
//     construction; do not register them.
//   - This registry covers the UNFOCUSED lane and every consumer that
//     used to be a DOM-class gate or a bare window listener.
import { useEffect, useRef } from "react";

export const ESC_PRIORITY = {
  overlay: 40,
  menu: 30,
  panel: 20,
  global: 10,
} as const;
export type EscPriority = (typeof ESC_PRIORITY)[keyof typeof ESC_PRIORITY];

export interface EscLayer {
  // Debug/test identity. Duplicates are allowed (two menus never share
  // the DOM at once); disposal is by registration, not by id.
  id: string;
  priority: EscPriority;
  // When omitted the layer is active whenever mounted. Mount-gated
  // layers (context menus mount only while open) omit this; state-gated
  // layers (composer menus, App's search/panel/cancel) pass a state read.
  active?: () => boolean;
  onEscape: () => void;
}

interface Entry extends EscLayer {
  // Insertion sequence — the tie-break inside one priority. Registration
  // order must NOT churn when closures update (App's search layer re-reads
  // searchOpenRef every render), so useEscLayer registers once per
  // id+priority and swaps closures in place via the entry object.
  seq: number;
}

// Array, not Map: disposal is by entry identity (duplicate ids legal) and
// the walk is tiny (≲10 entries), so linear scan beats bookkeeping.
const entries: Entry[] = [];
let nextSeq = 0;

// Register a layer; returns the disposer. The layer object is captured by
// reference — live semantics updates flow through useEscLayer's ref, so a
// returned disposer never needs re-issuing on state change.
export function registerEscLayer(layer: EscLayer): () => void {
  const entry: Entry = { ...layer, seq: nextSeq++ };
  entries.push(entry);
  return () => {
    const i = entries.indexOf(entry);
    if (i >= 0) entries.splice(i, 1);
  };
}

// Walk priority-desc, insertion-asc: the topmost ACTIVE layer consumes
// the keystroke (its onEscape runs) and true is returned; nobody active
// → false and the caller may apply its own fallback.
export function dispatchEscape(): boolean {
  const owner = [...entries]
    .sort((a, b) => b.priority - a.priority || a.seq - b.seq)
    .find((e) => (e.active ? e.active() : true));
  if (owner == null) return false;
  owner.onEscape();
  return true;
}

// The hook form: stable registration slot (keyed by id+priority) with the
// semantics swapped in-place every render. Callers pass fresh closures
// freely — re-registration churn REORDERS insertion ties (same priority),
// which the ref swap avoids by never re-registering.
export function useEscLayer(layer: EscLayer): void {
  const ref = useRef(layer);
  useEffect(() => {
    ref.current = layer;
  });
  useEffect(
    () =>
      registerEscLayer({
        id: layer.id,
        priority: layer.priority,
        active: () => ref.current.active == null || ref.current.active(),
        onEscape: () => ref.current.onEscape(),
      }),
    [layer.id, layer.priority],
  );
}

// Test seam: module state survives across tests in one file. Pulling this
// is strictly for isolation in unit tests (panel_overlay.ts seam pattern);
// production code must never call it.
export function __resetEscLayersForTests(): void {
  entries.length = 0;
  nextSeq = 0;
}
