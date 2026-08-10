import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Check, X } from "lucide-react";
import { visibleTodoItems } from "../todo";
import type { TodoUpdateAction, TodoViewItem } from "../types";
import { todoUpdate } from "../api";

// M12 (D-todo): the composer's "Plan · N open" chip — the GUI surface of
// the journal-backed plan layer. Read path: the todo_merge snapshots in
// the event history (derived by App); write path: the todo_update IPC,
// every user op journaled with origin:"user" by the daemon. The popover
// matches daemon semantics exactly: done/reopen via the checkbox, strike
// (✕) only on open items (retraction with record, never deletion), stale
// items dimmed but never auto-struck.

interface Props {
  conversationId?: number;
  projectRoot?: string | null;
  items: TodoViewItem[];
  // Called after every successful op so the parent re-polls promptly
  // (state returns through the normal poll).
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
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [busyId, setBusyId] = useState<string | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  const visible = useMemo(() => visibleTodoItems(items), [items]);
  const openCount = visible.filter((it) => it.status === "open").length;

  // Click-away closes the popover (the slash menu's blur pattern doesn't
  // fit non-composer buttons).
  useEffect(() => {
    if (!open) return;
    const onDocDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocDown);
    return () => document.removeEventListener("mousedown", onDocDown);
  }, [open]);

  // An action that left the journaled state catches up: release the row
  // spinner when the polled snapshot reflects the click.
  useEffect(() => {
    setBusyId(null);
  }, [items]);

  if (visible.length === 0) return null;

  const run = async (action: TodoUpdateAction, todoId?: string, text?: string) => {
    if (conversationId == null) return;
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
        return;
      }
      onChanged();
    } catch (e) {
      onError(`todo ${action} failed: ${e instanceof Error ? e.message : String(e)}`);
      setBusyId(null);
    }
  };

  const addItem = (e: FormEvent) => {
    e.preventDefault();
    const text = draft.trim();
    if (text === "" || conversationId == null) return;
    setDraft("");
    void run("add", undefined, text);
  };

  return (
    <div className="plan-chip-wrap" ref={rootRef}>
      <button
        type="button"
        className={`auto-distill-chip plan-chip${open ? " open" : ""}`}
        title={
          openCount > 0
            ? `${openCount} open plan item${openCount === 1 ? "" : "s"} — journal-backed, durable across folds`
            : "Plan layer — journal-backed, durable across folds"
        }
        onClick={() => setOpen((v) => !v)}
        disabled={disabled}
      >
        Plan · {openCount} open
      </button>
      {open && (
        <div className="plan-popover" role="dialog" aria-label="Plan items">
          <ul className="plan-list">
            {visible.map((it) => (
              <li
                key={it.id}
                className={`plan-row${it.status === "struck" ? " struck" : ""}${it.stale ? " stale" : ""}${busyId === it.id ? " busy" : ""}`}
              >
                <button
                  type="button"
                  className={`plan-check${it.status === "done" ? " checked" : ""}`}
                  title={it.status === "done" ? "Reopen this item" : "Mark this item done"}
                  aria-label={it.status === "done" ? `Reopen ${it.text}` : `Done ${it.text}`}
                  disabled={busyId != null}
                  onClick={() => void run(it.status === "done" ? "reopen" : "done", it.id)}
                >
                  {it.status === "done" && <Check size={11} />}
                </button>
                <span className="plan-text" title={it.stale ? "Untouched for 3+ folds" : it.status}>
                  {it.text}
                  {it.stale && <span className="plan-stale-mark"> ~stale</span>}
                </span>
                {it.status === "open" && (
                  <button
                    type="button"
                    className="chip-remove plan-strike"
                    title="Strike this item (keeps the record)"
                    aria-label={`Strike ${it.text}`}
                    disabled={busyId != null}
                    onClick={() => void run("strike", it.id)}
                  >
                    <X size={10} />
                  </button>
                )}
              </li>
            ))}
          </ul>
          <form className="plan-add" onSubmit={addItem}>
            <input
              type="text"
              value={draft}
              placeholder="+ add a plan item"
              aria-label="Add a plan item"
              disabled={busyId != null}
              onChange={(e) => setDraft(e.target.value)}
              maxLength={240}
            />
          </form>
        </div>
      )}
    </div>
  );
}
