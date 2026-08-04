import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";

// Belt B (⌘K): fuzzy action launcher overlaying the shell, patterned on
// settings-overlay. Two modes: the action list (filter by substring,
// arrows navigate, Enter runs) and prompt mode for actions that need a
// text argument (New Workstream name, Pin text) — Enter submits the text.

export interface PaletteAction {
  id: string;
  name: string;
  icon?: string;
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

export default function CommandPalette({ actions, onClose }: Props) {
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
  // Focus on mount and again when switching into/out of prompt mode.
  useEffect(() => {
    inputRef.current?.focus();
  }, [promptFor]);

  // Esc: back out of prompt mode, else close. Window-level so it works
  // regardless of focus; App's own Esc handler skips .palette-overlay, so
  // it won't double-fire into blur/cancel.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (promptFor !== null) {
        setPromptFor(null);
        setPromptText("");
      } else {
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, promptFor]);

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
    <div className="palette-overlay" onClick={onClose}>
      <div
        className="palette-panel"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onClick={(e) => e.stopPropagation()}
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
      </div>
    </div>
  );
}
