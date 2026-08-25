> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Diff52 Verify Block: Idempotent-Retry Contract Regression in `server.go` Heal Path

## Context

- Project: `odo` (`/Users/yingliangzhang/Projects/odo`), main at `c8677cb`; epochs 38–39 notes in scope.
- User report: "51 diff 被block了"; `review_action` confirmed `auto_land_blocked` by `auto_panel`, reason `verify_failed`.
- diff52 is the auto-revise product of diff51, carrying epoch-39 marker-first changes.

## Root Cause

diff52's verify gate (`go test ./...`) failed on `TestUserMemoryIdempotency` — consistently reproducible (3/3), not a flake. Mechanism chain:

1. Epoch-39's marker-first change makes **any** write-failed apply leave an intent marker → `findPendingBatch` judges the batch consumed.
2. Manual retry is rejected at the `handleApplyMemory` entry consumed branch (`epoch 1 already applied`) even after heal restored the file — never reaches the heal-complete success branch inside `applyResolvedBatch` (`server.go:5508-5511`).
3. Epoch-39's new pin `TestApplyMemoryCrashWindowHeals` requires the opposite: retry **after bootstrap heal** must be rejected. The two test contracts conflicted; the auto-revise round never reconciled them. Heal recovered the file, but the response contract degraded to error.

## Decision

Semantic reconciliation in the `server.go` consumed branch (not test gaming), mirroring the in-core path's existing semantics:

- Heal that **actually completes** the epoch's work this call → return `Applied: true`.
- Heal **no-op** (already persisted / bootstrap already healed / foreign state untouchable) → keep rejecting.

| Test | Contract | Result |
|---|---|---|
| `TestUserMemoryIdempotency` (legacy) | Retry whose heal completes work → success | ✅ |
| `TestApplyMemoryCrashWindowHeals` (new pin) | Bootstrap healed, retry heal no-op → reject | ✅ |
| `TestApplyMemorySingleConsumerRace` / `Idempotent` / `PinCrashWindowHeals` | Rejection semantics unchanged | ✅ |

## Code Changes

- `server.go`: consumed branch of `handleApplyMemory` now distinguishes "heal completed work" from "heal no-op" per the table above.
- Patch repacked into `.odo/diffs/6a8d51b5-c784bcdc6549.diff` (19 files, new sha prefix `81479a6b32`); `git apply --check` passes against main `c8677cb`.

## Verification

- All 5 affected tests green.
- `go test ./... -count=1` green in the diff52 worktree — the verify-gate-equivalent command (ipc suite, 533s).
- gofmt clean at the fix site; 5 residual gofmt flags are pre-existing in base, not diff-introduced — closes the epoch-39 gofmt open loop (provenance: pre-existing).
- Process note: two bash calls missed `cwd` and had to be re-run with it explicitly; no correctness impact.

## Open Loops

- Re-trigger verify/land on diff52 in the GUI (user action pending; worktree and patch already contain the fix).
- diff51 confirmation: it remains superseded by diff52 and intentionally untouched — no action unless the user wants it formally retired.