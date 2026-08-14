# DESIGN LOCK: P0a — Stale-Diff Rebase/Refresh Mechanism

> Tri-model MoA consolidation (K3/GLM/DSF, --thinking max, 540s, blind-sealed). 3/3 converged on core design with minor variations resolved below.

## Core insight (3/3 convergence)

`git apply --3way` IS the rebase mechanism. The diff file already embeds blob OIDs from the base SHA's tree; `--3way` uses these to do a real 3-way merge against the current tree — even when HEAD has moved. The only thing making stale diffs unlandable today is `checkBaseFresh` (server.go:1715) refusing before the apply is ever attempted.

**The refresh mechanism: remove the hard refusal, attempt the apply, classify the outcome.**

## Contract (3/3 convergence)

One new journal row type — additive `action` value on existing `EventReviewAction` (ADR-0002 immune):

```json
{
  "action": "refresh_attempted",
  "diff_id": <int64>,
  "base_sha": "<diff's original base SHA>",
  "target_sha": "<current HEAD at refresh time>",
  "outcome": "clean" | "conflict" | "error",
  "actor": "" | "auto_panel",
  "detail": "<git stderr tail, conflict/error only>",
  "phase": "pre_spend_probe" | "accept_apply"
}
```

Semantics:
- **clean**: 3-way merge succeeded. `d.BaseSHA` updated to `target_sha` via new `UpdateDiffBaseSHA`. For accept path, the diff is already applied — proceed to `CommitPaths`. For auto-land pre-spend, proceed to verify+panel.
- **conflict**: 3-way merge produced conflict markers. Main checkout rolled back (if touched). Diff stays `DiffPending` (NOT `DiffConflict`). Caller returns `errBaseStale`.
- **error**: git failure (missing blobs, worktree-create failure). Same as conflict. Fail closed.

**No new event types. No new diff statuses. No new deps. No schema migration.**

## Two refresh sites (K3 originated, GLM/DSF converged)

### Site 1: Human accept (`handleDiffAction`, server.go:1735)

**Replace `checkBaseFresh` with `checkAndRefreshBase`** — a new function that:

1. Reads current HEAD (`git.CurrentSHA`)
2. If base is nil/empty → skip (grandfathered, fix-INT D4 preserved)
3. If head == base → return (false, nil) — base is fresh, caller applies normally
4. If head != base (stale) → **attempt refresh**:
   a. `CapturePatchBaseline` (existing, server.go:1805) — capture rollback state
   b. `git.ApplyDiff(--3way)` (existing, git.go:121) — the actual rebase
   c. Clean → `UpdateDiffBaseSHA` + journal `refresh_attempted{outcome:"clean"}` → return (true, nil) — diff already applied, skip caller's ApplyDiff
   d. Conflict → `RollbackPatchApply` + journal `refresh_attempted{outcome:"conflict"}` → return (false, errBaseStale)
   e. Error → `RollbackPatchApply` + journal `refresh_attempted{outcome:"error"}` → return (false, err)

The accept branch order becomes:
```
acceptMu.Lock()
protected-path check (unchanged)
unmerged-index check (unchanged)
checkAndRefreshBase(ctx, &d) → (refreshed bool, err)
  if refreshed: skip CapturePatchBaseline + ApplyDiff (already done)
  if !refreshed && err == nil: CapturePatchBaseline + ApplyDiff (existing fresh path)
  if err != nil: return err (diff stays pending)
CommitPaths (unchanged)
UpdateDiffStatus(accepted) + journal accept row (additive: refreshed_from_sha when set)
retire worktree (unchanged)
```

### Site 2: Auto-land pre-spend gate (autoland.go:246-258)

**Dry-run probe in a throwaway worktree** (Option C, 3/3 converged):

1. `git worktree add --detach <tmp> HEAD` — isolated, never touches main checkout
2. `git apply --3way <diff>` in the temp worktree
3. Clean → `UpdateDiffBaseSHA` + journal `refresh_attempted{phase:"pre_spend_probe",outcome:"clean"}` → **fall through** to verify+panel
4. Conflict/error → journal `refresh_attempted{...conflict|error}` + `journalAutoLandBlocked("base_stale", detail)` → return
5. `defer git worktree remove --force <tmp>` — cleanup unconditionally

**Pre-spend probe runs under `autoLandMu` only (NOT `acceptMu`)** — the temp worktree is isolated, never touches main. Accepts stay unblocked during a probe.

### Auto-land final check (autoland.go:382)

**Unchanged code path** — `handleDiffAction` (called from autoland.go:377) now contains `checkAndRefreshBase` which replaces `checkBaseFresh`. If refresh fails, `errBaseStale` is returned, and the existing `errors.Is` branch (autoland.go:382) journals `base_stale_at_land` with the panel riding. A `refresh_attempted` row precedes the `base_stale_at_land` row.

## Functions

### New functions

| File | Function | Signature |
|---|---|---|
| `internal/git/git.go` | `ProbeApplyClean` | `func ProbeApplyClean(repoPath, diffPath string) (clean bool, detail string, err error)` — temp worktree probe |
| `internal/store/diffs.go` | `UpdateDiffBaseSHA` | `func (s *Store) UpdateDiffBaseSHA(ctx context.Context, diffID int64, baseSHA string) error` — plain UPDATE |
| `internal/ipc/server.go` | `checkAndRefreshBase` | `func (s *Server) checkAndRefreshBase(ctx context.Context, d *store.Diff) (refreshed bool, err error)` — replaces checkBaseFresh |
| `internal/ipc/server.go` | `journalRefreshAttempt` | `func (s *Server) journalRefreshAttempt(ctx context.Context, d store.Diff, phase, outcome, baseSHA, targetSHA string, applyErr error)` — journal helper |

### Modified functions

| File | Function | Change |
|---|---|---|
| `internal/ipc/server.go` | `handleDiffAction` accept branch | Replace `checkBaseFresh` call (server.go:1785) with `checkAndRefreshBase`; guard `CapturePatchBaseline`+`ApplyDiff` with `if !refreshed` |
| `internal/ipc/autoland.go` | pre-spend gate (autoland.go:246-258) | On `head != base`: call `ProbeApplyClean`; clean → `UpdateDiffBaseSHA` + journal + continue; conflict/error → journal + `journalAutoLandBlocked("base_stale")` + return |

### Removed functions

| File | Function | Reason |
|---|---|---|
| `internal/ipc/server.go` | `checkBaseFresh` (server.go:1715-1727) | Fully replaced by `checkAndRefreshBase` |

### Preserved

- `errBaseStale` (server.go:1699) — sentinel kept, wrapped by `checkAndRefreshBase` on conflict
- `errBaseStale` `errors.Is` contract at autoland.go:382 — unchanged
- `base_stale` blocked reason string — unchanged (tests assert it, autoland_test.go:288)
- Nil/empty base grandfathered skip — preserved (fix-INT D4)
- `DiffConflict` status — stays reserved for fresh-base apply failures only

## Hard rules

1. **No new deps.** `ProbeApplyClean` uses only `git worktree add/remove` + `git apply --3way` — all in the git package.
2. **No new event types.** `refresh_attempted` is an `action` value on `EventReviewAction`.
3. **No new diff statuses.** `DiffPending` for refresh failures, `DiffAccepted` for successes. `DiffConflict` stays reserved for fresh-base apply failures.
4. **Mutation of main only under `acceptMu`.** Probe never touches main, never takes `acceptMu`.
5. **Non-destructive.** Refresh failure → rollback + stay pending. No suspension, no cancel, no terminal state.
6. **Journal-first.** `refresh_attempted` precedes the accept/blocked row. Post-apply journal/store failure → rollback + fail closed.
7. **`errBaseStale` sentinel preserved.** `checkAndRefreshBase` wraps `errBaseStale` on conflict. `errors.Is` at autoland.go:382 fires unchanged.
8. **No infinite loop.** At most one refresh attempt per gate encounter. If HEAD moves again after a clean refresh, the second `checkBaseFresh` refuses (no re-refresh).
9. **No git add/commit.** Touch: `internal/git/git.go` (+`ProbeApplyClean`), `internal/ipc/server.go` (accept branch, −`checkBaseFresh`, +`checkAndRefreshBase`+`journalRefreshAttempt`), `internal/ipc/autoland.go` (pre-spend gate), `internal/store/diffs.go` (+`UpdateDiffBaseSHA`); tests in `internal/git/git_test.go`, `internal/store/store_test.go`, `internal/ipc/autoland_test.go`.

## Test names

- `TestProbeApplyClean_CleanOnDrift` — disjoint-path drift → clean=true; main untouched
- `TestProbeApplyClean_ConflictOnOverlap` — overlapping drift → clean=false; temp worktree removed
- `TestProbeApplyClean_CleansUp` — no worktree leaks after either outcome
- `TestUpdateDiffBaseSHA` — round-trip; ErrNoRows on unknown id
- `TestAcceptStaleBaseRefreshClean` — stale + disjoint → accept succeeds; `refresh_attempted{clean}` then `accept`; BaseSHA updated
- `TestAcceptStaleBaseRefreshConflict` — stale + overlap → `errBaseStale`; diff stays `pending` (NOT `conflict`); `refresh_attempted{conflict}`; main rolled back
- `TestAcceptFreshBaseNoRefresh` — fresh base → no `refresh_attempted` row; normal accept
- `TestAutoLandPreSpendRefreshClean` — stale at entry → probe clean → proceeds; `refresh_attempted{clean,pre_spend_probe}`; panel runs; lands
- `TestAutoLandPreSpendRefreshConflict` — stale + overlap → `refresh_attempted{conflict}` + `base_stale`; panel never called; diff pending
- `TestAutoLandFinalRefreshClean` — fresh at pre-spend, drift after panel → final refresh clean → lands; `base_stale_at_land` NOT journaled
- `TestAutoLandFinalRefreshConflict` — drift after panel → `refresh_attempted{conflict}` + `base_stale_at_land` (with panel); diff pending

## Verification

```bash
go build ./... && go vet ./...
go test ./internal/git/ ./internal/store/ -count=1
go test ./internal/ipc/ -run 'Refresh|Probe|BaseStale|Stale|AutoLand|DiffAction' -count=1
go test ./internal/ipc/ -count=1  # full suite, no regressions
```

## Attestation gap (stated plainly)

A clean-refreshed land is `current_HEAD + diff` — merged bytes nobody reviewed. Verify/panel attested `base + diff`. Bounded by: clean-only (any conflict refuses), full journal trail, unchanged `patch_sha16`. Re-running verify on the merge-result worktree = the deferred "merge-preview verify" follow-up, explicitly out of scope here.
