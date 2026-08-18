import { FormEvent, useMemo, useState } from "react";
import { Check, X, ChevronDown, ChevronRight, Circle, Plus } from "lucide-react";
import { visibleTodoItems } from "../todo";
import type { TodoUpdateAction, TodoViewItem } from "../types";
import { todoUpdate } from "../api";
import { cn } from "../lib/utils";

// Hermes-style inline todo stack — renders above the composer as a
// collapsible card showing live todo items with status glyphs:
//   open → hollow circle (dimmed), done → ✓, struck → ✕ with strikethrough.
// Unlike the old PlanChip popover, this is always visible (when items
// exist) and updates in real-time from the journaled events.
//
// Read path: todo_merge snapshots in event history (derived by App via
// deriveTodoState). Write path: todo_update IPC (origin: "user").

interface Props {
  conversationId?: number;
  projectRoot?: string | null;
  items: TodoViewItem[];
  onChanged: () => void;
  onError: (message: string) => void;
  disabled?: boolean;
}

// Status glyph for a todo item, matching Hermes's visual language.
// (P1-P4: styles are Tailwind utilities; class names survive as inert
// identity markers.)
function TodoGlyph({ status, stale }: { status: TodoViewItem["status"]; stale: boolean }) {
  if (status === "done") return <Check size={11} aria-hidden className="todo-glyph todo-glyph-done pointer-events-none text-[var(--ok-text)]" />;
  if (status === "struck") return <X size={11} aria-hidden className="todo-glyph todo-glyph-struck pointer-events-none text-[var(--err)]" />;
  if (stale) return <Circle size={11} aria-hidden className="todo-glyph todo-glyph-stale pointer-events-none opacity-30" />;
  return <Circle size={11} aria-hidden className="todo-glyph todo-glyph-open pointer-events-none opacity-50" />;
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
  const [draft, setDraft] = useState("");
  // busyId is cleared in run()'s resolve/catch — not by an items effect,
  // because an items-identity change fires on ANY event batch (including
  // unrelated tool-call stream traffic at 350ms), clearing the spinner
  // before the todo_update IPC has been journaled (DSF finding Q6).
  const [busyId, setBusyId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const visible = useMemo(() => visibleTodoItems(items), [items]);
  const openCount = visible.filter((it) => it.status === "open").length;
  const doneCount = visible.filter((it) => it.status === "done").length;
  // Progress denominator excludes struck (struck can never become done,
  // so including it makes the fraction unreachable — DSF finding Q4).
  const progressTotal = openCount + doneCount;

  // Don't render at all when there's no conversation or no items AND not adding.
  if (conversationId == null) return null;
  if (visible.length === 0 && !adding) return null;

  const run = async (action: TodoUpdateAction, todoId?: string, text?: string): Promise<boolean> => {
    if (conversationId == null) return false;
    setBusyId(todoId ?? "add");
    try {
      const resp = await todoUpdate(conversationId, action, {
        todoId,
        text,
        projectRoot: projectRoot ?? undefined,
      });
      if (!resp.ok) {
        onError(resp.error ?? `todo ${action} failed`);
        setBusyId(null);
        return false;
      }
      onChanged();
      setBusyId(null);
      return true;
    } catch (e) {
      onError(`todo ${action} failed: ${e instanceof Error ? e.message : String(e)}`);
      setBusyId(null);
      return false;
    }
  };

  const addItem = (e: FormEvent) => {
    e.preventDefault();
    const text = draft.trim();
    if (text === "" || conversationId == null) return;
    setBusyId("add");
    void run("add", undefined, text).then((ok) => {
      if (ok) {
        setDraft("");
        setAdding(false);
      }
      // On failure: keep draft + adding state so the user can retry (GLM Bug A).
    });
  };

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

      {/* Expanded body: item list + add row. */}
      {!collapsed && (
        <>
          <ul className="todo-list list-none m-0 p-0 flex flex-col gap-px max-h-[200px] overflow-y-auto">
            {visible.map((it) => (
              <li
                key={it.id}
                className={cn(
                  "todo-row group flex items-center gap-2 px-1 py-[3px] rounded text-[12px] leading-[1.4] hover:bg-[rgba(255,255,255,0.03)]",
                  it.status === "struck" && "struck",
                  it.stale && "stale",
                  busyId === it.id && "busy opacity-[0.55] pointer-events-none",
                )}
              >
                <button
                  type="button"
                  className={cn(
                    "todo-check flex items-center justify-center w-4 h-4 border-none bg-transparent cursor-pointer p-0 shrink-0",
                    it.status === "done"
                      ? "checked text-[var(--ok-text)]"
                      : "text-[var(--text-dim)] hover:text-[var(--text)]",
                  )}
                  title={
                    it.status === "done"
                      ? "Reopen this item"
                      : it.status === "struck"
                        ? "Struck items are closed — reopen via a new item"
                        : "Mark this item done"
                  }
                  aria-label={
                    it.status === "done"
                      ? `Reopen ${it.text}`
                      : it.status === "struck"
                        ? `${it.text} (struck)`
                        : `Done ${it.text}`
                  }
                  disabled={busyId != null || it.status === "struck" || disabled}
                  onClick={() => void run(it.status === "done" ? "reopen" : "done", it.id)}
                >
                  <TodoGlyph status={it.status} stale={it.stale} />
                </button>
                <span
                  className={cn(
                    "todo-text flex-1 min-w-0 overflow-hidden text-ellipsis whitespace-nowrap",
                    it.status === "struck" && "line-through",
                    (it.status === "struck" || it.stale) && "text-[var(--text-dim)]",
                  )}
                  title={it.stale ? "Untouched for 3+ folds" : it.status}
                >
                  {it.text}
                  {it.stale && <span className="todo-stale-mark text-[var(--text-dim)] text-[10px] ml-1"> ~stale</span>}
                </span>
                {it.status === "open" && (
                  <button
                    type="button"
                    className="chip-remove todo-strike flex items-center bg-transparent border-none text-[var(--text-dim)] py-0.5 px-1 opacity-0 transition-opacity duration-150 [transition-timing-function:ease] cursor-pointer group-hover:opacity-100 focus-visible:opacity-100 hover:text-[var(--err)]"
                    title="Strike this item (keeps the record)"
                    aria-label={`Strike ${it.text}`}
                    disabled={busyId != null || disabled}
                    onClick={() => void run("strike", it.id)}
                  >
                    <X size={10} aria-hidden />
                  </button>
                )}
              </li>
            ))}
          </ul>
          {adding ? (
            <form className="todo-add flex py-0.5 px-1" onSubmit={addItem}>
              <input
                className="flex-1 bg-[var(--bg-input)] border border-[var(--border)] rounded text-[var(--text)] text-[12px] py-1 px-2 outline-none focus:border-[var(--accent)]"
                type="text"
                value={draft}
                placeholder="Add a plan item…"
                aria-label="Add a plan item"
                disabled={busyId != null}
                autoFocus
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.stopPropagation();
                    setAdding(false);
                    setDraft("");
                  }
                }}
                maxLength={240}
              />
            </form>
          ) : (
            !disabled && (
              <button
                type="button"
                className="todo-add-btn flex items-center gap-1 bg-transparent border-none text-[var(--text-dim)] text-[11px] cursor-pointer py-0.5 px-1 hover:text-[var(--text)]"
                onClick={() => setAdding(true)}
                disabled={busyId != null}
              >
                <Plus size={10} aria-hidden /> add
              </button>
            )
          )}
        </>
      )}
    </div>
  );
}
