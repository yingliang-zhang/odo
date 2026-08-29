import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

// P1.3 (docs/design/adoption-lock.md): the keybind registry — the App
// dispatch, palette hints, and the ⌘/ panel all consume this ONE table.
// Tests pin combo parsing (incl. Ctrl-as-mod parity), registry hygiene,
// and the panel rendering every row.

import { comboFor, isEditableTarget, KEYBINDS, matchKeyEvent } from "./keybinds";
import ShortcutsPanel from "./components/ShortcutsPanel";
import { SLOT, slotSel } from "./slots";

const evt = (key: string, opts: Partial<{ meta: boolean; ctrl: boolean; shift: boolean }> = {}) => ({
  key,
  metaKey: opts.meta ?? false,
  ctrlKey: opts.ctrl ?? false,
  shiftKey: opts.shift ?? false,
});

describe("keybind registry (P1.3)", () => {
  it("resolves mod-combos on both ⌘ and Ctrl", () => {
    expect(matchKeyEvent(evt("b", { meta: true }))?.id).toBe("toggle-sidebar");
    expect(matchKeyEvent(evt("J", { ctrl: true }))?.id).toBe("toggle-panel");
    expect(matchKeyEvent(evt(",", { meta: true }))?.id).toBe("open-settings");
    expect(matchKeyEvent(evt("f", { meta: true }))?.id).toBe("search-chat");
    expect(matchKeyEvent(evt("k", { meta: true }))?.id).toBe("open-palette");
    expect(matchKeyEvent(evt("n", { meta: true }))?.id).toBe("new-workstream");
    expect(matchKeyEvent(evt("/", { meta: true }))?.id).toBe("open-shortcuts");
  });

  it("ignores bare keys, and shift does not block plain mod rows", () => {
    expect(matchKeyEvent(evt("b"))).toBeNull();
    expect(matchKeyEvent(evt("Escape"))).toBeNull();
    expect(matchKeyEvent(evt("b", { meta: true, shift: true }))?.id).toBe("toggle-sidebar");
  });

  it("comboFor feeds the palette hints; unknown actions get undefined", () => {
    expect(comboFor("toggle-sidebar")).toBe("⌘B");
    expect(comboFor("open-settings")).toBe("⌘,");
    expect(comboFor("cancel-run")).toBe("Esc");
    expect(comboFor("no-such-action")).toBeUndefined();
  });

  it("registry hygiene: unique ids, normalized combos, non-empty display", () => {
    const ids = KEYBINDS.map((k) => k.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const k of KEYBINDS) {
      expect(k.display.length).toBeGreaterThan(0);
      expect(k.label.length).toBeGreaterThan(0);
      if (k.combo.startsWith("mod+")) {
        expect(k.combo).toBe(k.combo.toLowerCase());
        const row = matchKeyEvent({ key: k.combo.slice(4).replace("shift+", ""), metaKey: true, ctrlKey: false, shiftKey: k.combo.includes("shift") });
        expect(row?.id, `combo ${k.combo} must round-trip`).toBe(k.id);
      }
    }
  });

  it("every palette action id with a hint resolves through the registry", () => {
    // The six hint-carrying palette actions in App.tsx — a palette action
    // that hardcodes a string would silently drift from this table.
    for (const id of ["new-workstream", "open-settings", "cancel-run", "toggle-sidebar", "toggle-panel", "search-chat"]) {
      expect(comboFor(id), `missing registry row for ${id}`).toBeDefined();
    }
  });

  it("isEditableTarget gates inputs, textareas, and contentEditable", () => {
    expect(isEditableTarget(document.createElement("input"))).toBe(true);
    expect(isEditableTarget(document.createElement("textarea"))).toBe(true);
    expect(isEditableTarget(document.createElement("div"))).toBe(false);
    const ed = document.createElement("div");
    Object.defineProperty(ed, "isContentEditable", { value: true });
    expect(isEditableTarget(ed)).toBe(true);
  });

  it("⌘/ panel lists every registry row with its display combo", () => {
    render(<ShortcutsPanel onClose={() => {}} />);
    const panel = document.querySelector(slotSel(SLOT.shortcuts));
    expect(panel).not.toBeNull();
    for (const k of KEYBINDS) {
      expect(panel!.textContent).toContain(k.label);
      expect(panel!.textContent).toContain(k.display);
    }
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeTruthy();
  });
});
