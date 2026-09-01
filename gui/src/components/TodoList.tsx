import { FormEvent, useState } from "react";
import { Check, Circle, Plus, X } from "lucide-react";
import type { TodoUpdateAction, TodoViewItem } from "../types";
import { todoUpdate } from "../api";
import { cn } from "../lib/utils";

// UX-1 D2 (ux-batch-lock-2026-09-01): the shared todo list body, extracted
// verbatim from PlanChip (TodoGlyph + row + the todo_update mutation
// runner) — ONE row view convention consumed by both the composer Plan
// chip and the Tasks panel tab, so the two surfaces can never drift.
// PlanChip keeps the collapsible chip chrome; TasksPanel adds what the
// chip truncates (full text, stale/swept sections) and reuses everything
// below.
//
// Read path: todo_merge snapshots folded by App's deriveTodoState (the
// journal is the cache). Write path: todo_update IPC (origin: "user").

interface Props {
  conversationId?: number;
  projectRoot?: string | null;
  // The caller-chosen render set, in caller order — the chip passes
  // visibleTodoItems; the panel passes per-section lists (incl. the swept
  // set the chip never renders).
  items: TodoViewItem[];
  onChanged: () => void;
  onError: (message: string) => void;
  disabled?: boolean;
  // Chip rows clip to one line with an ellipsis (composer space
  // contract); the panel sets this to wrap the full text.
  fullText?: boolean;
  // The add affordance renders once per surface (chip body, panel main
  // section) — per-section lists in the panel pass false.
  showAdd?: boolean;
}

// Status glyph for a todo item, matching Hermes's visual language.
// (P1-P4: styles are Tailwind utilities; class names survive as inert
// identity markers.)
export function TodoGlyph({ status, stale }: { status: TodoViewItem["status"]; stale: boolean }) {
  if (status === "done") return <Check size={11} aria-hidden className="todo-glyph todo-glyph-done pointer-events-none text-[var(--ok-text)]" />;
  if (status === "struck") return <X size={11} aria-hidden className="todo-glyph todo-glyph-struck pointer-events-none text-[var(--err)]" />;
  if (stale) return <Circle size={11} aria-hidden className="todo-glyph todo-glyph-stale pointer-events-none opacity-30" />;
  return <Circle size={11} aria-hidden className="todo-glyph todo-glyph-open pointer-events-none opacity-50" />;
}

export default function TodoList({
  conversationId,
  projectRoot,
  items,
  onChanged,
  onError,
  disabled,
  fullText = false,
  showAdd = true,
}: Props) {
  const [draft, setDraft] = useState("");
  // busyId is cleared in run()'s resolve/catch — not by an items effect,
  // because an items-identity change fires on ANY event batch (including
  // unrelated tool-call stream traffic at 350ms), clearing the spinner
  // before the todo_update IPC has been journaled (DSF finding Q6).
  const [busyId, setBusyId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

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

  return (
    <>
      <ul
        className={cn(
          "todo-list list-none m-0 p-0 flex flex-col gap-px",
          // The chip caps and scrolls itself; the panel's .panel-body owns
          // the scrollport, so full lists grow unbounded there.
          !fullText && "max-h-[200px] overflow-y-auto",
        )}
      >
        {items.map((it) => (
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
                "todo-text flex-1 min-w-0",
                fullText
                  ? "whitespace-pre-wrap break-words"
                  : "overflow-hidden text-ellipsis whitespace-nowrap",
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
      {showAdd && (adding ? (
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
      ))}
    </>
  );
}
