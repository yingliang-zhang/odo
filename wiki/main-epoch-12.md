# Epoch 11 session: #28 rebuild assessment, `.odo-verify` discoverability fix (verify advisory)

## Decisions

- **Rebuild confirmed necessary; all three deployed layers stale relative to accepted work.**
  | Component | Built | Contains | Missing |
  |---|---|---|---|
  | `~/Projects/odo/odo` (daemon, odo) | Aug 20 23:51 | #27 `5ec522b` | #28 |
  | `~/.odo/bin/odo` (daemon, hermes-agent) | Aug 20 23:51 | #27 `5ec522b` | #28 |
  | `/Applications/Odo.app` (GUI bundle) | Aug 20 12:09 | pre-#26 | #26/#27 GUI + #28 |
  Accept commit `397ca3c` (21:39) post-dates daemon binaries by ~22h; #28's core (`memory_autogate.go`, `wiki_commit.go`, rewritten `skills_gate.go`, distillCore auto-apply) is entirely daemon Go-side, so without rebuild auto-gate exists only as source.
- **Two auto-review paths have different `.odo-verify` prerequisites** (answering "only projects with `.odo-verify` get auto review, but it's never auto-created"):
  - *diff auto-land (M16/M20)*: hard dependency, fail-closed (autoland.go:810) — no file ⇒ `auto_land_blocked{verify_unconfigured}`, panel never runs.
  - *memory/wiki auto-gate (#28, distillCore)*: zero dependency — `len(models)>0` guards (server.go:3695/3713/3882); gate inert without review config, batch stays pending (per skills_gate.go header).
- **No auto-creation of `.odo-verify` is by design, not a bug.** Production code never writes it (14 `os.WriteFile` hits are all test fixtures); odo's own was hand-written in M16 and landed via human-accepted diff #11 (`d709366`). `verifyCommands` are read from the run worktree (autoland.go:866), which only carries tracked files, so auto-create would mean "write + commit" — and a daemon committing its own verify oracle is exactly what the supply-chain gate must block (m16 gate 6, fail-closed by design).
- **Real gap is discoverability**: blocked diffs sit at `verify_unconfigured` with no user-visible signal. Chose **Option A (bootstrap detection + advisory)** over B (daemon-generated scaffold diff through supply-chain block — extra generation path) and C (relax fail-closed — dismantles gate design; rejected).
- **Rebuild deferred to one trip**: land the advisory diff first, then rebuild all three layers once, avoiding a second rebuild.

## Code changes (worktree, pending auto-land)

| File | Change |
|---|---|
| `internal/ipc/verify_advisory.go` | New: `adviseVerifyUnconfigured` + toolchain probing (`go.mod`/`package.json`) + checklist copy (evidence contract PASS/ok/N-passed, commit requirement, supply-chain rationale) |
| `internal/ipc/verify_advisory_test.go` | New: 5 tests — once-per-project debounce, configured suppression, reclaimed-worktree suppression, toolchain hint, configured determination |
| `internal/ipc/autoland.go` | Hook at verify-gate failure: advisory emitted when `reason=="verify_unconfigured"` |
| `internal/ipc/server.go` | Server gains `verifyAdvised sync.Map` (one advisory per project per daemon lifetime) |

Design points:
- Hook placed at the block point, not project registration: registration has no conversation to write to, and existing projects registered long ago; the block point is the pain and covers both new and existing projects.
- Two suppression paths: `worktreePath==""` (recoverPendingDiffs reclaimed semantics — fix is re-run, not a file) and main checkout already having a usable `.odo-verify` (rare worktree-before-commit race).
- `/loop` Mode A shares `runVerifyGate` but uses the round-fact channel (V6) — explicitly a non-goal, noted in file header.

## Verification

- `go build` + `go vet` clean; 7 targeted tests pass; **full `go test ./...` 455s, all ok, 0 FAIL** — same command the landing verify gate will run (diff touches no `gui/**`, so only the fallback line triggers).
- Premise-correcting discovery: hermes-agent **already has** a tuned `.odo-verify` (added `088a987acd`, refined `552b8f434f`; web/ui-tui/apps scope lines + Python fallback) — its auto review was never broken.

## Landing & rebuild plan

- Advisory diff lands via auto-land at run settle: drainRun extracts diff → gate-source files (autoland/server.go) get risk annotation per doctrine → panel 3/3 consensus lands; disagreement leaves diff pending with GUI-visible reasons.
- Rebuild checklist (post-landing, single trip):
  ```
  cd ~/Projects/odo && go build -o odo.new . && mv odo.new odo   # replace before kill
  cp odo ~/.odo/bin/odo && shasum -a 256 odo ~/.odo/bin/odo      # shas must match
  kill 85528 85939                                                # ensure_daemon_running restarts
  strings odo | grep -c 'auto-land is blocked for this project'   # expect 1
  cd gui && npm run tauri:build && ditto src-tauri/target/release/bundle/macos/Odo.app /Applications/Odo.app
  ```
- Risk: this session's omp parent is bash, not the daemon — daemon/app restarts do not kill this run's process tree (the UI-epoch-11 run-death was a GUI-spawned registration interruption; not applicable). App restart only interrupts GUI use. hermes-agent needs zero action.

## Open loops

- Advisory diff (verify_advisory) landing — awaiting auto-land panel consensus when this run settles; if the panel disagrees, diff stays pending in GUI.
- Three-layer rebuild (2 daemons + `/Applications/Odo.app`) — blocked on the advisory diff landing; user to give the word after landing.