import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render } from "@testing-library/react";
import { waitFor } from "@testing-library/react";

// Odo DX wave (Features 1 + 5): retry rides send_message and the hub
// lists/executes through readFile/runCommand — hoisted mocks keep the
// panel's invoke surface assertable without the Tauri bridge.
const { readFileMock, runCommandMock, sendMessageMock } = vi.hoisted(() => ({
  readFileMock: vi.fn(),
  runCommandMock: vi.fn(),
  sendMessageMock: vi.fn(),
}));
vi.mock("../api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  return {
    ...real,
    readFile: readFileMock,
    runCommand: runCommandMock,
    sendMessage: sendMessageMock,
  };
});

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

beforeEach(() => {
  cleanup();
  readFileMock.mockReset();
  // Feature 5 default: .odo/commands.json ABSENT → the hub renders
  // nothing (zero clutter), like the daemon's missing-file refusal.
  readFileMock.mockRejectedValue(new Error("read_file: no such file"));
  runCommandMock.mockReset();
  sendMessageMock.mockReset();
});

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

// Odo DX wave (Feature 1): the hover retry affordance on failed runs —
// error rows with a replayable prompt offer it; ok/running rows never do;
// a live agent disables it with the busy tooltip. The click re-sends the
// journaled prompt through send_message and is swallowed from the row's
// transcript jump.
describe("retry button (Odo DX wave, Feature 1)", () => {
  const failedRun = [
    ev(3, "user_message", { text: "fix the flaky gate" }),
    ev(4, "agent_error", { error: "adapter exploded" }),
  ];

  it("offers retry on an error row and never on an ok row", () => {
    const { container } = renderPanel([
      ...failedRun,
      ev(5, "user_message", { text: "succeeded goal" }),
      ev(6, "agent_done", { summary: "fine" }),
    ]);
    const rows = [...container.querySelectorAll<HTMLElement>(`[data-slot="${SLOT.runsRow}"]`)];
    expect(rows).toHaveLength(2);
    const errorRow = rows.find((r) => r.dataset.status === "error")!;
    const okRow = rows.find((r) => r.dataset.status === "ok")!;
    expect(errorRow.querySelector(`[data-slot="${SLOT.runsRetry}"]`)).not.toBeNull();
    expect(okRow.querySelector(`[data-slot="${SLOT.runsRetry}"]`)).toBeNull();
  });

  it("re-sends the run's original prompt through send_message, swallowing the row jump", () => {
    sendMessageMock.mockResolvedValue({ ok: true });
    const onJumpToSeq = vi.fn();
    const { container } = renderPanel(failedRun, { onJumpToSeq });
    const button = container.querySelector<HTMLElement>(`[data-slot="${SLOT.runsRetry}"]`)!;
    fireEvent.click(button);
    expect(sendMessageMock).toHaveBeenCalledTimes(1);
    expect(sendMessageMock).toHaveBeenCalledWith(1, "fix the flaky gate", [], { projectRoot: undefined });
    // The click must not bubble into the transcript jump.
    expect(onJumpToSeq).not.toHaveBeenCalled();
  });

  it("disables the button with the busy tooltip while the agent runs", () => {
    const { container } = renderPanel(failedRun, { agentRunning: true });
    const button = container.querySelector<HTMLElement>(`[data-slot="${SLOT.runsRetry}"]`)! as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.title).toContain("Agent busy");
  });

  it("surfaces a thrown send refusal inline instead of crashing", async () => {
    sendMessageMock.mockRejectedValue(new Error("daemon: run already active"));
    const { container, getByText } = renderPanel(failedRun);
    fireEvent.click(container.querySelector(`[data-slot="${SLOT.runsRetry}"]`)!);
    await waitFor(() => expect(getByText(/retry failed: daemon: run already active/)).toBeTruthy());
  });
});

// Odo DX wave (Feature 5): the Run/Test hub — zero clutter without
// .odo/commands.json, buttons per registered command, a journaled row's
// badge folded from the events prop, and the click path through
// run_command with an immediate badge flip from the invoke response.
describe("commands hub (Odo DX wave, Feature 5)", () => {
  const hubConfig = {
    file_content: JSON.stringify({
      version: 1,
      commands: [
        { name: "tests", cmd: "go test ./...", timeout: 120 },
        { name: "lint", cmd: "gofmt -l ." },
      ],
    }),
  };

  it("renders nothing when commands.json is absent (zero clutter)", async () => {
    const { container } = renderPanel([ev(1, "user_message", { text: "goal" })], { conversationId: 1 });
    await waitFor(() => expect(readFileMock).toHaveBeenCalledWith(".odo/commands.json", null));
    expect(container.querySelector(`[data-slot="${SLOT.commandsSection}"]`)).toBeNull();
  });

  it("renders no hub at all without a conversation", () => {
    const { container } = renderPanel([ev(1, "user_message", { text: "goal" })]);
    expect(container.querySelector(`[data-slot="${SLOT.commandsSection}"]`)).toBeNull();
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it("names a malformed existing config on one line instead of hiding it", async () => {
    readFileMock.mockResolvedValue({ file_content: "not json {" });
    const { getByText } = renderPanel([ev(1, "user_message", { text: "goal" })], { conversationId: 1 });
    await waitFor(() => expect(getByText(/not valid JSON/)).toBeTruthy());
  });

  it("folds a journaled command_result into the row badge without any click", async () => {
    readFileMock.mockResolvedValue(hubConfig);
    const { container, getByText } = renderPanel(
      [
        ev(1, "user_message", { text: "goal" }),
        ev(2, "command_result", { name: "tests", exit_code: 1, stdout_tail: "FAIL pkg/x", duration_ms: 2300 }),
      ],
      { conversationId: 1 },
    );
    await waitFor(() => expect(container.querySelectorAll(`[data-slot="${SLOT.commandRow}"]`)).toHaveLength(2));
    expect(getByText(/exit 1 · 2s/)).toBeTruthy();
    // The journaled tail carries the expandable output.
    expect(container.textContent).toContain("FAIL pkg/x");
  });

  it("executes on click and flips the badge from the invoke response", async () => {
    readFileMock.mockResolvedValue(hubConfig);
    runCommandMock.mockResolvedValue({
      ok: true,
      command_result: { name: "tests", exit_code: 0, stdout_tail: "ok all", duration_ms: 640 },
    });
    const { container, getByText } = renderPanel([ev(1, "user_message", { text: "goal" })], { conversationId: 7 });
    await waitFor(() => expect(container.querySelectorAll(".command-run")).toHaveLength(2));
    fireEvent.click(getByText("tests"));
    await waitFor(() => expect(getByText(/ok · 640ms/)).toBeTruthy());
    expect(runCommandMock).toHaveBeenCalledWith(7, "tests", undefined);
    expect(container.textContent).toContain("ok all");
  });

  it("shows the run refusal inline (unknown command, daemon validation)", async () => {
    readFileMock.mockResolvedValue(hubConfig);
    runCommandMock.mockRejectedValue(new Error('run_command: no command named "tests" in .odo/commands.json'));
    const { container, getByText } = renderPanel([ev(1, "user_message", { text: "goal" })], { conversationId: 1 });
    await waitFor(() => expect(container.querySelectorAll(".command-run")).toHaveLength(2));
    fireEvent.click(getByText("tests"));
    await waitFor(() => expect(getByText(/no command named/)).toBeTruthy());
  });
});

// P1 borrow #7 (subagent report, quad-audit follow-up): nested rows under
// the parent run — prefix/goal/status/dot, the view-diff link riding the
// Changes-tab path, and the flat fallback for unattributed rows.
describe("subagent rows", () => {
  it("nests spawned children under their parent run, in spawn order", () => {
    const { container } = renderPanel([
      ev(1, "user_message", { text: "parent goal" }),
      ev(2, "subagent_spawned", { subagent_id: "sub-1", goal: "first child" }),
      ev(3, "subagent_spawned", { subagent_id: "sub-2", goal: "second child" }),
      ev(4, "subagent_done", { subagent_id: "sub-2", exit_code: 0, summary: "done" }),
      ev(5, "agent_done", { summary: "parent done" }),
    ]);
    const subs = [...container.querySelectorAll<HTMLElement>('[data-slot="runs-subagent"]')];
    expect(subs).toHaveLength(2);
    expect(subs[0].dataset.subagent).toBe("sub-1");
    expect(subs[0].dataset.status).toBe("running");
    expect(subs[1].dataset.subagent).toBe("sub-2");
    expect(subs[1].dataset.status).toBe("done");
    // Nesting contract: the sub rows immediately FOLLOW the parent run
    // row in DOM order, before any later run row.
    const ordered = [...container.querySelectorAll<HTMLElement>(".runs-row, .runs-subrow")].map((el) =>
      el.dataset.subagent ?? el.dataset.seq,
    );
    expect(ordered).toEqual(["1", "sub-1", "sub-2"]);
    expect(container.querySelectorAll(".runs-sub-prefix")).toHaveLength(2);
  });

  it("marks failures and renders the view-diff link only when a diff registered", () => {
    const onOpenChanges = vi.fn();
    const { container, queryByText, getByText } = renderPanel(
      [
        ev(1, "user_message", { text: "parent goal" }),
        ev(2, "subagent_spawned", { subagent_id: "sub-1", goal: "child with proposal" }),
        ev(3, "subagent_done", { subagent_id: "sub-1", exit_code: 0, diff_id: 42 }),
        ev(4, "agent_done", { summary: "parent done" }),
        ev(5, "user_message", { text: "second goal" }),
        ev(6, "subagent_spawned", { subagent_id: "sub-2", goal: "failed child" }),
        ev(7, "subagent_done", { subagent_id: "sub-2", exit_code: 1 }),
        ev(8, "agent_done", { summary: "done" }),
      ],
      { onOpenChanges },
    );
    expect(container.querySelector<HTMLElement>('[data-subagent="sub-2"]')!.dataset.status).toBe("failed");
    const link = getByText("view diff");
    expect((link as HTMLElement).getAttribute("title")).toContain("diff #42");
    fireEvent.click(link);
    expect(onOpenChanges).toHaveBeenCalledTimes(1);
    // The diff-less failed row stays inert: no link, no button role.
    expect(queryByText("view diff", { selector: '[data-subagent="sub-2"] *' })).toBeNull();
  });

  it("the whole row opens the Changes tab when a diff is registered", () => {
    const onOpenChanges = vi.fn();
    const { container } = renderPanel(
      [
        ev(1, "user_message", { text: "parent goal" }),
        ev(2, "subagent_spawned", { subagent_id: "sub-1", goal: "child" }),
        ev(3, "subagent_done", { subagent_id: "sub-1", exit_code: 0, diff_id: 7 }),
        ev(4, "agent_done", { summary: "done" }),
      ],
      { onOpenChanges },
    );
    const row = container.querySelector<HTMLElement>('[data-subagent="sub-1"]')!;
    expect(row.getAttribute("role")).toBe("button");
    fireEvent.click(row);
    expect(onOpenChanges).toHaveBeenCalledTimes(1);
  });

  it("unattributed rows fall back to the flat section instead of vanishing", () => {
    const { container, getByText } = renderPanel([
      ev(1, "subagent_spawned", { subagent_id: "sub-orphan", goal: "no run ever opened" }),
      ev(2, "subagent_done", { subagent_id: "sub-orphan", exit_code: 0 }),
    ]);
    expect(getByText(/without an attributed run/i)).toBeTruthy();
    const row = container.querySelector<HTMLElement>('[data-subagent="sub-orphan"]')!;
    expect(row.dataset.status).toBe("done");
  });
});
