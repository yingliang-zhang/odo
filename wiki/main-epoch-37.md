# Odo: Diff #49 Block Misdiagnosis & #50 Spec Root-Cause Fix

## Context

Auto-land pipeline blocked diff **#49** (`review_action: auto_land_blocked`, reason `verify_failed`). Investigation concluded the block was a **false positive** caused by a deterministic design flaw in a Playwright e2e spec, not a code regression. User manually accepted #49; root-cause fix landed as #50 and was auto-accepted.

## Key decisions

- **#49 block = collateral flake, accepted manually** (precedent: #42, #48). Failure was verify-gui leg `switch-cache.spec.ts:64` (118/119 passed). Evidence chain:
  - Archive integrity: `sha16=f8cde89f90d1eb5c` matched on-disk `.odo/diffs/6a8d282b-bd19909ce538.diff` recomputation.
  - Payload attribution: 7 files (autoland/loop_run/server/sweeper + context-panel e2e + 2 tests), **zero switch-cache paths** — regression via this diff excluded.
  - Same-bytes reproduction on candidate worktree `6a8d282b`: `--repeat-each=5` ×2 → **3/10 flaky** on spec:64 under `workers: 1`; intermittent failure independent of external load.
  - Full-suite rerun judged negative-information (≈coin-flip re-block risk) — skipped.
- **Root cause is design-level, not inferred**: `gui/src/dev/mock-invoke.ts:29-35` evaluated the `fail` branch **before** `delayMs`, so arming fail made bootstrap reject immediately — no guaranteed window between optimistic-toggle render and rollback. The spec's single-shot `FEAT_MARKER.count()` (line 90) sampled outside the window → 0. Same rAF-race class as the context-panel fix inside #49. Sibling test (:38) pinning the window with `delayMs: 2500` was 10/10 stable — counter-evidence.

## Code changes (#50; 2 files, +14/−4)

- `gui/src/dev/mock-invoke.ts` — moved `delayMs` check **before** the `fail` branch in the bootstrap path; fail+delay now holds then rejects, giving failures a deterministic window. Landing-signal semantics preserved (knobs still consulted before `makeBootstrap`/landings increment).
- `gui/e2e/switch-cache.spec.ts:93` — test 2 arming changed to `{delayMs: 2500, fail: true}` (window pinned same as test 1); `delayMs` reset after sampling (already-scheduled timer still fires the failure; subsequent assertions are web-first).

**Same-class site audit** ("fix one, audit all"): `bootstrapCtl` blast radius = 3 files, no vitest coverage. Swept all e2e for "arm fail → immediate single-shot sample": `advisory-slash.spec.ts` (web-first assertions — excluded), `sidebar.spec.ts` `staleWindowProbe` (window-pinned absence assertion — different class), only `switch-cache.spec.ts:90` affected — fixed.

## Verification

- `npx tsc --noEmit` — green.
- `switch-cache.spec.ts --repeat-each=10` (exact pre-fix repro condition): **20/20 PASS, 0 flaky**; :64 stable ~4.9s including pinned 2.5s hold.
- Full suite `npx playwright test` (parallel workers, gate load conditions): **119/119 PASS** in 6.4 min.
- #49 landed on main as `193ce90 odo: accept diff #49`; worktree clean before #50 packaging (drain = `git diff --cached HEAD`, no double-packaging).
- #50 drained and accepted by `auto_panel` (seq 13623).

## Open loops

None.