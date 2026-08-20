# Odo runs #21–#24: #23 rejected, #24 accepted, #22 verified green (pending GUI accept)

## User decisions (GUI)
- **#23 rejected**, **#24 accepted** by user.
- **#22** (UI switch-cache workstream): verified as the next accept; **#21** superseded by #22, still pending reject.

## Key decisions
- Fold the two stale-test fixes into **#22's diff snapshot** rather than a separate hotfix: fixes are byte-identical to what main needs anyway; splitting only adds a GUI round-trip. Accepting #22 = feature + main-green in one step.
- Rebased #22 onto `9c7e52d` (post-#24 main), resolving the server.go conflict on #22's side naming (`AfterSeq` hint, `pollLocked`, `latestDiffInfo`).

## Findings
- **Main @ `9c7e52d` is red after accepting #24.** #24's landed diff omitted the two stale-test fixes listed in the epoch-9 note:
  - `TestGetSettings` (server_test.go legs at :2357/:2379) — M20 flipped `auto_apply` default `off`→`main`; tests still expected `off`.
  - `TestPhantomDiffVerdictBlocksAutoLand` (runverdict_test.go:314) — prefs missing the `review:` line; M20 arming gate never arms.
- Both fixes were added into #22's snapshot (`off`→`main` ×2, plus `review:` line) and the snapshot regenerated: `base_sha=9c7e52d`, 11 files, byte-identical to producing worktree staging (`6a86b8e2`).
- Mechanics confirmed en route: `worktree.Manager.ExtractDiff` writes the worktree-vs-HEAD snapshot to `.odo/diffs/<runID>.diff`; `UpdateDiffBaseSHA` is the post-rebase refresh path.

## Code changes
- #22 snapshot updated: two assertions flipped to expect `main` in `TestGetSettings`; `review:` prefs line added in `TestPhantomDiffVerdictBlocksAutoLand`; base_sha → `9c7e52d`.

## Verification (base `9c7e52d` + #22 snapshot = post-accept state)
- `go build ./... && go vet ./...` — clean.
- Full ipc Go suite — **ok, 494s, 0 FAIL** (prior run was cancelled by session end; this re-run closes the loop).
- Other Go packages (root/adapter/git/moa/modelspec/store) — all ok.
- vitest — 9 files / 109 cases green, incl. switch_cache (12).
- Snapshot `git apply --check` on `9c7e52d` — clean.

## Open loops
- **User GUI: accept #22** → main returns to green.
- **User GUI: reject #21** (superseded by #22; missed in the previous round).
- Optional Playwright e2e (`switch-cache.spec.ts`, needs real Tauri app env) — epoch-8 question whether to run it before landing #22 remains undecided.