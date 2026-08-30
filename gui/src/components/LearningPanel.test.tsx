import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type * as Api from "../api";
import type { LearningContextRow, LearningOutcomeRow, LearningStatus } from "../types";

// D9-W3 (learning control plane, pure observability): the Learning panel
// renders the daemon's single learning_status fold — flagged rules with
// verdict badges + verbatim thresholds, per-episode non-zero outcome
// lines (attribution_lost/memory_free_outcomes visible when >0), and the
// W3 empty-candidates state (forward-compat rows covered too).
// Mock precedent: memory_cap.test.tsx.
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  learningStatus: vi.fn(),
}));

import { learningStatus } from "../api";
import LearningPanel from "./LearningPanel";

const mockedStatus = vi.mocked(learningStatus);

const ZERO_OUTCOMES: LearningOutcomeRow = {
  accepted: 0,
  rejected: 0,
  weak_rejected: 0,
  auto_accepted: 0,
  auto_rejected: 0,
  verify_failed: 0,
  panel_mixed: 0,
  panel_minority_reject: 0,
  revise_rounds_spawned: 0,
  revise_landed: 0,
  ladder_suspended: 0,
  revise_no_progress: 0,
  agent_errors: 0,
  false_stops: 0,
  no_texts: 0,
  human_reverts: 0,
};

const ZERO_CONTEXT: LearningContextRow = {
  panel_infra: 0,
  blocked_other: 0,
  diff_less_terminals: 0,
  attribution_lost: 0,
};

function makeStatus(overrides?: Partial<LearningStatus>): LearningStatus {
  return {
    project_root: "/repo",
    journal: "/repo/.odo/journal.jsonl",
    episodes: [
      {
        seq: 977,
        conversation_id: 3,
        workstream: "main",
        epoch: 17,
        window: { first_seq: 402, last_seq: 481 },
        outcomes: { ...ZERO_OUTCOMES, accepted: 3, auto_accepted: 2, rejected: 1, verify_failed: 1 },
        context: { ...ZERO_CONTEXT },
        flags_emitted: [977],
        usage: { available: true, input: 81230, output: 9402, cache_read: 0, cache_write: 1200, cost_usd: 0.182 },
        verify_ms_total: 41200,
        distill_ms: 98821,
      },
      {
        seq: 912,
        conversation_id: 3,
        workstream: "main",
        epoch: 16,
        window: { first_seq: 300, last_seq: 401 },
        outcomes: { ...ZERO_OUTCOMES, accepted: 2, rejected: 2, auto_rejected: 1, weak_rejected: 1, human_reverts: 1 },
        context: { ...ZERO_CONTEXT },
        flags_emitted: [],
        usage: { available: true, input: 60111, output: 7201, cache_read: 0, cache_write: 800, cost_usd: 0.121 },
        verify_ms_total: 30500,
        distill_ms: 81230,
      },
    ],
    episode_count: 2,
    episode_totals: { ...ZERO_OUTCOMES, accepted: 5, auto_accepted: 2, rejected: 3, auto_rejected: 1, weak_rejected: 1, verify_failed: 1, human_reverts: 1 },
    flags: [
      { seq: 977, rule: "Always run go vet before accepting", verdict: "harmful", injections: 12, rejects: 4, reject_conversations: 3 },
      { seq: 950, rule: "Prefer table-driven tests", verdict: "effective", injections: 20, rejects: 0, reject_conversations: 0 },
    ],
    flag_thresholds: { min_injections: 10, min_rejects: 3, min_reject_conversations: 3, rate_factor: 2 },
    candidates: [],
    ...overrides,
  };
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("LearningPanel", () => {
  it("renders flagged rules with verdict badges, the verbatim thresholds line, episodes, and the W3 empty-candidates state", async () => {
    mockedStatus.mockResolvedValue({ ok: true, learning: makeStatus() });
    const { container } = render(<LearningPanel projectRoot="/repo" active={true} />);

    // Thresholds surface verbatim — the GUI never re-derives the gate.
    await screen.findByText(
      "harmful: injections≥10 rejects≥3 conversations≥3 reject-rate≥2× baseline · effective: accept-rate≥2× baseline",
    );

    // One row per flag, newest-first; verdict maps to the shared Badge
    // variant (harmful → reject, effective → accept).
    const rows = [...container.querySelectorAll<HTMLElement>(".learning-flag-row")];
    expect(rows).toHaveLength(2);
    const harmfulBadge = rows[0].querySelector<HTMLElement>(".learning-flag-verdict");
    expect(harmfulBadge?.textContent).toBe("harmful");
    expect(harmfulBadge?.className).toContain("text-err-text");
    expect(rows[0].textContent).toContain("Always run go vet before accepting");
    expect(rows[0].textContent).toContain("inj 12 · rej 4 · convs 3 · seq 977");
    const effectiveBadge = rows[1].querySelector<HTMLElement>(".learning-flag-verdict");
    expect(effectiveBadge?.textContent).toBe("effective");
    expect(effectiveBadge?.className).toContain("text-ok-text");

    // Episode header folds the daemon's totals (journal-wide, not windowed).
    await screen.findByText("2 episodes recorded · totals: accepts 5 (auto 2) · rejects 3 (auto 1) · weak 1 · verify_failed 1");
    const episodes = [...container.querySelectorAll<HTMLElement>(".learning-episode-row")];
    expect(episodes).toHaveLength(2);
    expect(episodes[0].textContent).toContain("main · epoch 17 · window [402–481]");

    // W3: nothing writes candidates yet — the empty state is the contract.
    await screen.findByText(/No learning candidates yet — the candidate lifecycle lands in wave W4/);
  });

  it("renders the empty-flags hint when the audit emitted nothing", async () => {
    mockedStatus.mockResolvedValue({ ok: true, learning: makeStatus({ flags: [] }) });
    render(<LearningPanel projectRoot="/repo" active={true} />);
    await screen.findByText("No flagged rules — the rules audit has not emitted memory_audit_flag rows.");
    // The thresholds line still surfaces — it describes the gate, not the flags.
    screen.getByText(/reject-rate≥2× baseline/);
  });

  it("folds only the non-zero outcome keys, auto subsets inline; all-zero reads 'no outcomes'", async () => {
    mockedStatus.mockResolvedValue({
      ok: true,
      learning: makeStatus({
        episodes: [
          {
            seq: 977,
            conversation_id: 3,
            workstream: "main",
            epoch: 17,
            window: { first_seq: 402, last_seq: 481 },
            outcomes: { ...ZERO_OUTCOMES, accepted: 3, auto_accepted: 2, rejected: 1, verify_failed: 1 },
            // attribution_lost joins the line when >0; memory_free_outcomes
            // only ever arrives non-zero (emitted only when >0).
            context: { ...ZERO_CONTEXT, attribution_lost: 2, memory_free_outcomes: 1 },
            flags_emitted: [],
            usage: { available: false, input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 },
            verify_ms_total: 0,
            distill_ms: 0,
          },
          {
            seq: 912,
            conversation_id: 3,
            workstream: "main",
            epoch: 16,
            window: { first_seq: 300, last_seq: 401 },
            outcomes: { ...ZERO_OUTCOMES },
            context: { ...ZERO_CONTEXT },
            flags_emitted: [],
            usage: { available: false, input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: 0 },
            verify_ms_total: 0,
            distill_ms: 0,
          },
        ],
      }),
    });
    const { container } = render(<LearningPanel projectRoot="/repo" active={true} />);
    await screen.findByText("accepts 3 (auto 2) · rejects 1 · verify_failed 1 · attribution_lost 2 · memory_free_outcomes 1");
    const episodes = [...container.querySelectorAll<HTMLElement>(".learning-episode-row")];
    expect(episodes[1].textContent).toContain("no outcomes");
  });

  it("renders forward-compat candidate rows (stage badge, 12-char hash, scope, created_seq)", async () => {
    mockedStatus.mockResolvedValue({
      ok: true,
      learning: makeStatus({
        candidates: [
          {
            artifact_hash: "9f2c0011223344556677889900aabbccddeeff0011223344556677889900aabbccdd",
            version: 1,
            scope: "project:memory",
            stage: "candidate",
            created_seq: 460,
            created_at: "2026-08-30T01:12:44Z",
            invalid: false,
          },
        ],
      }),
    });
    render(<LearningPanel projectRoot="/repo" active={true} />);
    await screen.findByText("9f2c00112233");
    screen.getByText("candidate");
    screen.getByText("project:memory");
    screen.getByText("seq 460");
  });

  it("surfaces daemon failures in the settings-error banner", async () => {
    mockedStatus.mockResolvedValue({ ok: false, error: "learning fold blew up" });
    render(<LearningPanel projectRoot="/repo" active={true} />);
    const banner = await screen.findByText(/learning status failed: learning fold blew up/);
    expect(banner.classList.contains("settings-error")).toBe(true);
  });

  it("fetches on mount, then refetches on the inactive→active keep-alive edge", async () => {
    mockedStatus.mockResolvedValue({ ok: true, learning: makeStatus() });
    const { rerender } = render(<LearningPanel projectRoot="/repo" active={false} />);
    await screen.findByText(/episodes recorded/);
    expect(mockedStatus).toHaveBeenCalledTimes(1);
    rerender(<LearningPanel projectRoot="/repo" active={true} />);
    await vi.waitFor(() => expect(mockedStatus).toHaveBeenCalledTimes(2));
    expect(mockedStatus).toHaveBeenLastCalledWith("/repo");
  });
});
