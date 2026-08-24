# Odo Session: Diff #41 Manual Landing and Workspace Cleanup

## Decision: Diff #41 recovery via manual land (Path A)

Diff #41 (three fixes to `autoland.go` gate code + tests, patch `6a8bd86f-1b244913280e.diff`, 3 files +62/-5) passed verify (~8.5 min, full-package tests green, patch_sha16 `bc777792683fee7f`) but the auto-land panel returned a split verdict (2 accept / 1 reject → `panel_mixed`), which the M20 charter treats as terminal auto-reject. `accept_diff` is hard-guarded at server.go:2745 (`Status != DiffPending`), so the human-accept channel was closed.

Code-level audit refuted all three glm rejection reasons:

1. **capDetail tail-bias flip missing callers** — invalid. All 9 callsites audited (autoland×3, loop_run×3, server×3, settle×1); all are journal-detail/review-reasoning truncations where the tail carries the critical content (`--- FAIL`, verdict). None depend on head semantics.
2. **Playwright scrub weakens the test** — factually wrong. autoland.go:1126 uses `os.Getenv(...) != ""`; setting the var empty is equivalent to unset.
3. **GOTELEMETRYDIR shared dir "moves the race"** — invalid. Go telemetry's default is a per-user machine-wide shared dir designed for multi-process concurrency; pinning it under `$TMPDIR/` restores that shape outside `t.TempDir`, eliminating the cleanup race rather than relocating it.

Options weighed: (A) manual `git apply` + commit + journal correction, precedent #34 (cherry-pick land + seq 8017/8470 correction lines); (B) new run re-emitting #42 through the pipeline — ~8.5 min verify plus another panel where glm could repeat the same factually-refuted reasons, zero information gain. Chose **A** (user-selected).

## Code changes

- **Landed cf09b9e** (`odo: accept diff #41`) on main: applied patch byte-identical to `.odo/diffs/6a8bd86f-1b244913280e.diff` (3 files, +62/-5).
- **Journal correction** written at conv 1 seq 10616: `review_action ledger_correction` with `corrects_seq: 10417`, documenting the three refuted reasons, the prior verify pass on identical bytes (`bc777792683fee7f`), and `landed_commits: ["cf09b9e"]`. Store entry for #41 deliberately left as `rejected` per convention (correction, not silent DB mutation).
- **Binaries redeployed** atomically (`mv`, permissions preserved) to both `<project>/odo` and `~/.odo/bin/odo`; `vcs.revision=cf09b9e`, clean tree.

## Verification

- A/B proof: with `PLAYWRIGHT_BROWSERS_PATH` injected, `TestPlaywrightBrowsersDir` went from deterministic FAIL to **green** after the fix.
- Also green: `TestCapDetailTailBias`, `TestAutoLandStartedRowsAbsentBeforeSpend`, `TestRunVerifyMountsGoToolchainCaches`, `TestReviewWithModelJournals` (tail contract); full `go build ./...` OK.
- Pipeline consequence: the deterministic verify blocker that killed #39/#40/#41 (and would burn ~8.5 min per future diff) is eliminated from main; verify is no longer doomed for the next diff.

## Disk cleanup

User asked why the dev directory was 9+ GB. `du` measured 6.6 GB (Finder's 9+ GB is a counting-method discrepancy); 98% was Rust build output:

| Path | Size |
|---|---|
| `gui/src-tauri/target/debug` | 5.2 GB |
| `gui/src-tauri/target/release` | 1.1 GB |
| `gui/node_modules` | 255 MB |
| `.odo`, `.git`, Go source + docs + wiki | ~108 MB |

Actions: deleted `target/` and `node_modules/` (workspaces gitignored; rebuildable via `npm --prefix gui ci` + `cargo build`) → project shrunk **6.6 GB → 128 MB**. Archived tracked source to `~/Desktop/odo-src-cf09b9e-20260824.zip` via `git archive HEAD` (469 files, baseline cf09b9e, clean tree; excludes all gitignored product). No Rust/npm artifacts existed in any worktree.

## Open loops

- hermes-agent daemon (PID 85939) still runs an old binary image from the shared `~/.odo/bin/odo` path; whether/when to restart it remains a pending user decision (main-epoch-23). Same repo code, per-project stores — no divergence urgency.
- Odo project's own daemon is not currently running; it will load cf09b9e code on next start (manual or Tauri-launched) — no action needed, deployment rule (replace binary before kill) already satisfied.
- CLI defect observed (unfixed): an old binary run inside a worktree path hangs during daemon bootstrap on `odo review list` / `odo help` after "refusing to register worktree path"; read-only journal commands work. Candidate future fix.
- Optional test hardening suggested by glm/deepseek (non-blocking): add a "tail content survives" integration assertion in review_test, and cover the `==""` fallback branch in `TestRunVerifyMountsGoToolchainCaches` in a later diff.