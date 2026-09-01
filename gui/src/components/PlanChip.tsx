import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { visibleTodoItems } from "../todo";
import type { TodoViewItem } from "../types";
import TodoList from "./TodoList";

// Hermes-style inline todo stack — renders above the composer as a
// collapsible card showing live todo items with status glyphs:
//   open → hollow circle (dimmed), done → ✓, struck → ✕ with strikethrough.
// Unlike the old PlanChip popover, this is always visible (when items
// exist) and updates in real-time from the journaled events.
//
// Read path: todo_merge snapshots folded by App's deriveTodoState and
// handed down as `items`. Write path: todo_update IPC (origin: "user") —
// shared with the Tasks panel tab via TodoList (UX-1 D2): this file keeps
// ONLY the collapsible chip chrome, the row/mutation convention lives once
// in TodoList so chip and panel cannot drift.

interface Props {
  conversationId?: number;
  projectRoot?: string | null;
  items: TodoViewItem[];
  onChanged: () => void;
  onError: (message: string) => void;
  disabled?: boolean;
}

export default function PlanChip({
  conversationId,
  projectRoot,
  items,
  onChanged,
  onError,
  disabled,
}: Props) {
  const [collapsed, setCollapsed] = useState(false);

  const visible = useMemo(() => visibleTodoItems(items), [items]);
  const openCount = visible.filter((it) => it.status === "open").length;
  const doneCount = visible.filter((it) => it.status === "done").length;
  // Progress denominator excludes struck (struck can never become done,
  // so including it makes the fraction unreachable — DSF finding Q4).
  const progressTotal = openCount + doneCount;

  // Don't render at all when there's no conversation or no items — the
  // Tasks tab is the always-on surface for an empty plan (UX-1 D2).
  if (conversationId == null) return null;
  if (visible.length === 0) return null;

  const label = openCount > 0
    ? `Plan · ${openCount} open`
    : progressTotal > 0
      ? `Plan · all done (${doneCount})`
      : "Plan";

  return (
    <div className="todo-stack flex flex-col gap-0.5 py-1 px-2 bg-[var(--bg-raised)] rounded-t-lg border-b border-[var(--border)]">
      {/* Header row: caret + label + progress count. Click toggles collapse. */}
      <button
        type="button"
        className="todo-stack-header flex items-center gap-1.5 bg-transparent border-none text-[var(--text-dim)] text-[11px] font-medium cursor-pointer py-0.5 w-full text-left hover:text-[var(--text)]"
        onClick={() => setCollapsed((v) => !v)}
        aria-expanded={!collapsed}
      >
        {collapsed ? <ChevronRight size={10} aria-hidden /> : <ChevronDown size={10} aria-hidden />}
        <span className="todo-stack-label flex-1">{label}</span>
        {progressTotal > 0 && (
          <span className="todo-stack-count tabular-nums text-[var(--text-dim)] text-[10px]">
            {doneCount}/{progressTotal}
          </span>
        )}
      </button>

      {/* Expanded body: the shared list (rows + add row) capped at 200px. */}
      {!collapsed && (
        <TodoList
          conversationId={conversationId}
          projectRoot={projectRoot}
          items={visible}
          onChanged={onChanged}
          onError={onError}
          disabled={disabled}
        />
      )}
    </div>
  );
}
