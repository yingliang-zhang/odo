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

// UX-4 / A3-2 (ux-batch-lock-amendment-a3): the review_action receipts
// that owned the deleted Ledger tab fold into this panel as a second
// section, off the same events prop. Pins: outcome/actor badges, Guardian
// risk badges with honest "unrated" for pre-W5 rows, the timed-out chip,
// the collapsed run log, newest-first order, and the exclusion of
// bookkeeping rows (todo_merge et al. keep their own surfaces).
describe("receipts section (A3-2)", () => {
  it("renders decision receipts newest-first with actor, outcome, and risk badges", () => {
    const { container, getByText } = renderPanel([
      ev(1, "review_action", { action: "moa_review", consensus_verdict: "accept", actor: "auto_panel", risk_class: ["none"] }),
      ev(2, "review_action", { action: "accept", actor: "auto_panel", diff_id: 7, risk_class: ["credential_probe", "supply_chain"] }),
      ev(3, "review_action", { action: "auto_revise_round", round: 2, risk_class: ["none"] }),
      // Bookkeeping rows keep their own surfaces — never receipts.
      ev(4, "review_action", { action: "todo_merge" }),
    ]);
    expect(getByText("review actions — journal receipts, newest first")).toBeTruthy();
    const rows = [...container.querySelectorAll<HTMLElement>(".ledger-review-row")];
    expect(rows).toHaveLength(3);
    expect(rows[0].dataset.seq).toBe("3");
    expect(rows[0].textContent).toContain("Revise round 2");
    expect(rows[2].textContent).toContain("Review · accept");
    const accept = rows.find((r) => r.dataset.action === "accept")!;
    expect(accept.querySelector(".badge-actor-auto")!.textContent).toBe("Auto");
    expect(accept.textContent).toContain("diff #7");
    expect(accept.querySelector(".risk-critical")!.textContent).toBe("credential_probe");
    expect(accept.querySelector(".risk-low")!.textContent).toBe("supply_chain");
    expect(accept.querySelector(".risk-clean")).toBeNull();
  });

  it("renders honest unrated rows, the timed-out chip, and the collapsed run log", () => {
    const { container } = renderPanel([
      ev(1, "review_action", { action: "reject" }), // pre-W5: no risk receipt journaled
      ev(2, "review_action", { action: "moa_review", consensus_verdict: "mixed", timed_out: true, risk_class: ["security_weakening"] }),
      ev(3, "review_action", { action: "auto_land_blocked", reason: "base_stale", detail: "go test ./... FAILED", actor: "auto_panel", risk_class: ["destructive"] }),
      ev(4, "review_action", { action: "refresh_attempted", outcome: "conflict", phase: "pre_spend_probe" }),
    ]);
    const rows = [...container.querySelectorAll<HTMLElement>(".ledger-review-row")];
    expect(rows).toHaveLength(4);
    const reject = rows.find((r) => r.dataset.action === "reject")!;
    expect(reject.querySelector(".risk-unrated")!.textContent).toBe("unrated");
    expect(reject.querySelector(".badge-actor-human")!.textContent).toBe("Human"); // no actor journaled
    const mixed = rows.find((r) => r.dataset.action === "moa_review")!;
    expect(mixed.querySelector(".risk-timeout")!.textContent).toBe("timed out");
    const blocked = rows.find((r) => r.dataset.action === "auto_land_blocked")!;
    expect(blocked.querySelector(".ledger-review-detail")!.textContent).toBe("base_stale");
    expect(blocked.querySelector(".ledger-run-log-pre")!.textContent).toBe("go test ./... FAILED");
    // A refresh is a rebase, not a verdict: no risk chip at all, and the
    // phase rides the detail cell.
    const refresh = rows.find((r) => r.dataset.action === "refresh_attempted")!;
    expect(refresh.querySelectorAll(".risk-badge")).toHaveLength(0);
    expect(refresh.querySelector(".ledger-review-detail")!.textContent).toBe("pre_spend_probe");
  });

  it("shows the empty state only when neither runs nor receipts exist", () => {
    const { container, queryByText } = renderPanel([
      ev(1, "review_action", { action: "accept", risk_class: ["none"] }),
    ]);
    expect(queryByText(/No runs yet/)).toBeNull();
    expect(container.querySelectorAll(".ledger-review-row")).toHaveLength(1);
  });
});
