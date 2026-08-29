import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { Dialog, DialogContent } from "./ui/dialog";
import { snippetFor, type JournalHit } from "../journal_search";
import { SLOT } from "../slots";

// Belt B (⌘K): fuzzy action launcher over a Radix Dialog (Phase 5).
// Two modes: the action list (filter by substring, arrows navigate, Enter
// runs) and prompt mode for actions that need a text argument (New
// Workstream name, Pin text) — Enter submits the text.
// P1.1: a typed-only "Journal search" group joins the action list whenever
// the query reaches 2 chars — App owns the search_events fan-out and hands
// results down as props; the palette only renders rows and reports picks
// (read-only; the journal stays the only index).

export interface PaletteAction {
  id: string;
  name: string;
  icon?: ReactNode;
  // Right-aligned hint (e.g. "⌘B"); display only.
  shortcut?: string;
  // Shown greyed-out and skipped by keyboard navigation.
  disabled?: boolean;
  // When set, selecting the action switches to text-entry mode and Enter
  // hands the trimmed text to onRun.
  prompt?: string;
  onRun: (text: string) => void | Promise<unknown>;
}

interface Props {
  actions: PaletteAction[];
  onClose: () => void;
  // M11 D2: when set, the palette opens straight into prompt mode for this
  // action (⌘N → new-workstream name entry).
  initialActionId?: string;
  // P1.1 journal group props (all optional; absent = group never renders).
  // Called with every query change (raw, untrimmed); the parent debounces.
  onQueryChange?: (query: string) => void;
  // Results for the current query; null = nothing returned yet. An empty
  // array is a completed empty search (renders "No journal matches").
  journal?: JournalHit[] | null;
  journalLoading?: boolean;
  // Enter/click on a journal row; the trimmed query rides along so App can
  // prefill ⌘F with it.
  onPickJournal?: (hit: JournalHit, query: string) => void;
}

// Handlers surface failures through App's error banner; the palette just
// has to not leak unhandled rejections or sync throws.
function execute(action: PaletteAction, text: string) {
  try {
    const r = action.onRun(text);
    if (r instanceof Promise) r.catch(() => {});
  } catch {
    /* surfaced by the handler's own banner */
  }
}

export default function CommandPalette({
  actions,
  onClose,
  initialActionId,
  onQueryChange,
  journal = null,
  journalLoading = false,
  onPickJournal,
}: Props) {
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState(0);
  const [promptFor, setPromptFor] = useState<PaletteAction | null>(null);
  const [promptText, setPromptText] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q === "" ? actions : actions.filter((a) => a.name.toLowerCase().includes(q));
  }, [actions, query]);

  // The journal group is typed-only: under 2 chars the daemon never sees
  // the keystrokes, and prompt mode is action-argument entry, not search.
  const journalActive = promptFor === null && query.trim().length >= 2;
  const journalHits = journalActive ? (journal ?? []) : [];

  // One selection index spans actions then journal rows. Disabled actions
  // stay non-selectable; journal rows never disable.
  type Entry = { kind: "action"; action: PaletteAction } | { kind: "hit"; hit: JournalHit };
  const entries = useMemo<Entry[]>(
    () => [
      ...filtered.map((action) => ({ kind: "action" as const, action })),
      ...journalHits.map((hit) => ({ kind: "hit" as const, hit })),
    ],
    [filtered, journalHits],
  );

  // Every filter change restarts selection at the top of the list.
  useEffect(() => setSelected(0), [query]);
  // M11 D2: ⌘N opens the palette straight into the new-workstream prompt.
  useEffect(() => {
    if (initialActionId) {
      const a = actions.find((act) => act.id === initialActionId);
      if (a && a.prompt !== undefined) setPromptFor(a);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // Focus on mount and again when switching into/out of prompt mode.
  useEffect(() => {
    inputRef.current?.focus();
  }, [promptFor]);

  const runAction = (action: PaletteAction) => {
    if (action.disabled) return;
    if (action.prompt !== undefined) {
      setPromptFor(action);
      setPromptText("");
      return;
    }
    onClose();
    execute(action, "");
  };

  const pickJournal = (hit: JournalHit) => {
    onClose();
    onPickJournal?.(hit, query.trim());
  };

  const runEntry = (entry: Entry) => {
    if (entry.kind === "action") runAction(entry.action);
    else pickJournal(entry.hit);
  };

  // Wrap-around navigation that skips disabled entries.
  const step = (delta: number) => {
    setSelected((i) => {
      if (entries.length === 0) return 0;
      let next = i;
      for (let n = 0; n < entries.length; n++) {
        next = (next + delta + entries.length) % entries.length;
        const e = entries[next];
        if (e.kind !== "action" || !e.action.disabled) return next;
      }
      return i;
    });
  };

  const handleKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      step(1);
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      step(-1);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      if (promptFor !== null) {
        const text = promptText.trim();
        if (text === "") return;
        const action = promptFor;
        onClose();
        execute(action, text);
        return;
      }
      const entry = entries[Math.min(selected, entries.length - 1)];
      if (entry) runEntry(entry);
    }
  };

  const clampedSelected = Math.min(selected, Math.max(0, entries.length - 1));

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent
        aria-label="Command palette"
        data-slot={SLOT.palette}
        // Esc always closes — even from prompt mode. Pre-migration App's
        // window-level palette gate fired alongside the palette's own
        // listener, so a bare Esc closed the whole palette regardless of
        // mode (shortcuts.spec contract). Radix's onEscapeKeyDown
        // stopPropagation (in the wrapper) keeps it from reaching App's
        // agent-cancel handler.
        // palette-overlay + palette-panel survive as inert identity
        // markers (e2e shortcuts.spec); their CSS is deleted in app.css.
        // top-[15vh]/-translate-y-0 override the Dialog's default centering
        // to match the old top-anchored palette placement.
        className="palette-overlay palette-panel flex w-[500px] max-w-[calc(100vw-48px)] max-h-[400px] flex-col p-3 top-[15vh] -translate-y-0"
      >
        <input
          ref={inputRef}
          type="text"
          className="palette-input"
          value={promptFor !== null ? promptText : query}
          onChange={(e) => {
            if (promptFor !== null) setPromptText(e.target.value);
            else {
              setQuery(e.target.value);
              onQueryChange?.(e.target.value);
            }
          }}
          onKeyDown={handleKeyDown}
          placeholder={promptFor !== null ? promptFor.prompt : "Type a command…"}
          aria-label={promptFor !== null ? promptFor.name : "Command palette"}
        />
        {promptFor === null && (
          <div className="palette-list" role="listbox" aria-label="Actions">
            {filtered.length === 0 && !journalActive && <div className="palette-empty">No matching actions</div>}
            {filtered.length === 0 && journalActive && journalHits.length === 0 && !journalLoading && journal !== null && (
              <div className="palette-empty">No matching actions</div>
            )}
            {filtered.map((action, i) => (
              <button
                type="button"
                key={action.id}
                role="option"
                aria-selected={i === clampedSelected}
                className={`palette-item${i === clampedSelected ? " selected" : ""}${action.disabled ? " disabled" : ""}`}
                disabled={action.disabled}
                onMouseEnter={() => setSelected(i)}
                onClick={() => runAction(action)}
              >
                {action.icon !== undefined && <span className="palette-icon">{action.icon}</span>}
                <span className="palette-name">{action.name}</span>
                {action.shortcut !== undefined && (
                  <span className="shortcut">{action.shortcut}</span>
                )}
              </button>
            ))}
            {journalActive && (
              <div className="palette-journal-group" role="group" aria-label="Journal search">
                <div className="palette-group-label text-text-dim">Journal search</div>
                {journalLoading && journalHits.length === 0 && (
                  <div className="palette-empty">Searching the journal…</div>
                )}
                {!journalLoading && journalHits.length === 0 && journal !== null && (
                  <div className="palette-empty">No journal matches</div>
                )}
                {journalHits.map((hit, j) => {
                  const idx = filtered.length + j;
                  return (
                    <button
                      type="button"
                      key={`${hit.root}:${hit.result.event.conversation_id}:${hit.result.event.seq}`}
                      role="option"
                      aria-selected={idx === clampedSelected}
                      className={`palette-item palette-journal-row${idx === clampedSelected ? " selected" : ""}`}
                      onMouseEnter={() => setSelected(idx)}
                      onClick={() => pickJournal(hit)}
                    >
                      <span className="palette-name palette-journal-snippet">
                        {snippetFor(hit.result.event.payload, query)}
                      </span>
                      <span className="palette-journal-meta">
                        {hit.projectName} · {hit.result.workstream_name} · {hit.result.event.type}
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
