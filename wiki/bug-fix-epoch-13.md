> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# `revise_product_missing`: loud drain failure + canonical worktree prompts (odo epoch 13)

Bug fix for the revise-ladder wedge — a repair run staging its work in a sibling worktree produced a forever-silent `pending` origin diff. Status: implemented, staged as pending diff #85, not yet landed.

## Problem

- `startReviseRun` (`internal/ipc/settle.go`) spawns repair runs in a FRESH detached worktree from HEAD; the repair prompt embeds the origin diff but never names the run's own worktree as the canonical checkout.
- Sibling worktrees of the same conversation sit side-by-side under `.odo/worktrees/`; the agent sometimes edits/stages in the origin run's worktree instead.
- `drainRun` (`internal/ipc/server.go`, `ExtractDiff(meta.worktreePath, …)` ~L2794) then extracts an empty diff → no diff row, no `agent_error`, ladder waits forever after a clean `agent_done`.
- Incidents: 2026-08-26, diffs #81 and #83.

## Design lock (2026-08-27, three-leg panel consensus)

Two changes, exactly:

1. **Loud failure at drain** (`server.go` empty-diff branch, new case before the false-stop retry case):
   - Condition: `meta.originDiffID > 0` AND `!meta.errored` AND `verdict != verdictFalseStop`.
   - Journal `review_action` `{action:"revise_product_missing", actor:"auto_panel", origin_diff_id, run_dir_id, detail:<worktree path>}` + human-readable `journalRunAdvisory` (recovery: `git -C <sibling> diff --cached` + zero-change re-snapshot run).
   - Then the existing default tail (`journalSteersDropped`, parked-goal / `maybeAutoAfterActivityLocked`); NO false-stop retry, NO continuation.
   - `ladderState` ignores unknown actions and the advisory is a plain `agent_error` row — no schema/ladder changes.
2. **Canonical-worktree declaration in both ladder prompts** (`settleRepairPrompt`, `settleRebasePrompt`):
   - Reordered `startReviseRun` to create the worktree FIRST (after admission gates, before prompt assembly/journal); absolute `wtPath` passed into both prompt builders; new near-top "WORKTREE (canonical)" section (absolute path; all edits/staging there; other `.odo/worktrees/` dirs are read-only; diff extracted from this checkout only).
   - Worktree-create failure now leaves NO user_message/round rows; the caller's `revise_spawn_failed` ledger row closes the round.

Constraints honored: only `internal/ipc/server.go` + `internal/ipc/settle.go` (+ tests); no journal schema, `ladderState`, verify gate, risk classifier, GUI, or distill changes; English why-comments citing #81/#83; no commit — pipeline commits on accept.

## Implementation discoveries

- **`startFollowupRunLocked` did not propagate `originDiffID`** to the retry's `runMeta` — added propagation, required by locked test 4 (a false-stop retry of a revise run that again ends clean-and-empty must journal `revise_product_missing`).
- `ladderState`'s fold confirmed to ignore unknown `review_action` actions (verified by read, no change needed).
- The seal-drill absence assertion in `autoland_test.go` became vacuous after the prompt text change; the pin was re-anchored to a string still present in inputs.
- New `counterLines`-based test wrapper fact: the wrapper overwrites the counter value rather than appending newline-delimited lines; assertions read the value instead of counting lines.

## Code changes (staged, diff #85, 754 lines, sha16 `76208f46f643afbb`)

- `internal/ipc/server.go` — new `revise_product_missing` case in `drainRun`; `originDiffID` propagation in `startFollowupRunLocked`.
- `internal/ipc/settle.go` — canonical-worktree block helper + both prompt builders receive/state the worktree path; `startReviseRun` reorder (worktree create before prompt assembly; failure path returns `worktree_create: …` with no journal rows); `settleDraft` call site updated.
- `internal/ipc/revise_product_missing_test.go` (new) — clean+empty revise run → action + advisory + no retry/continuation; errored run → no action; non-revise run → no action; false_stop verdict → retry fires, retry's clean-empty finish journals action; prompts embed path verbatim; worktree-create failure leaves no `auto_revise`/`auto_revise_round` rows.
- `internal/ipc/settle_test.go` — `TestSettleRepairPromptUnit` updated for new signature + worktree assertion; `internal/ipc/autoland_test.go` — seal-drill pin re-anchored.

## Verification

- Green in-run: `go build ./...`, `go vet ./internal/...`, `gofmt` clean; 7 targeted tests pass; adjacent ipc suites pass.
- Not completed before the run yielded: `go test ./internal/ipc/... -count=1` (kicked off in background, ~8–9 min, no journaled result) and full `go test ./... -count=1`.

## Current state (verified 2026-08-27 16:34 CST)

- Work staged in the run's canonical worktree `.odo/worktrees/6a8fdbad-dea30e0d887a` (5 files, index clean-shaped); main checkout untouched (`revise_product_missing` absent).
- Diff #85 `pending` in conversation 3; journal seq 8479 shows `review_action auto_land_started, stage:verify` for diff #85; **no accept/reject verdict recorded**.

## Open loops

- Diff #85 auto-land: verify stage started (seq 8479) but no terminal verdict in the journal; `settle.go` is a protected gate file → the diff lands via the human Accept path as designed; pipeline/daemon must resume reviewing the pending diff.
- Gates outstanding: `go test ./internal/ipc/... -count=1` completed after `agent_done` with no captured result, and full `go test ./... -count=1` was never run in the run; rerun (or rely on the accept-path verify) before treating the fix as settled.
- Efficacy of the prompt-side guard (Change 2) unproven in production: the next revise run is the first real test; Change 1 is the designed detector — a recurrence should now surface as a `revise_product_missing` row instead of a silent wedge (watch the first post-land revise runs for one).
- Prior epochs' context: #81/#83 were already superseded/accepted-separately (epoch 11/12 wiki pages, incl. epoch-11 addendum at `a1b4454`); confirm no follow-up repair owes them once #85 lands.