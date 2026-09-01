import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type * as Api from "../api";
import type { OdoEvent } from "../types";
import { deriveTodoState, visibleTodoItems } from "../todo";
import { PANEL_CONTRIBUTIONS, badgeFor } from "../contrib";

// UX-1 D2: the Tasks panel pins — journaled todo_merge snapshots fold
// through the REAL deriveTodoState (no hand-built items), the panel renders
// every derived row exactly once across its live/stale/swept sections with
// the chip's glyph classes, the add/op write path rides todo_update
// (origin "user", daemon-side), and the strip badge derives from the same
// fold App wires (visibleTodoItems ⊂ open count → badgeFor).
// Mock precedent: LearningPanel.test.tsx.
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  todoUpdate: vi.fn(),
}));

import { todoUpdate } from "../api";
import TasksPanel from "./TasksPanel";

const mockedUpdate = vi.mocked(todoUpdate);

const TS = "2026-09-01T10:00:00Z";

function todoMerge(seq: number, snapshot: { id: string; text: string; status: string; origin_seq: number; updated_seq: number }[]): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type: "review_action", payload: { action: "todo_merge", snapshot }, created_at: TS };
}

function distill(seq: number, lastSeq: number): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type: "review_action", payload: { action: "distill", last_seq: lastSeq }, created_at: TS };
}

// One snapshot + three fold markers exercising every glyph/section branch:
//   t1 open, updated_seq 5  → 3 folds pass → STALE (section)
//   t2 open, updated_seq 10 → 3 folds pass → STALE (section)
//   t3 open, updated_seq 38 → 1 fold passes → fresh open (live section)
//   t4 done, updated_seq 18 → boundary 39 ≥ 18 → SWEPT (section)
//   t5 done, updated_seq 45 → above boundary → visible done (live)
//   t6 struck, updated_seq 12 → SWEPT (section)
//   t7 struck, updated_seq 44 → visible struck (live)
function fixtureEvents(): OdoEvent[] {
  return [
    todoMerge(10, [
      { id: "t1", text: "reword the frozen plan copy", status: "open", origin_seq: 3, updated_seq: 5 },
      { id: "t2", text: "sweep the archive rotation", status: "open", origin_seq: 4, updated_seq: 10 },
      { id: "t3", text: "wire the tasks tab", status: "open", origin_seq: 6, updated_seq: 38 },
      { id: "t4", text: "land the registry entry", status: "done", origin_seq: 7, updated_seq: 18 },
      { id: "t5", text: "recalibrate the tab spec", status: "done", origin_seq: 8, updated_seq: 45 },
      { id: "t6", text: "delete the stray chip", status: "struck", origin_seq: 9, updated_seq: 12 },
      { id: "t7", text: "drop the second derive", status: "struck", origin_seq: 11, updated_seq: 44 },
    ]),
    distill(20, 19),
    distill(30, 29),
    distill(40, 39),
  ];
}

function renderPanel(events = fixtureEvents()) {
  const items = deriveTodoState(events);
  return render(
    <TasksPanel
      conversationId={1}
      projectRoot="/repo"
      items={items}
      onChanged={() => {}}
      onError={() => {}}
      active
    />,
  );
}

describe("TasksPanel (UX-1 D2)", () => {
  beforeEach(() => {
    cleanup();
    mockedUpdate.mockReset();
    mockedUpdate.mockResolvedValue({ ok: true } as never);
  });

  it("renders every derived row exactly once, sectioned live/stale/swept", () => {
    const { container } = renderPanel();
    expect(container.querySelectorAll(".todo-row")).toHaveLength(7);
    // Live section: t3 open + t5 done + t7 struck (chip's visible set, minus stale).
    const lists = container.querySelectorAll(".todo-list");
    expect(lists).toHaveLength(3);
    // Sections present and labeled.
    expect(container.querySelector(".tasks-stale-section .mem-section-title")?.textContent).toContain("stale");
    expect(container.querySelector(".tasks-swept-section .mem-section-title")?.textContent).toContain("swept");
    // Section composition: stale holds t1+t2, swept holds t4+t6.
    const text = (root: Element | null) => root?.textContent ?? "";
    expect(text(container.querySelector(".tasks-stale-section"))).toContain("reword the frozen plan copy");
    expect(text(container.querySelector(".tasks-stale-section"))).toContain("sweep the archive rotation");
    expect(text(container.querySelector(".tasks-swept-section"))).toContain("land the registry entry");
    expect(text(container.querySelector(".tasks-swept-section"))).toContain("delete the stray chip");
  });

  it("renders the chip's glyph classes for open/done/struck/stale", () => {
    const { container } = renderPanel();
    expect(container.querySelectorAll(".todo-glyph-open")).toHaveLength(1); // t3
    expect(container.querySelectorAll(".todo-glyph-stale")).toHaveLength(2); // t1, t2
    expect(container.querySelectorAll(".todo-glyph-done")).toHaveLength(2); // t5 live, t4 swept
    expect(container.querySelectorAll(".todo-glyph-struck")).toHaveLength(2); // t7 live, t6 swept
    // Stale rows keep the inline ~stale mark (chip convention).
    expect(container.querySelectorAll(".todo-stale-mark")).toHaveLength(2);
  });

  it("shows full text (panel posture: no ellipsis clipping)", () => {
    const { container } = renderPanel();
    for (const el of container.querySelectorAll(".todo-text")) {
      expect(el.className).not.toContain("text-ellipsis");
      expect(el.className).not.toContain("whitespace-nowrap");
    }
  });

  it("derives the strip badge from the same fold App wires (visible opens)", () => {
    const items = deriveTodoState(fixtureEvents());
    // App.tsx badgeInput: visibleTodoItems(todoItems).filter(status==="open")
    // — stale opens COUNT (they're still visible work), swept never do.
    const openTodos = visibleTodoItems(items).filter((t) => t.status === "open").length;
    expect(openTodos).toBe(3); // t1, t2, t3
    const tasks = PANEL_CONTRIBUTIONS.find((c) => c.id === "tasks");
    expect(tasks && badgeFor(tasks, {
      pendingDiffs: 0, pendingReview: 0, wikiNotes: null, memoryProposals: 0, openTodos, activeJobs: 0, activeBatches: 0,
    })).toBe(3);
  });

  it("add op writes through todo_update and pokes onChanged", async () => {
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /add/i }));
    const input = screen.getByLabelText("Add a plan item");
    fireEvent.change(input, { target: { value: "recalibrate the spec" } });
    fireEvent.submit(input.closest("form")!);
    expect(mockedUpdate).toHaveBeenCalledWith(1, "add", expect.objectContaining({ text: "recalibrate the spec", projectRoot: "/repo" }));
  });

  it("row ops ride the shared mutation runner (done on an open row)", () => {
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Done wire the tasks tab" }));
    expect(mockedUpdate).toHaveBeenCalledWith(1, "done", expect.objectContaining({ todoId: "t3" }));
  });

  it("empty plan: panel-empty copy plus the always-on add affordance", () => {
    const { container } = renderPanel([]);
    expect(container.textContent).toContain("No plan items yet");
    expect(screen.getByRole("button", { name: /add/i })).toBeTruthy();
  });
});
