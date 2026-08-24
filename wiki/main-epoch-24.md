# Auto-Land Memory-Path Guard: Decisions & Implementation

## Context

User observed diff #38 sitting in review despite the auto panel review pipeline and asked why. Investigation traced it to a structural block, not a review-routing regression.

## Root cause findings

- Diff #38 (goal "把P2 快修包都完成了吧", 20 files) contained a `wiki/topics/gui-visibility.md` hunk plus 4 protected gate files (`autoland`, `autonomy`, `risk`, `contradiction`).
- Pre-panel `autoLandCheck` (`autoland.go:763`) hard-blocks patches touching `.odo/` or `wiki/` prefixes as `protected_path` — terminal, no retry. The diff never reached a panel.
- Accept-time backstop `rejectMemoryPaths` (`server.go:2743`) refuses memory paths for **every actor, including a human Accept click** (2026-08-20 doctrine, diff #27 `5ec522b`, invariant 1: agents never write memory). So #38 was permanently unlandable — pending forever, burning inbox/badge space.
- Gate-source files (`protectedGateFiles`) are only **annotations** fed into panel facts; since 2026-08-22 they require **unanimous** panel attestation (majority valve has no attestation power). They are not blockers.
- `wiki/` is the daemon memory pipeline's own content area: distill/curate write it directly and auto-commit via `commitWiki` (diff #28 `397ca3c`). An agent diff carrying a wiki hunk is misrouted content — those bytes can never land through the diff channel.
- **Correction to epoch-23 note**: its "P2 diff awaits human accept" claim was factually wrong; prior manual accepts (#6, #10, #37) worked only because they touched gate source without memory paths.

## Decisions

1. **Do not narrow `autoLandCheck`.** The remaining hard blocks (`.odo/`/`wiki/`, supply-chain manifests/lockfiles) are the minimal set left after the 2026-08-20 `5ec522b` narrowing. Downgrading only the pre-panel layer is useless (executor still rejects → wasted panel spend); opening both layers deletes invariant 1 and reopens prompt-injection → 3-leg correlated failure → persistent memory pollution. Supply-chain stays blocked: one-line lockfile dependency change is an RCE vector that diff review structurally cannot judge.
2. **Fail fast instead**: reject memory-path diffs at registration time, reusing the same SSOT predicate, with zero permission change.
3. **Reject diff #38** for ledger cleanup (user executed, review_action seq 9964). Archive is append-only; patch is not lost.
4. **P2 content rerouting**: `gui-visibility.md` stuck definition goes through distill/`commitWiki`; remaining 19 files re-registered as a new diff (wiki hunk stripped), subject to unanimous panel due to gate files.

## Code changes (diff #39, 5 files, +179/−6)

| File | Change |
|---|---|
| `ipc/server.go` | `drainRun` guard after `ExtractDiff`, before `InsertDiff`: `git.PatchPaths` → `rejectMemoryPaths`. On hit: transcript advisory (names paths, points to distill/wiki-commit route, salvage patch kept in `.odo/diffs/`), retire per no-diff shape, steers ledger closed; loop runs go through failure matrix; unparseable patches pass through (backstop covers); `runMeta.refusalDetail` carries the cause |
| `ipc/loop_run.go` | `loopNoDiffAfterRun` recognizes `refusalDetail` → `run_tainted` + detail, replacing the previously unsolvable "land it manually" V5-gate advice for memory paths |
| `ipc/server_test.go` | New `TestMemoryDiffRefusedAtRegistration` (wiki + `.odo` subtests): no diff row, advisory content, salvage patch retained, worktree retired immediately |
| `ipc/loop_test.go` | `TestLoopRiskGateSuspends` wiki subtest re-anchored from `risk:protected_path` to `run_tainted` + refusal detail; supply-chain subtest unchanged |
| `docs/design/auto-land-zero-manual-lock.md` | 2026-08-24 amendment; corrected the "Loop pipeline unchanged" section's memory-path description |

Audit notes: `InsertDiff` has a single production funnel (`drainRun`), so the guard covers all registrations; revise-ladder products with memory hunks (legacy origin only) are refused identically; accept-layer `TestDiffGuardRejectsProtectedPaths` unaffected — double-layer structure retained. GUI unchanged (no diff row → no inbox/badge entry).

## Verification

- `go build ./... && go vet ./...` clean.
- `go test ./internal/ipc/ -count=1`: all green, 506s (incl. two new tests + updated loop case).
- `go test . ./internal/store ./internal/git`: green.
- Diff #39 touches no protected/memory paths → eligible for normal verify + majority-panel auto-land.

## Open loops

- Diff #39 (registration guard) was pending in the auto pipeline; the running daemon still uses the old binary — registration-time refusal only takes effect after #39 lands **and the daemon restarts**.
- P2 re-registration: strip the `wiki/topics/gui-visibility.md` hunk from the remaining 19 files and register as a new diff; until #39 is live this stripping must be done manually or the #38 deadlock repeats.
- The `gui-visibility.md` stuck-definition content still needs to land via the distill/`commitWiki` route, not an agent diff.