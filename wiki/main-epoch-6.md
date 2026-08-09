# Conversation Summary

## Context
Odo dev project (Tauri 2 + Go daemon + React GUI; repo `github.com/yingliang-zhang/odo`). Work happened across a run worktree (branch `odo/main` @ `ac8bed8`) and the main checkout `~/Projects/odo` (branch `main`). Session covered: status Q&A → repo rename + cleanup → full rollback → "missing output" diagnosis → /panel thinking-model failure fix → inventory of outstanding work.

## Key Decisions

| # | Decision | Rationale / Evidence |
|---|---|---|
| 1 | **No merge needed** — `odo/main` fully contained in `main` (merge-base = `ac8bed8`; main strictly ahead) | Direction was reversed: worktree was the stale side |
| 2 | **`odo/main` explained** — Odo-internal work branch with literal `/` in name (M11c): workstreams store bare `main`, git consumers prefix `odo/`; runs check out worktrees on it; accept lands on `main`, then `AdvanceBranch` fast-forwards `odo/main` | `internal/ipc/server.go:453`, `internal/git/git.go:41` |
| 3 | **Op1+Op3 executed** from `docs/design/rename-install-cleanup-brief.md` ("先完成1，3"), Op2 (install .app) deferred | Brief had 3 numbered ops |
| 4 | **Tauri bundle identifier kept** `com.yingliangzhang.odo` across rename | Bundle ID = macOS app identity; changing resets permissions/stores for zero value |
| 5 | **Local dir `~/Projects/odo` NOT renamed** | 7 live worktrees + running daemon (PID 30215) reference the path |
| 6 | **Historical docs/logs never rewritten** — corrections are appended | `memory/log.md` is append-only |
| 7 | **Full rollback of the rename** after user said it was previously abandoned: `gh repo rename` back, `git revert --no-edit cb7bde4` → `753d553`, never history rewrite | Pushed `80bd148..a39825a`; gates re-run green (ipc 122.7s) |
| 8 | **SUPERSEDED stamping** — `wiki/main-epoch-1.md` got a ROLLED BACK banner so future recall can't resurrect stale state |
| 9 | **`journal.sqlite` reset deferred** — live daemon holds SQLite WAL; corruption risk. Manual: quit Odo, `rm .odo/journal.sqlite*` |
| 10 | **"Missing output" = auto-distill epoch fold, not data loss** — prefs `auto_distill: on_idle / 60s / auto_curate_after_distill: true`; distill at 18:25:32 (seq 278) made `ChatSurface.tsx:456-466` filter `seq > lastDistillSeq`, hiding the rollback Q&A. Folded twice that day; first masked by a message 4s later |
| 11 | **/panel root cause: `defaultMaxTok = 4096`** — thinking models (kimi-k3, deepseek-v4-flash) burn output budget on reasoning traces → `stop_reason=max_tokens`. Raised to **16384** |
| 12 | **`TestVisibleLoopAcceptRejectRestore` FAIL = pre-existing bug, not the fix** — test lacks `t.Setenv("HOME", t.TempDir())`; `readUserMemory()` injected real `~/.odo/user.md` into the stubbed prompt, breaking `hello.txt == msg1`. Verified: passes with isolated HOME |

## Code Changes

| Change | Files | Commit / State |
|---|---|---|
| Module path `odo`→`odo-agent` (go.mod + 15 Go files, 26 lines) | `go.mod`, `internal/**`, `main.go` | `cb7bde4` pushed → **reverted** by `753d553` |
| Log rename + cleanup; log rollback | `memory/log.md` | `80bd148`, `a39825a` pushed |
| **`defaultMaxTok` 4096→16384** (+ comment on thinking-model budget) | `internal/moa/client.go` | Accepted via GUI → `1de583c odo: accept diff #1` on `main`, **unpushed** |
| Cleanup: 6 stale session+prompt pairs deleted (live session kept), `gui/dist`, `gui/test-results` removed, `daemon.log` truncated | filesystem | done (wiki/ + ledger.md already absent) |
| SUPERSEDED/ROLLED BACK annotations | `wiki/main-epoch-1.md` | done |
| HOME-isolation fixes for 15 run-loop tests lacking `t.Setenv("HOME", …)` (`TestSteering`/`TestCancelRun` need per-subtest) | `internal/ipc/*_test.go` | identified at cutoff; edits reported landed in `7559f7d`/`ac8bed8` vicinity |

**Self-caused incident (repaired):** bulk sed traversed `.odo/worktrees/*`, rewriting all 7 worktree checkouts; reverse-substitution also corrupted line 47 of the brief `.md` in each. All restored; every worktree verified `git status` clean. Lesson distilled into memory/skill proposals (scoped replacement via `git ls-files`).

**Fix verification (live gateway):** grounded 3-model fan-out at 16384 — all `end_turn`; output tokens 7325/8076/8550 (all > 4096, proving the old cap fundamentally insufficient). Earlier 4096 repro showed non-deterministic truncation (2890 tokens that run).

## Open Questions / Pending

1. **Epoch-fold UX** — options unselected: A) `auto_distill: never`; B) empty-state distinguishes "new" vs "folded to wiki" + click-through; C) distill toast noting the fold.
2. **`journal.sqlite` reset** — still manual, needs Odo fully quit.
3. **`1de583c` push** — `main` is 1 ahead of origin; user to decide.
4. **Uncommitted wiki restructure** in main checkout (10 files: index rewritten to new slugs, old topics merged/renamed — curator-shaped) — commit or discard?
5. **/panel grounded consolidation** — the Hermes-comparison question (GUI / auto distill / auto curate / schema, `~/.hermes/hermes-agent/`) was re-run at 16384 with grounded context; answers captured in `/tmp/odo-panel/resp_*_16384.json` but **not yet presented/consolidated**. glm-5.2 needs file-pasted grounding (no tool access) for non-generic answers.
6. **`odo/main` drift** — 6+ commits behind `main`; deliberately left for Odo's accept flow to fast-forward.
7. **Earlier anomalies (uninvestigated)**: `agent_error` exit 127 (`omp: command not found`), one `agent_text` containing raw session JSON, 401 auth error.
8. **Distilled skills pending acceptance**: `scoped-bulk-replacement`, `rollback-pushed-change`, `diagnose-folded-epoch`, `reset-odo-journal-safely`, `rename-github-repo-and-go-module` (reviews mostly ACCEPT; two needed wording fixes — verification step contradicting commit-later ordering).