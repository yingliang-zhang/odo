> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Auto-Distill Daily-Cap Feedback Loop Fix (Epoch Regeneration)

## Context

Re-implementation of the daily-cap circuit breaker for odo's auto-distill scheduler. The previous dispatch (#81) drifted — its auto-revise round tried to rebuild the diff from task text against a stale worktree and produced nothing (HEAD was the wiki distill; diff #80 was memory replay). This epoch was a fresh dispatch on current main, implemented from scratch against the live repo, plus three round-1 panel fixes.

## The defect (production-observed)

When auto-distill's daily cap was reached, every subsequent activity re-triggered the cycle: urgent trigger journaled `memory_update{cause:"scheduled"}` → fire discovered `daily_cap` → journaled `memory_update{cause:"skipped", reason=daily_cap}` → the window was never folded → next activity re-fired. Bookkeeping rows themselves grew the eligible window (production: 3786→3819 events, 497KB→565KB of pure scheduler noise after cap hit). Root cause in code: `runAutoDistill` did `skip("daily_cap")` then returned with no re-arm, leaving arm→fire→skip to self-perpetuate.

## Decisions (owner-approved design; cap numbers unchanged — trigger behavior only)

1. **Suspend once, not per event**: on first cap hit per day, journal exactly one `memory_update{cause:"cap_suspended_until", detail:"<RFC3339 earliest quota release>"}`; journal nothing further (no repeated scheduled/skipped) until the horizon passes.
2. **Single timer**: at most one pending timer aimed at the earliest quota-release time (oldest counted distill exiting the 24h window); activity while suspended does not reschedule.
3. **Bookkeeping excluded from the eligible window**: scheduler rows (`scheduled`, `skipped`, `cap_suspended_until`) don't count toward event/byte eligibility (`measureWindow` in `internal/ipc/auto.go`; matching exclusion in window rendering in `internal/ipc/server.go`).
4. **GUI**: suspended state renders "今日额度已用完 · 预计恢复 `<time>`" from the row, falling back to computed oldest-distill + 24h for old journals (upgrade path, no crash).
5. **Resume**: timer fires with quota available → one normal scheduled row, distill runs, suspension cleared — no catch-up storm.

Round-1 panel fixes folded in:

- **FIX 1 — race-free first-hit**: serialized check→journal→cache across concurrent `runAutoDistill` goroutines; exactly one `cap_suspended_until` row and one timer per suspension window. Proven under `-race` with a two-goroutine shared-project drill.
- **FIX 2 — timer resilience**: `runAutoCapResume` re-arms (or re-suspends) on conversation/workstream lookup failure instead of silently dying; the activity gate can no longer permanently swallow a project's activity on timer loss.
- **FIX 3 — badge hygiene**: `autoCapResumeForBadges` and the GUI gate on auto-distill being enabled — disabled hides the chip and kills stale cached impressions past the timestamp; re-enable restores it.

## Code changes

- `internal/ipc/auto.go` — suspension/journal/timer logic; `measureWindow` excludes scheduler bookkeeping from eligibility.
- `internal/ipc/server.go` — window-exclusion rendering; IPC payload decode adjust (payload returned whole, decoded at ipc layer); comment/return-value consistency fix.
- `internal/ipc/auto_cap_test.go` — new drill suite (D1–D5: cap silence, window fold, resume, upgrade fallback, race; plus `TestAutoCapResumeAtMath`). Two drill-side assertion bugs fixed during bring-up: D2 had to count the last truthful row rather than totals given a `cause:"scheduled"` noise fixture; D3's badge assertion moved before the distill trigger since the post-resume marker lands inside the 24h window.
- GUI: suspension chip ("今日额度已用完 · 预计恢复 `<time>`"), enabled-gate on chip/impression cache, new vitest + playwright e2e coverage.

## Verification

- `go build ./...` clean; `go vet ./internal/...` clean; `gofmt -l internal/` empty.
- `go test -race` full suite green (ipc ≈ 538s + store; includes the FIX 1 concurrency drill).
- New e2e chip tests 3/3 passed (suspend row + disabled → chip hidden; re-enable → chip returns).
- Final full gates (full playwright + full `go test ./internal/...`) were launched at end of epoch.

## Open loops

- Final results of the last-launched full playwright suite and full `go test ./internal/...` run were not captured in this log slice — confirm green before closing the epoch.