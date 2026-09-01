# D9 Lock Amendment A1: replace replay f/g gates with canary-side f′

## Context
- Authoritative spec: `docs/design/d9-lock-amendment-a1-fg-canary.md` (read first; every implementation clause cites it).
- Review verdict: quad-blind 4/4 ACCEPT_WITH_FIXES; user ruling: Option 1. All file:line anchors pre-verified against main by the four review legs.
- Scope guard: `learning_replay.go`, `learning_measure.go`, `learning_stages.go` are Tier-1 but NOT in the 10-file `protectedGateFiles` list → normal auto-land path. `gatepolicy`, `gate_manifest`, `.odo-verify/settle.go` untouched.

## Key decisions
- **Delete replay checks f and g**; keep a–e, h, provenance. Per DSF review, `prevented_harm`/`friction` counters are retained as telemetry — only the f/g gating is removed.
- **f′ lives in `learningPromotionVerdict`** (`learning_measure.go`): `liveHarm := m.Live.Rejects + m.Live.WeakRejects; f′ ⇔ liveHarm ≥ 1`, placed after the paired-minimums floor and before the reject-rate leg. Below floors → verdict stays `""` (unchanged). Per-check boolean rides the detail map.
- **Amendment 3 grace**: an f′ miss does not drop immediately; `efficacy_vacuity` fires only after a 3-epoch grace, keyed by a new `stage_epoch` field on the measure row (populated by `learningCanaryMeasure` from `learningStageMainEpochAt`, `learning_stages.go:493; additive, no migration).
- **Two new canary drop exits**, both writing ZERO freeze-set entries (vacuity ≠ harmful; R2 untouched):
  - `efficacy_vacuity`: floors met ∧ liveHarm=0 ∧ canary age ≥ `learningShadowAgingEpochs` (3).
  - `canary_starved`: canary age > 2×`learningStallMainEpochs` (24) with floors unmet; detail carries `m.Excluded`.
- **Amendment 4 seam bug**: `learningShadowCheckpoints` must not flip shadow→canary when `learningCanaryFraction() <= 0`.
- **Sol fix**: stall-advisory dedupe made epoch-keyed so a re-cycled candidate re-advises honestly (busy-but-vacuous hole subsumed by the drop exits).
- **Amendment 5**: the live-harm attribution join f′ uses is the same shared predicate implementation as replay-era attribution — single source, no drift; double-execution fixture extended to cover the measure fold.
- **Fixture-baseline discovery**: the 4 existing canary tick fixtures had liveHarm=0 and would trip the new vacuity drop; they gained live rejects (baseline correction, not masking).
- **Edit strategy**: when a multi-op edit partially failed (f/g left in the false-set; overlapping ranges; indentation pollution), the whole op set was rolled back for atomicity and re-sent with smaller anchors.

## Code changes
Production (internal/ipc learning subsystem):
- `learning_replay.go` — removed f/g comment block (28–60), evaluation block (~618–632), and their Violations entries; `prevented_harm`/`friction` kept as counters; shared predicate wired in.
- `learning_measure.go` — added `StageEpoch` to the measure row; shared attribution predicate; `learningPromotionVerdict` gained f′ plus the two drop-exit verdicts.
- `learning_stages.go` — seam-disabled gate fix in `learningShadowCheckpoints`; `stage_epoch` journaled by `learningCanaryMeasure`; verdict dispatch for `efficacy_vacuity`/`canary_starved`; `journalLearningStall` dedupe epoch-keyed.

Tests (extended existing files, table-driven, mirror names):
- `learning_replay_test.go` — f/g fixtures now expect PASS at replay (moved-out proof); a–e/h/provenance pins intact; check list updated.
- `learning_measure_test.go` — fixture baselines updated with live rejects; f′ predicate table test; shared-attribution pin (cross-fold consistency + double-execution extension covering the measure fold).
- Stages/lifecycle tests — three new tick-level tests (vacuity drop, starved drop, epoch-keyed stall re-advisory); Amendment 4 seam regression (fraction=0 + shadow aging complete ⇒ stays shadow, no canary entry); zero-freeze-entry assertions for both vacuous drops; `stage_epoch` journaled additively; stale comments updated.

## Verification
- `go build ./...` clean; `go vet ./internal/...` clean; gofmt clean.
- Focus gate: `go test ./internal/ipc/ -run 'Learning' -count=1 -timeout=900s` — pass; 13/13 relevant tests confirmed actually executed (not just suite-pass).
- Full suite `go test ./... -timeout=20m` launched via nohup in the background; worktree intentionally left dirty per instructions.

## Open loops
- Full-suite (`go test ./... -timeout=20m`) result was not observed — it was still running in the background when the session ended.
- Run worktree left dirty by design; awaiting user review / auto-land decision.
- Final per-item report with gate tails and the one-line risk note still owed to the user once the full suite completes.