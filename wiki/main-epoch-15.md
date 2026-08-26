> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo P1 Batch: Review Fixes #5–#9 Implemented and Verified

## Context

Continuation of the P1 optimization batch in the `odo` daemon codebase. Session implemented review findings #5–#9 (orchestrator P1 checklist), each with dedicated tests, then ran full verification and synced design-doc contract sentences.

## Key decisions

- **#5 SQLite DSN hardening**: disable mmap (`_pragma=mmap_size(0)`) and force `synchronous(FULL)` on both read-write and read-only open paths. RO path shares the live WAL, so it carries the same SIGBUS class — both got the fix.
- **#6 C10 loop-admission atomicity**: refactored both loop-start paths so state fold + `loop_started` journal append happen in one critical section. Early fold outside the lock is retained only as a fast-reject hint; the in-lock re-fold is the atomic check. Preserved each path's existing error precedence (an existing test pins `"already"` ahead of `"nothing_to_audit"`); mechanism unified, precedence untouched.
- **#7 Journal read failure propagation**: `runMemoryLayers` now returns an error and `assembleRunPrompt` refuses to assemble a blind prompt. All four callers already had fail-closed paths that take over unchanged. Honestly bounded: if the journal is truly dead, the failure append also fails and the user sees a store error rather than the refusal cause — but a blind run becomes impossible; that is the contract.
- **#8 Single-judge auto-land disarm**: with only 1 review model configured, the panel is no longer armed. Silent disarm plus a once-per-daemon-lifetime `auto_land_blocked{single_judge_panel}` advisory (`Server.singleJudgeAdvised` atomic). The `review_diff` advisory surface is deliberately not capped.
- **#9 Unbounded panel legs**: added a one-line `WithTimeout(s.legTimeout(model))` wrapper in each leg goroutine, with a `legTimeoutForTest` seam. Same defect class found and fixed in `reviewWithModel` (shared auto-land/review_diff/skills-distill funnel) and in the `/vision` and `/preview` legs — all previously unbounded.

## Code changes

| # | Change | Key locations | Tests |
|---|---|---|---|
| 5 | DSN pragmas: mmap off, sync FULL, RO path too | `store.go:180` | Extended `TestOpenMigrates` asserts; new `TestOpenReadOnlyDisablesMmap` |
| 6 | Atomic fold+append in both start paths | `loop.go` | `TestLoopAdmissionConcurrentSingleWinner` (4 goroutines, exactly 1 winner, `-race` green) |
| 7 | `runMemoryLayers` returns error; prompt assembly refuses on journal read failure | `runMemoryLayers`, `assembleRunPrompt` + 4 call sites | `TestRunMemoryLayersJournalReadFailure` |
| 8 | N=1 disarm + once-only advisory | `autoland.go:280+`, `Server.singleJudgeAdvised` | `TestAutoLandSingleJudgeUnarmed` (2 attempts → 1 advisory line, 0 panel calls, diff stays pending) |
| 9 | Outer deadline on panel legs + funnel + vision/preview legs | leg goroutines, `reviewWithModel`, `/vision`, `/preview` | `TestPanelLegOuterDeadline`, `TestReviewLegOuterDeadline` (Infra fail-closed → needs_fixes) |

Incidental fixes discovered during the work:

- **Fixture blast radius**: the autoland/runverdict/verify_advisory test fleet widely used single-model `review: rm1@test` — confirming the #8 finding was real. All flipped to 2-model configs (including call-count guards and per-call side-effect stubs).
- **`TestVerdictBlocksUnit` hermetized**: it implicitly depended on a real `~/.odo/prefs.md` and would fail on any machine without one — a pre-existing flakiness root, fixed in passing.
- **Test determinism for #9**: handler stubs use bounded sleep instead of relying on connection-cancel semantics for deterministic shutdown.

Docs synced (docs-are-prompts rule): `loop-design-lock.md` C10 gained the atomicity clause (contract sentence now true); `auto-land-zero-manual-lock.md` gained the panel-size arming rule section.

## Verification

- `go build` / `go vet` green.
- Full `go test ./... -count=1` green (internal/ipc 457.0s; remaining 6 packages ok).
- Key concurrency surfaces green under `-race`.

## Open loops

- P1 items #10–#14 untouched (review-lane concurrency semaphore, verify scratch-HOME environment, learner throttling, loop-attribution `diff_id` + journal rotation) — awaiting user approval to proceed with the next batch.
- `wiki/topics/daemon-restart-recovery.md` still says the SIGBUS issue is "still unresolved" — wiki is daemon-owned and protected, so the rewrite to "remedy landed" is deferred to the next curate cycle.
- The mmap-disable remedy (#5) is a journaled therapy, not a proven root cause (modernc WAL recovery memcpy); production recurrence remains under observation.