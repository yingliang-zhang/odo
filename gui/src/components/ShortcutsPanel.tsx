// P1.3 (docs/design/adoption-lock.md): read-only shortcuts panel behind ⌘/.
// Renders the keybinds.ts registry grouped by category — the table is the
// single source (the App dispatch and palette hints read it too), so this
// panel can never drift from what actually fires. Esc handling rides the
// shared Dialog wrapper (Radix onEscapeKeyDown stopPropagation: a bare Esc
// closes the panel without reaching App's ladder).
import { Dialog, DialogContent, DialogTitle } from "./ui/dialog";
import { KEYBINDS, type Keybind } from "../keybinds";
import { SLOT } from "../slots";

const CATEGORY_ORDER: Keybind["category"][] = ["Palette", "Chat", "View", "Run"];

export default function ShortcutsPanel({ onClose }: { onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent
        aria-label="Keyboard shortcuts"
        data-slot={SLOT.shortcuts}
        className="shortcuts-panel w-[400px] max-w-[calc(100vw-48px)] p-4"
      >
        <DialogTitle className="mb-3">Keyboard shortcuts</DialogTitle>
        <div className="flex flex-col gap-3">
          {CATEGORY_ORDER.map((cat) => {
            const rows = KEYBINDS.filter((k) => k.category === cat);
            if (rows.length === 0) return null;
            return (
              <section key={cat}>
                <h3 className="mb-1 text-[11px] uppercase tracking-wide text-[var(--text-dim)]">{cat}</h3>
                <div className="flex flex-col gap-0.5">
                  {rows.map((k) => (
                    <div key={k.id} className="shortcut-row flex items-center justify-between gap-4 py-0.5">
                      <span className="text-[13px] text-[var(--text)]">{k.label}</span>
                      <kbd className="shrink-0 rounded border border-[var(--border)] bg-[var(--bg-input)] px-1.5 py-px font-mono text-[11px] text-[var(--text-dim)]">{k.display}</kbd>
                    </div>
                  ))}
                </div>
              </section>
            );
          })}
        </div>
      </DialogContent>
    </Dialog>
  );
}
