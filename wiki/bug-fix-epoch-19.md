# D4 Control-Plane Hardening: Memory Write Contract + Flag Consumer

## Context

Task: implement D4 [P1] of `docs/design/control-plane-hardening-lock.md` (binding spec; ruling ④ = Sol hybrid) in the `odo` repo. D1/D3/D5/D7/D2 already landed (914a82f, d0f0a5c, 8adc8d9, fdd282a, 1c31dea). Tier-0 `gatepolicy.go` / `gate_manifest.json` and `.odo-verify` were off-limits.

Goal: make the memory write contract honest (ADR matches reality), give `memory_audit_flag` its missing consumer, and add journaled rollback.

## Key decisions

- **Scope hold before the gate, not after.** All-user.md batches are intercepted by `holdUserScopeBatch` in both auto paths (`autoApplyProposals` / `sweepPendingBatch`) before `applyResolvedBatch`, so they cost zero panel spend. Journal: `memory_update{layer:"apply", cause:"scope_held_for_human", target:"user.md", epoch, proposal_index}`. Defense in depth: apply-core asserts autoActor+user.md accepted ⇒ fail-closed error, making `planUserApply` provably unreachable from the auto path.
- **Flag consumption is hybrid (spec ruling ④).** Unconsumed `review_action{action:"memory_audit_flag"}` rows are collected in `distillCore`, deduped via `memory_update{layer:"learner", cause:"flag_consumed", flag_seq}`, and injected into the learner prompt as a verbatim DATA block (evidence, not instructions). Vetting is LLM-free: a `flag:<seq>` citation must resolve to an existing flag row AND a rule present in current memory.md, else dropped with `retract_proposal_rejected`.
- **Retract-intents never auto-apply.** Valid retract proposals ride the batch as additive `intent:"retract"` (+ `flag_seq`) on `MemoryProposal`; the panel gates them like any proposal; accepted ones emit `memory_update{layer:"memory", cause:"retract_candidate", rule, flag_seq, panel_consensus, epoch}`. Human resolution via `apply_memory` or new `odo rules retract <text>`.
- **Oscillation guard is deterministic** from memory_apply rows: retract→re-land within 3 epochs ⇒ rule marked `[frozen]` in the prompt; retract proposals rejected `oscillation_guard`. Retract intents excluded from both sets.
- **Revert restores a rebuilt pre-image.** `odo memory revert <epoch>` locates the receipt project-wide (ambiguous ⇒ refuse), verifies live bytes == receipt `after`, rebuilds the pre-image from the lane's receipt chain (fail-closed when unreconstructable), journals `memory_update{layer:"apply", cause:"revert", actor:"human", before_sha16, after_sha16}`. Second revert of same epoch refused; user.md/skill-touching batches refuse (scope: memory.md + archive).
- **Replay fold taught to retire reverted epochs' receipts** — otherwise boot replay would "repair" reverted bytes back to post-state. `unownedFoldGrowth` extended for the two new learner causes, with an attribution pin test.
- **ADR-0003 amended 2026-08-28, stale clause fixed in place:** "learner proposals wait for human apply_memory" → "learner proposals wait for the panel gate; user.md waits for human apply_memory". Invariant 1 reaffirmed: AGENTS write to no memory layer; daemon is sole writer; USER.md/global/cross-project layers remain human-written forever.

## Code changes

New files: `cmd_memory.go`, `cmd_rules_retract_test.go`, `internal/ipc/memory_flags.go`, `memory_flags_test.go`, `memory_revert.go`, `memory_revert_test.go`.

Modified:
- `docs/adr/0003-memory-architecture.md` — 2026-08-28 amendment.
- `internal/ipc/memory_autogate.go` — `holdUserScopeBatch` + sweep/auto integration + header doc.
- `internal/ipc/server.go` — distillCore flag collection; apply-core assert + `retract_candidate` rows.
- `internal/ipc/learner.go` — `learnerPrompt` flag block, flag-ref lift, consumption/reject journals.
- `internal/ipc/protocol.go` — `MemoryProposal` += `intent`, `flag_seq` (additive, ADR-0002-safe).
- `internal/ipc/memory_replay.go` — fold retires reverted epochs (`isMemRevertRow`).
- `cmd_rules_audit.go` — `rules retract` subcommand + dispatch.
- `main.go` — `case "memory"` dispatch.
- Tests: `memory_autogate_test.go`, `learner_test.go`, `cmd_rules_audit_test.go`; `TestDistillSweepSkipsRefusedBatch` rewritten to the D4 hold contract (was the one full-suite failure — pre-D4 pin expected 3 panel calls + `auto_apply_failed` for oversized user.md batch; D4 correctly yields 0 calls + `scope_held_for_human`).

## Verification

- `go build ./...`, `go vet ./internal/ipc/ .`, `gofmt -l`: clean.
- D4 focused tests all pass (~4.2s): `TestUsermdAutoApplyHeld`, `TestAutoPathUserPlanUnreachable`, `TestFlagInjectedIntoLearnerPrompt`, `TestRetractProposalNeedsFlagRow`, `TestOscillationGuard`, `TestRevertEpoch`, `TestRulesRetractPlan`, `TestUnownedFoldGrowthMemoryPipelineRows`.
- CLI smoke: `odo memory revert abc` → exit 2; `odo rules retract` (no text) → usage exit 2.
- One full `go test ./internal/ipc/ -timeout=700s -count=1` run: 1 failure (`TestDistillSweepSkipsRefusedBatch`, root-caused as above), fixed and re-run green. Changed-surface family run green (155.1s); root package green (0.893s). No second full ipc run per briefing.

## Open loops

- Cross-lane flag propagation deferred: flag collection is lane-local (flags journal on `main`; other lanes consume flags journaled there) — documented in `memory_flags.go`.
- Untouched subsystems (autoland/settle/loop/wiki/todo/skills/panel) verified by build+vet only; no second full ipc run was executed after the `TestDistillSweepSkipsRefusedBatch` fix.
- D4 changes exist in the worktree as modified/untracked files (`git status --porcelain`); no commit recorded in this conversation.
- GUI companion unchanged: new additive keys (`intent`, `flag_seq`) ignore-safely, but no GUI surfacing of retract candidates exists yet.