> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Deep Audit #2 — 10 Findings Fixed (post-diff-#50)

## Session context

- Confirmed diff **#50** auto-accept was a compliant auto-land healthy path (verify gate → MoA panel round-1 unanimous accept → `risk_class=["none"]`, no protected files; SHA binding `52bcad61a3b0d8fb` matched commit `e457227`). No bypass, no manual unlock needed.
- User then delivered a second deep audit (seq 13682): previous round's fixes verified intact, but **1 P0 + 4 P1 + 5 P2** newly confirmed. All 10 were fixed in this session.

## Fixes applied

| # | Severity | Fix | Location |
|---|---|---|---|
| 1 | P0 | Skill dir chain resolved through `guardedBase`; symlinked `.odo`/`.odo/skills` degrades whole project skill scope to absent. `handleDeleteSkill` gained `guardProjectWritePath` (update had it, delete did not). Global scope unchanged | `internal/ipc/skills.go`, `server.go` |
| 2 | P1 | New `memMu` single-writer leaf lock (does not touch `s.mu`/`acceptMu`); `applyResolvedBatch` re-folds journal under lock for pending/consumed recheck — closes double-consumption and cross-workstream last-rename-wins for both memory batches and pins | `server.go`, `pins.go` |
| 3 | P1 | `AGENTS.md` now carries only the stable protocol (Project Rules + odo-todo contract); dynamic memory/pins copy blocks removed so the model sees exactly one receipted copy | `server.go`, `replay_test.go` retargeted |
| 4 | P1 | `handleDeleteWorkstream` checks daemon in-memory active state (`run`/`distill`/`distillKind`/`slash`/`panel`/`loop`/`autoPending`), then journal-derived loop activity, before SQL delete — blocks hidden diffs on soft-deleted workstreams | `server.go` |
| 5 | P1 | `readLoopTaskFile`: `os.Stat` size pre-check + `readWithinDir` anchored at `resolvedRoot` — closes symlink escape and read-then-limit memory pressure | `loop.go` |
| 6 | P2 | Successful poll only clears `poll failed:`-prefixed errors; switch/send errors persist until `ERROR_BANNER_MS` (10s) timer — fixes the Playwright flake class where switch failures were silently rolled back | `gui/src/App.tsx` |
| 7 | P2 | `migrateV4`: dedupes fossil duplicate workstream names (new id keeps name, old gets `-dup-<id>`) + partial unique index on `status='active'`; `CreateOrGet` re-reads winner row on constraint conflict | `store.go`, `workstreams.go` |
| 8 | P2 | New `loopArtifactBody` helper: guarded containment + `<field>_sha16` comparison; tampered/escaped/symlinked `findings`/`design_lock` artifacts fail closed at both read sites | `loop_journal.go`, `loop_run.go`, `loop_ctl.go` |
| 9 | P2 | `panelProg` is now a per-consult batch-group slice; defer removes only its own batch, poll snapshot merges — concurrent panels no longer show `Done > Total` or mixed legs | `server.go` |
| 10 | P2 | Skill scan enforces 64KB per-file limit (oversized file skipped entirely); injection `break → continue` so a large skill no longer blocks smaller relevant ones | `skills.go` |

## Decisions and boundaries

- **P1-1 crash window kept as-is** (file write before journal write): `planMemoryApply`'s `normalizeRule` dedupe makes replay idempotent, so the window is benign; the real gap was the missing concurrent single-writer, which was fixed.
- **P1-3 residual**: sub-second race remains between the active-state guard check and the SQL delete in `handleDeleteWorkstream` (peer could `send` inside the window). Classified as user self-inflicted; store's pending-diff check remains the backstop. Full atomicity would require moving deletion into the run state machine — out of scope.
- **Migration ordering**: v4 index deliberately excluded from schemaV1 unconditional DDL because legacy DBs may carry duplicate names that need dedupe first; fresh DBs now record `version=4`, `TestOpenMigrates` updated accordingly.

## Verification

- `go test ./...` — all green (ipc 544s; two stale v3 store assertions updated to v4)
- `go test -race` (new concurrency groups + store/ipc race groups) — green
- **12 new test pins**, one per audit finding (incl. concurrent panel, double-consumption race, spill tampering) — all pass
- GUI vitest 166/166; `tsc --noEmit` clean
- Playwright `switch-cache.spec.ts` (asserts switch-failed banner visibility, the P2-1 flake point) — 2/2

## Open loops

- User review pending; worktree (`10 fixes + 12 new pins + 3 retargeted pins`) is ready to be packed as **diff #51** once approved.
- P1-3 residual race window between guard check and SQL delete in `handleDeleteWorkstream` (sub-second, user self-inflicted; full fix requires deletion in the run state machine).
- GUI main JS chunk still ~625KB — lazy split for cold-start remains unscheduled (from the earlier audit round's note).
- P1-1 crash window (model may see new memory while journal still shows pending) deferred by design; replay-idempotent dedupe makes it benign.