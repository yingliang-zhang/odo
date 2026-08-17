import { FormEvent, useMemo, useState } from "react";
import { Check, X, ChevronDown, ChevronRight, Circle, Plus } from "lucide-react";
import { visibleTodoItems } from "../todo";
import type { TodoUpdateAction, TodoViewItem } from "../types";
import { todoUpdate } from "../api";

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
function TodoGlyph({ status, stale }: { status: TodoViewItem["status"]; stale: boolean }) {
  if (status === "done") return <Check size={11} aria-hidden className="todo-glyph todo-glyph-done" />;
  if (status === "struck") return <X size={11} aria-hidden className="todo-glyph todo-glyph-struck" />;
  if (stale) return <Circle size={11} aria-hidden className="todo-glyph todo-glyph-stale" />;
  return <Circle size={11} aria-hidden className="todo-glyph todo-glyph-open" />;
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
    <div className="todo-stack">
      {/* Header row: caret + label + progress count. Click toggles collapse. */}
      <button
        type="button"
        className="todo-stack-header"
        onClick={() => setCollapsed((v) => !v)}
        aria-expanded={!collapsed}
      >
        {collapsed ? <ChevronRight size={10} aria-hidden /> : <ChevronDown size={10} aria-hidden />}
        <span className="todo-stack-label">{label}</span>
        {progressTotal > 0 && (
          <span className="todo-stack-count">
            {doneCount}/{progressTotal}
          </span>
        )}
      </button>

      {/* Expanded body: item list + add row. */}
      {!collapsed && (
        <>
          <ul className="todo-list">
            {visible.map((it) => (
              <li
                key={it.id}
                className={`todo-row${it.status === "struck" ? " struck" : ""}${it.stale ? " stale" : ""}${busyId === it.id ? " busy" : ""}`}
              >
                <button
                  type="button"
                  className={`todo-check${it.status === "done" ? " checked" : ""}`}
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
                <span className="todo-text" title={it.stale ? "Untouched for 3+ folds" : it.status}>
                  {it.text}
                  {it.stale && <span className="todo-stale-mark"> ~stale</span>}
                </span>
                {it.status === "open" && (
                  <button
                    type="button"
                    className="chip-remove todo-strike"
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
            <form className="todo-add" onSubmit={addItem}>
              <input
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
                className="todo-add-btn"
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
