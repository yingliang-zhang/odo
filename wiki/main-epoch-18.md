# Session: Manual land of #34 + review-prompt objective anchor fix

## Decisions

- **#34 accepted after auditing deepseek's 3 technical objections** (4th comment — objective mismatch — was the known anchor bug, fixed separately):
  | Objection | Verdict | Evidence |
  |---|---|---|
  | Single-judge panel check ordered before `runErrored`, mislabels block reason | Valid — fixed | autoland.go ran `N==1` check before `run_errored`; an (errored, N=1) run journaled `single_judge_panel` and burned the once-per-lifetime quota. Order swapped in same commit: run-state evidence (`run_errored` / tainted verdict) precedes config advisory. |
  | Scratch-HOME sandbox still exports GOCACHE/GOMODCACHE/GOPATH → leak path | Invalid (overstated) | Sandbox is env-scrubbing, not chroot — absolute-path reads of `~/.ssh` were always possible; exporting 3 dir paths adds zero exposure. Pre-#11 posture inherited all of HOME (strictly worse). Credential knobs (GONOSUMDB/GOPRIVATE) stay off the whitelist. |
  | Shared client cap=5 + per-leg deadline → tail-leg starvation misreported as Infra | Theoretically valid, practically negligible, fail-closed | Production leg timeout = 900s + 65536/120s ≈ 24 min/leg; `acquire` respects ctx; starvation needs 5 slots saturated at ~24 min each. Worst case is `needs_fixes`+`panel_infra` (re-reviewable, no false accept). |

- **Ledger divergence accepted, no out-of-band DB patch**: store holds #34 as terminal `rejected` (auto-panel veto journaled), git now contains the change. Journal shows "rejected" while code landed — left for human audit instead of silent DB repair.

- **Anchor fix via schema v3 (stored goal, not re-scan)**: root cause — `originGoal` scanned the session's *latest* human message; recover / review_diff / loop-seed / revise all anchored on it, so #34 was mis-killed by an unrelated connectivity note sent later in the same session. Fix stores the correct goal at diff creation.

## Code changes

**Commit `a5c98ca` — `odo: accept diff #34`**
- `git apply` clean on `ba68fb5`; includes the autoland block-precedence swap (run-state before single-judge config check).

**Commit `abca6f1` — anchor bug fix (schema v3)**
- `diffs` table gains `goal` column; `drainRun` writes the round's review target at `InsertDiff` (revise-chain products get the chain head's goal, byte-exact).
- New `diffGoal()`: inline goal when present, falls back to `originGoal` for NULL legacy rows; all four consumers (recoverPendingDiffs, review_diff, loop seed, revise) switched.
- Migrations: v1→v3 and v2→v3 atomic ALTERs; fresh DBs record v3 directly.
- Test-side fix: recover test `Server` was missing `worktree.Manager`, causing nil panic in `retireRun` (`s.mgr.Remove`); production path unaffected — test now mirrors production shape.

## Verification

- `go build` + `go vet` green after both commits.
- Full `go test ./... -count=1` green: ~470s after #34, 468s after anchor fix.
- New regression tests: `TestRecoverReFireAnchorsStoredGoal` (re-fire e2e: stored goal used, no sudo-note leakage, full land), `TestReviewDiffAnchorProvenance` (both manual-review branches), `TestAutoLandBlockPrecedence`, plus store migration/backfill tests.
- Two gofmt debts introduced by #34 remain (wiki-documented as deliberately untouched); left as-is.

## Open loops

- **Ledger divergence on #34**: store says `rejected`, git says landed (commits `a5c98ca`/`abca6f1`). Awaiting user decision on how (or whether) to reconcile the journal evidence chain.
- Starvation scenario (5 slots × ~24 min legs → false `panel_infra`): accepted as fail-closed for now; fix is directional-only if it ever manifests.