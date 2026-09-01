// UX-1 D2 (docs/design/ux-batch-lock-2026-09-01): the ContextPanel "Tasks"
// tab body — the plan layer's always-on surface where the composer Plan
// chip's compact stack stops (chip truncates to ellipsis rows over
// visibleTodoItems; this panel wraps full text and adds stale/swept
// sections). Journal SSOT: App folds deriveTodoState ONCE per poll tick
// and hands the same items to the chip and here — zero new IPC, the
// journal is the cache (RunsPanel precedent). Write path: todo_update
// (origin: "user") via the shared TodoList mutation runner — one row view
// convention, chip and panel cannot drift.
//
// Keep-alive contract (RunsPanel posture): App mounts this once behind
// the `active` flag with referentially stable props, so an off-tab poll
// tick skips via the memo and no activation refetch ever exists — the
// derive is live by construction.
//
// Single-occurrence invariant: every derived item renders in exactly ONE
// section — live (visible non-stale: fresh opens first, then this epoch's
// done/struck), stale (visible open items untouched 3+ folds), swept
// (closed items the fold boundary has passed; the chip never shows these).

import { memo } from "react";
import type { TodoViewItem } from "../types";
import { visibleTodoItems } from "../todo";
import TodoList from "./TodoList";

interface Props {
  conversationId?: number;
  // Panel-tab wiring contract (same shape as LedgerPanel/RunsPanel): the
  // fold is conversation-scoped and App owns the daemon route.
  projectRoot: string | null;
  // App's lifted deriveTodoState output — the SAME array reference the
  // Plan chip reads; zero re-derivation here.
  items: TodoViewItem[];
  // Ops poke the prompt re-poll (todo ops journal immediately).
  onChanged: () => void;
  onError: (message: string) => void;
  // Keep-alive activation edge: declared for the mounting contract —
  // nothing to refetch on activation (events-derived).
  active: boolean;
  // Same op-disable semantics as the chip (boot gate + manual distill
  // lock) — App passes the identical expression.
  disabled?: boolean;
}

function TasksPanel({ conversationId, projectRoot, items, onChanged, onError, disabled }: Props) {
  const visible = visibleTodoItems(items);
  const live = visible.filter((it) => !it.stale);
  const stale = visible.filter((it) => it.stale);
  const swept = items.filter((it) => it.swept);
  const listProps = { conversationId, projectRoot, onChanged, onError, disabled };
  return (
    <div className="mem-body">
      {items.length === 0 && (
        <div className="panel-empty">No plan items yet — the agent's plan or your adds land here.</div>
      )}
      {(live.length > 0 || stale.length > 0 || swept.length > 0) && (
        <div className="mem-section-title">
          {`plan · ${live.filter((it) => it.status === "open").length + stale.length} open`}
        </div>
      )}
      {/* Main section carries the add op (one per surface, TodoList showAdd). */}
      <TodoList {...listProps} items={live} fullText />
      {stale.length > 0 && (
        <div className="tasks-stale-section mt-3">
          <div className="mem-section-title">stale — open, untouched for 3+ folds</div>
          <TodoList {...listProps} items={stale} fullText showAdd={false} />
        </div>
      )}
      {swept.length > 0 && (
        <div className="tasks-swept-section mt-3">
          <div className="mem-section-title">swept — closed and folded away · reopening returns an item to the plan</div>
          <TodoList {...listProps} items={swept} fullText showAdd={false} />
        </div>
      )}
    </div>
  );
}

// Keep-alive panel (RunsPanel/LedgerPanel convention): App hands only
// referentially stable props to the mounted-off-tab subtree, so the
// default shallow compare skips quiet poll ticks — no custom comparator.
export default memo(TasksPanel);
