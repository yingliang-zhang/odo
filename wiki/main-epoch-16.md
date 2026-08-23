# Diff #33 Auto-Land Block: Diagnosis and Panel Checklist Fixes

## Context
- User reported an unexpected review request for **diff #33**, which was stuck with `auto_land_blocked` / `reason: verify_failed` (seq 6898).
- Investigation traced pending-diff state sources in the store and auto-land trigger conditions, confirming #33 was the blocked diff; the verify failure tail and full payload were extracted.
- A prior panel convergence checklist (seq 6462/6263) defined remediation items **#10–#14**, which this session executed.

## Key decisions
- **Parallel decomposition**: 3 subagents handled #12, #13 (producer side), and #14; the main agent owned #10, #11, and #13's recovery-consumer side (`recoverPendingDiffs`).
- **Verbatim-locked text**: the blocked-detail format change was skipped because the affected text is verbatim-locked; flagged as a follow-up rather than worked around.
- GUI scope confirmed: playwright e2e tests depend on browser cache mounts, excluded from scope.

## Code changes
- **#10 — shared moa client**: Server-level shared client; `NewClientFromEnv` call sites converged; `runMoaOneShot` signature updated across callers; concurrency-cap pin tests added to `autoland_test.go`.
- **#11 — scratch-HOME verify sandbox**: `runVerify` now runs under a scratch `HOME` via a new helper; gate comment contract synchronized; four pin tests appended to `autoland_test.go`.
- **#13 — recovery exclusion**: `recoverPendingDiffs` excludes loop-owned seeds; recovery header comment updated.
- **Docs sync**: loop-design-lock row table, ADR-0003 promotion contract, README status lines, and the `user.md` writer column (learner → human) updated to match.
- **Subagent deliveries (#12/#13/#14)**: merged after build+vet; contract-level hunks spot-checked via diff stat.

## Verification
- `go build` + `go vet` clean after merge; main agent's 5 pin tests green; subagent-added and affected test groups green.
- **Full-suite regression found**: `internal/ipc` `TestPanelContextBlock` panicked at `slashctx_test.go:345` — traceable to test changes carried in by diff #33. Fixed; related tests and the full `internal/ipc` package re-ran green.

## Open loops
- Blocked-detail format change deferred (verbatim-locked); follow-up task pending.
- Final disposition of diff #33 (manual review/land or re-trigger auto-land after the fixes) awaits user decision.