// P1.3 (docs/design/adoption-lock.md): the static keybind registry. Every
// global shortcut lives here as data — App's window keydown dispatch and the
// ⌘/ shortcuts panel both consume this ONE table, and the command palette
// renders live hints via comboFor(actionId) instead of hardcoded "⌘B"
// strings.
//
// `combo` is normalized: modifiers first ("mod+shift+key"), "mod" meaning
// ⌘ on macOS / Ctrl elsewhere (the dispatcher matches metaKey || ctrlKey,
// same as the pre-registry switch did). The "escape" combo is display-only:
// the Esc ladder (close search → close panel → cancel run) is a priority
// chain with DOM gates, not a flat dispatch, so App keeps it imperative.
export interface Keybind {
  // Stable action id. Palette actions with a keybind reuse these ids so
  // `comboFor(action.id)` resolves their hint.
  id: string;
  // Human label — the shortcuts panel's row text (palette action names are
  // a separate surface and may phrase the same act differently).
  label: string;
  combo: string;
  // Keystroke display form, e.g. "⌘B".
  display: string;
  category: "Palette" | "Chat" | "View" | "Run";
  // When false the dispatch is skipped while focus sits in an editable
  // element (input/textarea/contentEditable). All combos today are
  // window-level (they fire from the composer too), so every row is true;
  // the gate exists for future bare-key binds.
  allowedInInput: boolean;
}

export const KEYBINDS: readonly Keybind[] = [
  { id: "open-palette", label: "Command palette", combo: "mod+k", display: "⌘K", category: "Palette", allowedInInput: true },
  { id: "new-workstream", label: "New workstream", combo: "mod+n", display: "⌘N", category: "Palette", allowedInInput: true },
  { id: "open-shortcuts", label: "Keyboard shortcuts", combo: "mod+/", display: "⌘/", category: "Palette", allowedInInput: true },
  { id: "search-chat", label: "Search chat", combo: "mod+f", display: "⌘F", category: "Chat", allowedInInput: true },
  { id: "send-message", label: "Send message", combo: "mod+enter", display: "⌘↵", category: "Chat", allowedInInput: true },
  { id: "toggle-sidebar", label: "Toggle sidebar", combo: "mod+b", display: "⌘B", category: "View", allowedInInput: true },
  { id: "toggle-panel", label: "Toggle context panel", combo: "mod+j", display: "⌘J", category: "View", allowedInInput: true },
  { id: "open-settings", label: "Open settings", combo: "mod+,", display: "⌘,", category: "View", allowedInInput: true },
  { id: "cancel-run", label: "Cancel run", combo: "escape", display: "Esc", category: "Run", allowedInInput: true },
  { id: "dismiss-overlay", label: "Close search bar / context panel", combo: "escape", display: "Esc", category: "Run", allowedInInput: true },
];

// Live hint lookup for the palette (undefined = the action has no keybind).
export function comboFor(id: string): string | undefined {
  return KEYBINDS.find((k) => k.id === id)?.display;
}

interface KeyEventLike {
  metaKey: boolean;
  ctrlKey: boolean;
  shiftKey: boolean;
  key: string;
}

// Resolve a keyboard event to a registry row. Mod-combos only — "escape"
// rows are documentation for the ⌘/ panel; App's Esc ladder handles them.
// Shift participates only for combos that actually carry it (none today),
// so ⌘B with shift held still resolves to "mod+b" — matching the old
// switch, which never inspected shiftKey.
export function matchKeyEvent(e: KeyEventLike): Keybind | null {
  if (!(e.metaKey || e.ctrlKey)) return null;
  const want = "mod+" + e.key.toLowerCase();
  for (const k of KEYBINDS) {
    if (!k.combo.startsWith("mod+")) continue;
    const base = k.combo.replace("mod+shift+", "mod+");
    if (base === want) {
      if (k.combo.includes("shift") && !e.shiftKey) return null;
      return k;
    }
  }
  return null;
}

// True when the event target is an editable element (input, textarea,
// or contentEditable) — the allowedInInput gate consults this.
export function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}
