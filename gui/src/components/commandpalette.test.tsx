import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

// P1.1 (docs/design/adoption-lock.md): palette journal-search group —
// typed-only visibility, row content (provenance + snippet), combined
// keyboard navigation, and Enter pick semantics. App-level flow (search
// fan-out, switch, ⌘F prefill) lives in app_journal_search.test.
// NOTE: Radix Dialog portals to document.body — every query here is
// document/screen-scoped, never container-scoped.

import CommandPalette, { type PaletteAction } from "./CommandPalette";
import type { JournalHit } from "../journal_search";

function makeHit(over: Partial<{ root: string; projectName: string; ws: string; text: string; seq: number }> = {}): JournalHit {
  const { root = "/r1", projectName = "odo", ws = "fix-daemon-binary", text = "fold markers regression notes live here", seq = 41 } = over;
  return {
    root,
    projectName,
    result: {
      event: {
        id: seq,
        conversation_id: 3,
        seq,
        type: "agent_text",
        payload: { text },
        created_at: "2026-08-29T09:00:00Z",
      },
      workstream_id: 3,
      workstream_name: ws,
      conversation_id: 3,
    },
  };
}

const action = (id: string, name: string, over: Partial<PaletteAction> = {}): PaletteAction => ({
  id,
  name,
  onRun: vi.fn(),
  ...over,
});

const q = (sel: string) => document.querySelector(sel);
// DialogContent ALSO carries aria-label="Command palette" — the input is
// the textbox with the placeholder, not the labelled container.
const input = () => screen.getByPlaceholderText("Type a command…") as HTMLInputElement;

beforeEach(() => cleanup());

describe("palette journal group (P1.1)", () => {
  it("renders nothing journal-shaped below 2 chars, rows at ≥2", () => {
    const onQueryChange = vi.fn();
    render(
      <CommandPalette actions={[action("a", "Alpha")]} onClose={() => {}} onQueryChange={onQueryChange} journal={[makeHit()]} />,
    );
    fireEvent.change(input(), { target: { value: "f" } });
    expect(q(".palette-group-label")).toBeNull();
    fireEvent.change(input(), { target: { value: "fo" } });
    expect(onQueryChange).toHaveBeenLastCalledWith("fo");
    expect(q(".palette-group-label")?.textContent).toBe("Journal search");
    const row = q(".palette-journal-row")!;
    expect(row.textContent).toContain("odo · fix-daemon-binary · agent_text");
    expect(row.textContent).toContain("fold markers regression");
  });

  it("Enter picks the highlighted row with the trimmed query", () => {
    const onPickJournal = vi.fn();
    const onClose = vi.fn();
    render(
      <CommandPalette
        actions={[action("a", "Alpha")]}
        onClose={onClose}
        journal={[makeHit({ ws: "feat-a", seq: 1 }), makeHit({ ws: "feat-b", seq: 2 })]}
        onPickJournal={onPickJournal}
      />,
    );
    fireEvent.change(input(), { target: { value: "zz" } }); // no action matches
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(onPickJournal).toHaveBeenCalledTimes(1);
    expect(onPickJournal.mock.calls[0][0].result.workstream_name).toBe("feat-a");
    expect(onPickJournal.mock.calls[0][1]).toBe("zz");
    expect(onClose).toHaveBeenCalled();
  });

  it("ArrowDown wraps onto journal rows; clicks pick a row directly", () => {
    const onPickJournal = vi.fn();
    render(
      <CommandPalette
        actions={[action("a", "Alpha")]}
        onClose={() => {}}
        journal={[makeHit()]}
        onPickJournal={onPickJournal}
      />,
    );
    fireEvent.change(input(), { target: { value: "zz" } });
    // No actions match "zz" → selection lands on the first journal row
    // (and stays there under wrap-around).
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    const selected = q(".palette-item.selected")!;
    expect(selected.classList.contains("palette-journal-row")).toBe(true);
    fireEvent.keyDown(input(), { key: "Enter" });
    fireEvent.click(q(".palette-journal-row")!);
    expect(onPickJournal).toHaveBeenCalledTimes(2);
  });

  it("prompt mode suppresses the journal group (argument entry, not search)", () => {
    render(
      <CommandPalette
        actions={[action("pin", "Pin Memory", { prompt: "remember: …" })]}
        onClose={() => {}}
        journal={[makeHit()]}
      />,
    );
    // Hold the node across the prompt-mode switch — its placeholder flips
    // to the action's prompt ("remember: …"), so a placeholder re-query
    // would miss.
    const el = input();
    fireEvent.change(el, { target: { value: "pi" } });
    fireEvent.keyDown(el, { key: "Enter" }); // enters prompt mode
    fireEvent.change(el, { target: { value: "todo" } }); // ≥2 chars
    expect(q(".palette-group-label")).toBeNull();
  });

  it("states: searching…, then no matches", () => {
    const { rerender } = render(
      <CommandPalette actions={[]} onClose={() => {}} journal={null} journalLoading onPickJournal={() => {}} />,
    );
    fireEvent.change(input(), { target: { value: "zz" } });
    expect(document.body.textContent).toContain("Searching the journal…");
    rerender(<CommandPalette actions={[]} onClose={() => {}} journal={[]} onPickJournal={() => {}} />);
    expect(document.body.textContent).toContain("No journal matches");
  });
});
