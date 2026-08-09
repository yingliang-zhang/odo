# Conversation Summary — Odo Session (2026-08-09)

## Key Decisions

| # | Decision | Outcome |
|---|---|---|
| 1 | Branch topology explained — `odo/main` is Odo's internal work branch (M11c: workstreams store bare `main`, git consumers prefix `odo/`); run worktrees check out `odo/main`, accept lands on `main`, then `AdvanceBranch` fast-forwards `odo/main` | Resolved |
| 2 | No merge needed — `odo/main` fully contained in `main` (0 ahead, 7 behind); `memory/log.md` stale (logged HEAD `6ecbac0` vs actual `ac8bed8`+) | Resolved |
| 3 | Executed Op1 (GitHub rename `odo`→`odo-agent`) + Op3 (artifact cleanup) from `rename-install-cleanup-brief.md`; Op2 (install to /Applications) deferred | ✅ then reverted (see #6) |
| 4 | During rename: keep tauri identifier `com.yingliangzhang.odo` (app identity ≠ repo name; changing resets macOS perms); do NOT rename local dir `~/Projects/odo` (live worktrees/daemon paths); leave historical docs/logs untouched | Stands |
| 5 | Defer `journal.sqlite` reset — live daemon holds SQLite WAL; requires full Odo quit first | Open (manual) |
| 6 | **Full rollback of the rename** — user had previously abandoned `odo-agent`; revert `cb7bde4` in full | ✅ Done |
| 7 | Rollback discipline: `git revert` (never history rewrite on pushed commits), append-only `memory/log.md`, stamp SUPERSEDED/ROLLED BACK on affected wiki notes | ✅ Applied |
| 8 | "No output" root cause — **not data loss**: auto-distill (60s idle + `auto_curate_after_distill`) completed → `ChatSurface.tsx` epoch filter (`seq > lastDistillSeq`) folded the rollback Q&A from view | Diagnosed; fix option unselected |
| 9 | `/panel` failure root cause — `defaultMaxTok = 4096` in `internal/moa/client.go`; thinking models (kimi-k3, deepseek-v4-flash) burn output budget on reasoning traces → `stop_reason=max_tokens` | ✅ Fixed → 16384 |
| 10 | Grounded re-panel: models have no tool/file access → paste Odo/Hermes code facts into the prompt (previous glm-5.2 answer was generic precisely because it lacked grounding) | ✅ Applied |

## Code Changes

| Commit / Diff | Content | State |
|---|---|---|
| `cb7bde4` | `go.mod` module path + 15 Go files rewritten to `…/odo-agent` (16 files, 26 lines); remotes/gates green | **Reverted** |
| `80bd148` | Op3 cleanup logged (6 stale session+prompt pairs, `gui/dist`, `gui/test-results` deleted; `daemon.log` truncated) | Kept |
| `753d553` | `git revert --no-edit cb7bde4` — module path back to `…/odo`; `go build/vet/test` green (ipc 122.7s) | Pushed |
| `a39825a` | Rollback entry appended to `memory/log.md`; pushed `80bd148..a39825a` | Pushed |
| `internal/moa/client.go` | `defaultMaxTok` 4096→16384 (+ comment on thinking-model budget) | In worktree; accepted (diff_id 1) |
| 15 test files edits | Add `t.Setenv("HOME", t.TempDir())` to run-loop tests missing it (`server_test.go` ×8, `concurrent_test.go` ×6, `streaming_test.go` ×1) | **In progress at cutoff** |

**Incident (self-caused, repaired):** first bulk `sed` for the rename traversed `.odo/worktrees/*`, rewriting all 7 worktree checkouts; the reverse-pass then corrupted line 47 of the brief `.md` in each. All restored; every worktree verified `git status` clean. Distilled into skills: `scoped-bulk-text-replacement`, `rollback-pushed-change`, `reset-odo-journal-safely`, `diagnose-folded-epoch`.

**Verification highlights:** 16384 fan-out returned `end_turn` for all 3 models with 7325/8076/8550 output tokens — all >4096, proving the old cap was structurally insufficient for thinking models. `TestVisibleLoopAcceptRejectRestore` failure root-caused as a *pre-existing* HOME-isolation bug (`readUserMemory()` injected the real `~/.odo/user.md` into the stub prompt, breaking the `hello.txt == msg1` assertion); passes with `HOME=/tmp/odo-emptyhome` — unrelated to the moa change.

## Open Questions

1. **Test isolation edits in flight** — 15 tests identified and insertion points mapped; `TestSteering`/`TestCancelRun` use `t.Run` subtests needing per-subtest `Setenv`. Full `go test ./internal/ipc/` re-run pending.
2. **Epoch-fold UX fix choice** — A (`auto_distill: never`) / B (empty-state distinguishes "new" vs "folded to wiki" + click-through) / C (distill toast notes the fold). Taken to the grounded `/panel`; per-model answers captured in `/tmp/odo-panel/resp_*_16384.json` but not yet consolidated/presented.
3. **`journal.sqlite` reset** — still pending manual step after full Odo quit: `cd ~/Projects/odo && rm .odo/journal.sqlite*` (bootstrap recreates).
4. **M7 GUI webview E2E (cua-driver)** — outstanding since M7 closed.
5. **Dead code** — `steering.txt` write path in `omp.go` (Adapter interface, per A2 brief RC8).
6. **`odo/main` branch drift** — 7+ commits behind `main`; Odo's accept flow (`AdvanceBranch`) will fast-forward it; left untouched deliberately.
7. **Op2 status ambiguity** — `/Applications/Odo.app` appeared (built 18:01, launched 18:04) though Op2 was formally deferred; origin of the build unconfirmed in-transcript.
8. **Recurrence risk** — auto-distill folded the chat twice in one day; the first fold went unnoticed only because the user sent a new message 4s later, confirming the "silent fold" failure mode is systematic, not incidental.