import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { Dialog, DialogContent } from "./ui/dialog";

// Belt B (⌘K): fuzzy action launcher over a Radix Dialog (Phase 5).
// Two modes: the action list (filter by substring, arrows navigate, Enter
// runs) and prompt mode for actions that need a text argument (New
// Workstream name, Pin text) — Enter submits the text.

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

export default function CommandPalette({ actions, onClose, initialActionId }: Props) {
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState(0);
  const [promptFor, setPromptFor] = useState<PaletteAction | null>(null);
  const [promptText, setPromptText] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q === "" ? actions : actions.filter((a) => a.name.toLowerCase().includes(q));
  }, [actions, query]);

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

  // Wrap-around navigation that skips disabled entries.
  const step = (delta: number) => {
    setSelected((i) => {
      if (filtered.length === 0) return 0;
      let next = i;
      for (let n = 0; n < filtered.length; n++) {
        next = (next + delta + filtered.length) % filtered.length;
        if (!filtered[next].disabled) return next;
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
      const action = filtered[Math.min(selected, filtered.length - 1)];
      if (action) runAction(action);
    }
  };

  const clampedSelected = Math.min(selected, Math.max(0, filtered.length - 1));

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent
        aria-label="Command palette"
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
          onChange={(e) =>
            promptFor !== null ? setPromptText(e.target.value) : setQuery(e.target.value)
          }
          onKeyDown={handleKeyDown}
          placeholder={promptFor !== null ? promptFor.prompt : "Type a command…"}
          aria-label={promptFor !== null ? promptFor.name : "Command palette"}
        />
        {promptFor === null && (
          <div className="palette-list" role="listbox" aria-label="Actions">
            {filtered.length === 0 && <div className="palette-empty">No matching actions</div>}
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
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
