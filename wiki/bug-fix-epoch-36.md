> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# D9-W5 — rollback, freeze stage-interrupt, stall advisory, never-score (odo learning control plane)

## Context

Wave W5 of the D9 Learning Control Plane design lock (`docs/design/d9-learning-control-plane-lock.md`; detailed spec `docs/design/learning-control-plane-d9.md` §4–§6), implemented daemon-side in the odo repo. Base: main `63bdaa1` (W4-R restore, which put the W4 core files — `learning_candidate.go`, `learning_lint.go`, `learning_replay.go`, `learning_scoring.go`, `learning_stages.go`, `learning_canary.go` — back on main from the diff #114 archive); earlier waves W3 = `bafb61d`, W4.5 suite accel = `acf96f9`. Diff #118 (combined W4+W5) had been panel-rejected solely for bundling W4; its round-2 findings (GLM decorated-text mismatch, `settle.go` must stay untouched) were carried into this wave. Pre-task daemon feed showed two `auto_land_blocked` events (verdict `needs_fixes`, reason `panel_infra`), a gate-policy check, a refresh attempt, then a reject.

Hard constraints: `internal/ipc/**` only (Tier-1), `settle.go` untouched, no Tier-0, no supply-chain, GUI additive only (LearningPanel deferred to W6), edits route human-accept-preferred, worktree left dirty.

## Key decisions

- **W5 recovered from the diff archive, not the lost worktree.** The prior session's 27-file staged changeset existed in no worktree, but `.odo/diffs/6a9502f7-98d8cf4a081c.diff` (265 KB) preserved it completely. Applied with `git apply --3way` onto main `63bdaa1`: 21/27 files merged byte-identical to HEAD (W4-R restore already carried them, including the cohort-mode `assembleRunPrompt` signature in `settle.go` and the canary audit row in `cmd_rules_audit.go`); the 3 conflicts (`learning_stages.go`, `server.go`, `learning_lifecycle_test.go`) were all "ours = W4 half-done / theirs = W5 complete" and were resolved per-hunk by taking theirs. True W5 increment: pure additions, +2,319 lines.
- **R1 rollback is candidate-layer-only (binding user ruling).** Trigger: harmful tuple on a candidate's own `delta.add`, evaluated at per-epoch measure cadence. Rollback = instant fold `project_active → rolled_back`, zero `memory.md` writes. If the candidate's text had landed in `memory.md` via the receipted `project_active` path, the fold additionally emits the existing D4 receipt `memory_update{layer:"memory", cause:"retract_candidate", rule, flag_seq, candidate, epoch}`; the human resolves via `apply_memory` / `odo rules retract`. The daemon never deletes the `memory.md` line itself. Restore bounded to the candidate's own `delta.add` — never opaque/human lines, never sibling candidates.
- **Freeze-set check now matches the production journal shape (GLM #118 finding — fixed this session; the prior session had not landed it).** Production `learning_frozen` rows journal decorated strings (`text + " (" + reason + ")"`) while `learningCandidateFreezeSet` folded bare `p.Texts`, so a bare key could never hit the frozen set. `learningFrozenBareText` now strips the decoration by splitting on the fixed marker prefix `" (oscillation_guard: "` (tolerant of parentheses inside the reason). Pinned by a decorated-form fixture in `learning_lint_test.go` plus an e2e round-trip (production journal row → fold) assertion.
- **Two-pass freeze set within a measure tick**: pass-1 (rollback) folds before pass-2 (promotion), closing the same-tick same-text re-entry hole.
- **Stall is advisory-only**: shadow/canary candidates aging > 12 epochs without promotion-worthy evidence journal `learning_stall` once per (hash, stage); never auto-promotes, never auto-drops.
- **Never-score extended to promotion cohorts**: both legs of a paired cohort exclude auto rows, other canaries, and scoring-excluded rows (`learningScoringClassify` fail-closed), with paired minimums of ≥10 human outcomes per leg.
- **Evidence → measure → gate separation is greppable and pinned**: the gate consumes only the measure struct; `TestLearningGateSignaturesSeparation` AST-pins the signatures so no single evidence row can move a stage.
- **`settle.go` untouched** (hard #118 panel constraint): `git diff HEAD -- settle.go` empty; no call-site re-migration.

## Code changes

9 files staged, +2,338/−28, all `internal/ipc/**`; worktree `6a9514e5` left dirty per instruction.

- `learning_rollback.go` (new): marker-first `learning_rollback{retracted, rules, present, measure_seq}` journal, stage fold, bounded receipt emission (receipt only when the presence fold hits the candidate's own `delta.add`).
- `learning_stages.go`: frozen stage-interrupt (`learningFrozenHits` + `journalLearningFrozen`, once per hash+stage), two-pass freeze set, `learning_stall` advisory.
- Lint-side freeze-set fold: `learningFrozenBareText` decoration-stripping fix.
- Promotion-cohort scoring: fail-closed exclusions + paired ≥10 minimums.
- `server.go`: wiring for the new folds/events.
- Tests (+14 new): `TestLearningRollbackR1TwoLayer` (two-layer receipt; sibling with identical text untouched; absent text → `present:false`, zero receipts; `memory.md` byte-identical), `TestLearningFreezeLintBoundaryE2E` (N+1 rejected via `oscillation_guard`), `TestLearningFreezeStageInterruptBoundaryFree` (N+4 free), `TestLearningFreezeStageInterrupt` (decorated production row round-trips through the fold), `TestLearningStallAdvisory` (fires; stage stays shadow), `TestLearningMeasureNeverScoreExclusions`, `TestLearningPromotionPairedGuard`, `TestLearningGateSignaturesSeparation`.

Also verified in place (not only via tests): the canary-measure promotion arm applies marker-first, converges idempotently, journals hold/drop, and treats stall as advisory-only.

## Verification

```
gofmt -l internal/ipc/                          → clean
go build ./... && go vet ./internal/...          → BUILD_VET_OK
go test ./internal/ipc/ -run 'Learning' -count=1 → ok  9.182s
go test ./... -timeout=20m                       → EXIT=0 (all green)
  ok  odo 2.080s · adapter 0.699s · git 21.323s · ipc 517.143s
  ok  moa 1.010s · modelspec 0.757s · store 0.703s
```

IPC 517s vs the 507s W4.5 baseline, consistent with the 14 new tests. Closed accounting item: the two auto-apply → `memory.md` default-preference assertion fixes are already in HEAD (`63bdaa1`); full suite shows no residual stragglers.

## Risk note

Freeze-set decoration stripping splits on the fixed prefix `" (oscillation_guard: "`. If the freeze reason format ever changes without syncing `learningFrozenReasonMarker`, decorated rows would lose freeze effect — the pinned tests go red immediately, so the failure mode is a loud red suite, not silent drift.

## Open loops

- **Land decision pending** — the staged W5 changeset (9 files, +2,338/−28) awaits the human `accept_diff` valve; worktree `6a9514e5` intentionally left dirty.
- **W6 GUI deferred** — LearningPanel stage feed, stall-advisory rendering, and the human accept/retract valves are the next wave per the design lock.
- **Wiki/epoch governance correction outstanding** — `wiki/bug-fix-epoch-35.md` still records W4 as landed at `8979516`; that is incorrect (W4 landed later via the W4-R restore). Fixing the wiki is outside this diff's scope and still to be done.