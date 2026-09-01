> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# D9-W6 — `odo learning` promote --global, drop/apply, and stall closeout

## Context

Final wave (W6) of the D9 Learning Control Plane design lock in the **odo** repo (`/Users/yingliangzhang/Projects/odo`). Spec sources: `docs/design/d9-learning-control-plane-lock.md` (read first) and K3 long-form `docs/design/learning-control-plane-d9.md` §7. Prior waves already on main: W3 (`bafb61d`), W4 (restored `63bdaa1`), W4.5 (`acf96f9`), W5 rollback/freeze/stall/never-score (`43e893d`), D9-C grounded-round cap (`3c87b63`, tool-loop cap 40).

All four scope items were implemented, tested, and gated green. Work performed in the canonical run worktree (sibling worktrees untouched, per pitfall #36); worktree intentionally left dirty per task constraints.

## Key decisions

- **`promote --global` is human-initiated actuation, zero file I/O.** `ipc.LearningPromoteGlobal` behind `odo learning promote --global <hash|prefix>` via a daemon `learning_action` IPC. Stage must already be `project_active`. Journaling is marker-first: `review_action{action:"learning_promote", scope:"global"}` carrying the same measure evidence tuple (canary/live/baseline/rules/excluded + `harmful_absent:true`, same never-score-own-changes machinery) → `learning_stage{to:"global_active", actor:"human"}`. **user.md is never written** (D4 ④); the CLI result returns the rule lines for the human to add manually. Harmful-now candidates are rejected with no journal rows at all.
- **`drop` stays candidate-layer-only.** `learning_drop` marker + stage `dropped_by_human`; terminal stages rejected; `project_active` drop allowed but memory.md bytes untouched (`landed:true`), with CLI pointing to `odo rules retract` for landed lines. Voluntary drops never feed freeze (voluntary ≠ harmful evidence).
- **`apply` is the sole entry for the `held_for_human` state**, fully mirroring the existing `learningPromoteApply` conventions: `memory_apply{actor:"human", epoch:-1, recovery}` → stage transition → archive write first → apply/rotate/retract receipts; already-present converges idempotently; canary/terminal rejected.
- **`learning_stall` is advisory-only, pinned by tests.** Status fold gains `stalls` plus a per-candidate `stalled` flag; LearningPanel renders a `.learning-stall-row` inside the existing Candidates stage feed (no new panels); `odo learning list [--stalled] [--json]` surfaces them. Tests assert the fold leaves journal event counts and stage states untouched.
- **Single actuation path daemon↔CLI.** Shared free-function cores; the Daemon handler only adds `memMu` + advisory wiring. Extracted one store-keyed twin each for `learningStageOf` / `gatherLearningReplayInput` rather than introducing a second convention.
- **Distill whitelist audit: zero edits needed.** `unownedFoldGrowth` already attributes by the `learning_` prefix (pinned at `learning_episode_test.go:247-250`), so `learning_drop`/`learning_promote` inherit attribution.
- **Docs: nothing to sync** — README has no `odo learning` coverage at all, so no documentation was updated.

## Code changes

- New: `internal/ipc/learning_actions.go`, `internal/ipc/learning_actions_test.go` (6 rig-level tests), `cmd_learning_test.go` (5 store-fixture tests).
- Modified: `internal/ipc/{protocol.go, learning_status.go, server.go, learning_stages.go, learning_replay.go}`, `cmd_learning.go`.
- GUI (additive): `gui/src/types.ts`, `gui/src/components/LearningPanel.tsx` + `LearningPanel.test.tsx`, `gui/src/dev/mock-invoke.ts`.
- Mid-work fixes: CLI test fixture `cliCandidate` appended a copy and discarded the returned row (artifact hash never computed) — fixed by computing via the exported hash fn in the builder; removed a dead Map/Set lookup in the panel render (stall rows render as one block after candidate rows).

## Verification (gate tails)

- Focus: `go test ./internal/ipc/ -run 'Learning'` → **ok 13.048s**; new daemon tests 6/6 PASS; CLI tests 5/5 PASS (all first-run green).
- `gofmt -l` clean; `go build ./...` and `go vet . ./internal/ipc/` exit 0.
- Full suite: `go test ./... -timeout=20m -count=1` → **EXIT 0**, 7 packages ok, 0 FAIL (`internal/ipc` at 582s, within the ~507s baseline family); log at `/tmp/d9w6-full-suite.log`.
- GUI: `npx vitest run src/components/LearningPanel.test.tsx` → **7/7 PASS**; `tsc --noEmit` clean. Worktree lacked vitest, so `gui/node_modules` was symlinked to the main checkout's install (same convention as the verify gate).

## Accepted risk

CLI `apply` writes memory.md directly without holding `memMu` while the daemon is alive. Same window the rules-retract write-CLI already accepts: WAL busy_timeout serializes journal access, `writeFileWithin` is atomic, and the boot replayer repairs crash-torn writes. Not a new risk class.

## Open loops

- The canonical worktree is left dirty by design — committing/landing the D9-W6 changes is still pending the user.
- The auto-land panel initially returned `needs_fixes` citing `panel_infra`; the block was later superseded and the review accepted — confirm the panel-infra check stays green when the actual landing runs.
- README has zero `odo learning` coverage; decide whether the now-complete CLI surface (`list --stalled`, `promote --global`, `drop`, `apply`) deserves documentation.