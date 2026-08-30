import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render } from "@testing-library/react";

// P2.4 (docs/design/adoption-lock.md): the Runs tab — journaled rows with
// the P1.2 slot marker, click-to-jump deep-link, empty state, and the
// no-fabrication rules (a running row has no finished time; an unmeasured
// run shows an est / "—", never invented tokens).
import RunsPanel from "./RunsPanel";
import { SLOT } from "../slots";
import type { EventPayload, OdoEvent } from "../types";

function ev(seq: number, type: OdoEvent["type"], payload: EventPayload = {}, at?: string): OdoEvent {
  return {
    id: seq,
    conversation_id: 1,
    seq,
    type,
    payload,
    created_at: at ?? `2026-08-29T10:00:${String(seq % 60).padStart(2, "0")}.000Z`,
  };
}

beforeEach(cleanup);

function renderPanel(events: OdoEvent[], over: Partial<Parameters<typeof RunsPanel>[0]> = {}) {
  return render(
    <RunsPanel events={events} projectRoot={null} active={true} {...over} />,
  );
}

describe("RunsPanel", () => {
  it("renders one row per run with the runs-row data-slot and status", () => {
    const { container } = renderPanel([
      ev(1, "user_message", { text: "first goal" }),
      ev(2, "agent_done", { summary: "shipped it" }),
      ev(5, "user_message", { text: "second goal" }),
      ev(6, "agent_error", { error: "adapter exploded" }),
    ]);
    const rows = [...container.querySelectorAll<HTMLElement>(`[data-slot="${SLOT.runsRow}"]`)];
    expect(rows).toHaveLength(2);
    // Newest first, seq pinned as the deep-link anchor.
    expect(rows[0].dataset.seq).toBe("5");
    expect(rows[0].dataset.status).toBe("error");
    expect(rows[1].dataset.seq).toBe("1");
    expect(rows[1].dataset.status).toBe("ok");
  });

  it("fires onJumpToSeq with the run's startSeq on click", () => {
    const onJumpToSeq = vi.fn();
    const { getByText } = renderPanel(
      [ev(7, "user_message", { text: "jumpable goal" }), ev(8, "agent_done", { summary: "s" })],
      { onJumpToSeq },
    );
    fireEvent.click(getByText("jumpable goal"));
    expect(onJumpToSeq).toHaveBeenCalledTimes(1);
    expect(onJumpToSeq).toHaveBeenCalledWith(7);
  });

  it("renders the empty state when no run has journaled", () => {
    const { getByText, container } = renderPanel([ev(1, "agent_text", { text: "stray" })]);
    expect(getByText(/No runs yet/)).toBeTruthy();
    expect(container.querySelector(`[data-slot="${SLOT.runsRow}"]`)).toBeNull();
  });

  it("shows a running row with a start clock and no finished time", () => {
    const { container } = renderPanel([ev(3, "user_message", { text: "still going" }, "2026-08-29T10:11:12.000Z")]);
    const row = container.querySelector<HTMLElement>(`[data-slot="${SLOT.runsRow}"]`)!;
    expect(row.dataset.status).toBe("running");
    expect(row.textContent).toContain("running");
    // One clock (the start), never a "→ finish" for an open run.
    expect(row.textContent).not.toContain("→");
  });

  it("renders measured in/out tokens when a usage receipt landed", () => {
    const { getByText, container } = renderPanel([
      ev(1, "user_message", { text: "goal", total_prompt_bytes: 999 }),
      ev(2, "agent_done", { summary: "s" }),
      ev(3, "loop_event", {
        kind: "loop_run_usage",
        loop_id: 1,
        kind_run: "fix",
        run_id: "r1",
        covers_spawn_seq: 1,
        usage_available: true,
        input_tokens: 4000,
        output_tokens: 100,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
      }),
    ]);
    // Measured receipt wins over the prompt-byte estimate.
    expect(getByText(/4\.0k tok in · 100 tok out/)).toBeTruthy();
    expect(container.textContent).not.toContain("est");
  });

  it("labels prompt bytes as an estimate when nothing was measured", () => {
    const { getByText } = renderPanel([
      ev(1, "user_message", { text: "goal", total_prompt_bytes: 4096 }),
      ev(2, "agent_done", { summary: "s" }),
    ]);
    expect(getByText("est 4.0 KB")).toBeTruthy();
  });

  it("renders — for a plain run with no usage row and no estimate", () => {
    const { container } = renderPanel([
      ev(1, "user_message", { text: "goal" }),
      ev(2, "agent_done", { summary: "s" }),
    ]);
    const tokens = container.querySelector<HTMLElement>(".runs-tokens")!;
    expect(tokens.textContent).toBe("—");
  });

  it("shows the tab-header model line once when provided", () => {
    const { getByText } = renderPanel(
      [ev(1, "user_message", { text: "goal" })],
      { currentModel: "t9s/kimi-k3" },
    );
    expect(getByText("coding model: t9s/kimi-k3")).toBeTruthy();
  });
});
