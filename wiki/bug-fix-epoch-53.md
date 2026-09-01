# Odo dev session 2026-09-01 — diff #137 replay, auto-land hardening, UX-3b/c advisories, k8s settings fix

Four tasks across isolated worktrees; all work left staged-dirty, zero commits, per task contract. One run was killed mid-flight by a daemon restart and resumed from its staged worktree.

## Task 1 — Re-apply diff #137 (UX-4 tab diet / ledger fold), no edits

- Patch resolved from journal: `.odo/diffs/6a9688c9-4f2fee42cb69.diff`; `git apply --3way` applied all 16 files cleanly (+355/−333), rename detected (`LedgerPanel.tsx → ReviewReceipts.tsx`).
- Fast gates only (prior full-suite/flake evidence stood, patch not re-litigated): `tsc --noEmit` exit 0; the two previously-flaky vitest files 13/13; Playwright trio (ledger 5, context-panel-tabs 2, lru-park 2) 9/9 with zero retries after mandatory `:1420` cleanup.
- 16 files staged in worktree `6a969730`, HEAD `2abaada`, left dirty.

## Tasks 2/5 — Pipeline hardening (pitfalls #57 rename staging + #42 vitest flakes)

**Key decision — locate by ground truth, not the task brief.** The brief blamed "drain staging depends on the patch path list"; journal evidence for #138 showed the real failure point is **accept-time staging in `git.ApplyDiff`** (`internal/git/git.go`). Drain extraction (`ExtractDiff`) was already `git add -A` + `git diff --cached` and rename-aware.

- **Root cause (reproduced)**: `git apply --3way` records renames directly into the index, pre-staging the deletion, so the old path exists in neither worktree nor index; `StagePaths` over the remembered patch path list then fatals with `pathspec … did not match`.
- **Fix**: keep the patch-path-union scope (unscoped `git add -A` in the main checkout is P0-forbidden — would sweep unrelated user changes) but filter ghost paths, staging only what exists in index or worktree. Exported as `StageExistingPaths` (renamed from `stageAcceptPaths`).
- **Audit found a second same-class site**: the M20 already-landed branch (`server.go:3771`) still used bare remembered-path staging; wired to the same filter.
- **Self-refutation disclosed**: the hypothesized "M20 silently loses renames via skipCommit" gap was false (tracked pre-image deletions are visible to `git diff HEAD`). The two new `server_test.go` tests are kept as characterization pins, not red/green pins; docs rewritten to match.
- **Fix B**: `.odo-verify` vitest segment now runs `--maxWorkers=2` (CLI-only; `vitest.config.ts` untouched so dev/test stay fast; explicitly no `--retry` — that would mask real flakes). Confirmation run 462/462 green, isolated statusbar 23/23.
- Regression proof: `git_test.go` rename + delete rows reproduce #138's exact error against old code, green after the fix.
- Run killed by daemon restart mid-flight; resumed in worktree `6a96a726` from staged state without redo. Final: 5 files staged (+367/−7) — `git.go` (+53/−3), `server.go` (+9/−1), `server_test.go` (+91), `.odo-verify`, `git_test.go` (+208, prior agent, unchanged). Gates: gofmt/build/vet clean, `go test ./internal/git` ok (17.5s), `./internal/ipc -run 'AutoLand|Stage|Verify'` ok (35.2s), 2 new ipc tests 2/2.

## Task 3 — UX-3b/3c (odo advisory styling + memory backoff surface)

**Reconnaissance halved the scope**: the `odo` flag typing (`types.ts:295`), notify exclusion (`App.tsx:439`), and finished-flash exclusion (`switch_cache.terminalError`) were already landed in UX-2/UX-3a; only three consumers were missing.

- **UX-3b**: `agent_error` payload gains `odo?: boolean`. MessageBubble renders advisories amber (`.bubble-advisory`, "odo advisory" label + ⚠) instead of red. ChatSurface run-header `failed` ✗ excludes `odo:true`. Daemon side unchanged — event type not migrated, so history isn't orphaned (A2-6b "pick the smaller diff").
- **Declared deviation from the brief**: instead of an amber RunsPanel error row, the `deriveRuns` fold treats advisories as **non-terminal — no row, no run closure**, because a daemon advisory corresponds to no run's failure and a row would fabricate an error entry.
- **UX-3c**: new `gui/src/memory.ts` `deriveAutoBackoff` — per-layer latest-wins fold over journal events the GUI already holds (zero new IPC). Curator backoff → "auto-curate paused — next eligible HH:MM" (stale horizons hidden); distill reason whitelist (below_min_bytes/events → "idle"; backoff/backoff_suspended/hourly_cap → "paused"); transient/config/unknown reasons hidden. MemoryPanel dim `.mem-backoff` footer; `App.tsx` `memoryEventsRef` frozen-thread per RunsPanel precedent.
- Known boundaries (declared out of scope): a RunGroup holding only an advisory shows ⟳ (honest unknown); distill daily-cap pause (`cause=cap_suspended`) is deliberately not whitelisted — it already has the `autoDistillCapResume` chip.
- 12 files staged (+496/−16), HEAD `644ccb6`. Gates: tsc ×2 clean, focused vitest 76/76, full suite 484/484 (two consecutive rounds), new e2e spec 3/3 (retries=0, three rounds), regression 32/32, `:1420` released.

## Task 4 — k8s settings write-branch fix

- Defect: `ReadSettings` mapped `k8s_namespace`/`k8s_context`/`k8s_job_selector`, but `UpdateSettings`' `set()` chain had no branches for them — GUI writes silently dropped.
- Fix: three non-empty-write branches after the auto_apply block (charset validation stays fail-loud at read in the k8s_status handler); round-trip test `TestK8sPrefsRoundTrip`; stale "Read-only over IPC" comments corrected in `settings.go` and `gui/src/types.ts:711`; bridge audited — `lib.rs update_settings` forwards `Value` passthrough, no second leak. Other "read-only k8s snapshot" comments (jobs/pods, not settings keys) kept.
- 3 files staged (+65/−4); go build/vet/gofmt clean, focused tests 3/3, tsc clean.

## Process incidents & lessons

- **Bash `cwd` parameter dropped repeatedly** — commands silently ran in the main checkout or wrong worktree; "green gates in the wrong tree" twice. Mitigation: explicit `cd … &&` and pwd assertions before gate runs. One stray sed polluted a line of main's `server.go` (restored, verified via `go build` + empty `git status`); a sibling worktree polluted early was `git checkout`-restored before auto-retirement.
- **`cp -c` in the worktree skill is wrong for directories** — cloning `node_modules` requires `cp -Rc`; the bare form fails with "is a directory".
- **Nested-copy artifact**: re-cloning into an existing `node_modules` with `cp -R` created `node_modules/node_modules`, and the duplicate React caused 35 false vitest failures (`useMemo` invalid-hook). Deleting the nested copy fixed everything; unrelated to the worker cap or any diff.
- **Tail-only test output loses failure names** — write full output to a file when triaging suites.

## Open loops

- Landing status of the staged diffs is only partially evidenced: `auto_land_blocked` (reason `verify_failed`) fired during the k8s run's window and a later `accept` fired after pipeline hardening completed — neither is mapped to a specific diff in the visible journal; confirm which diffs (UX-3b/c, k8s settings, pipeline hardening) remain blocked or unlanded.
- k8s clear semantics: `UpdateSettings` writes only non-empty fields for all keys, so the GUI cannot clear a namespace/context/selector back to `""` via `update_settings` — clearing still requires hand-editing `prefs.md`. If GUI-off is wanted, a clear contract (sentinel value or boolean flag) needs a decision.
- Two attributed-but-unproven failure windows: 6/484 first-round full-suite failures in the UX-3b/c worktree (names never captured) and 4/462 on the main checkout during the node_modules-pollution window. Both plausibly machine-load flakes (pitfall #42 family); no further investigation was run.
- Worktree skill text still says `cp -c`; the correct command is `cp -Rc` — skill correction pending.