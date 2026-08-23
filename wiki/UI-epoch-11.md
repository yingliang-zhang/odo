> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Session Summary — odo: #26 Verification, tsc Fix, Doctrine Rebuild (#27), Full Binary Redeploy

Context: odo project's GUI-epoch review pipeline. "Doctrine" = the auto-land zero-manual-lock redesign: agent-authored diffs to gate source files are no longer hard-blocked; they land via panel risk annotation plus a byte-bound evidence gate, with human accept only as legacy fallback.

## Decisions

- **#26 accepted and verified on main `ba6a5ab`**: pipeline skips `auto_revise_product` (`pipeline.ts:126`), field comments in `types.ts`, regression test in `pipeline.test.ts` using the real 21→22 chain shape. 15/15 file tests, 110/110 full suite.
- **Fixed main's red tsc (broken since #22)** in commit `63d698a`: `mock-invoke.ts:31` uses `Promise.withResolvers` but `tsconfig.json` lib was ES2020. Fix: lib → ES2024, adjacent executor code unified to `withResolvers` per project convention. Runtime floor (dev-mode-only file): Chrome 119+ / Safari 17.4+ / Linux WebKitGTK ≥ 2.44 — no risk on user's darwin 27.
- **Doctrine rebuilt from journal, not transcript**: transcript `sessions/6a8715dd-60842bf07b67/output.txt` was pruned; recovery replayed 24 write/edit payloads verbatim from the journal (UI session seq 3933–4204). Original run died at 14:13 when its own daemon restart interrupted diff registration.
- **Doctrine deployed to both daemons** (user-approved rebuild + atomic binary swap + restart): doctrine is now behaviorally live, not source-only. Gate-source diffs follow the new three-valued `autoLandCheck` + `panelVerdictAttestsDiff` path.
- **hermes-agent data untouched**: its accumulated diff lives in its own store (`~/.hermes/hermes-agent/.odo/journal.sqlite` + capture file), decoupled from binary swap and odo's daemon lifecycle — answering the user's question before approving the restart.

## Code changes

**#27 — doctrine, landed on main `5ec522b`** (7 files, +286/−97):

- `autoland.go` — `autoLandCheck` returns three values; gate source, new top-level dirs, and net-new assertions downgrade from hard block to panel risk annotation. Hard blocks remain only for memory and supply-chain paths.
- `server.go` — new execution-layer evidence gate `panelVerdictAttestsDiff`: `patch_sha16` binds the panel verdict to the exact landing bytes. Deleted `rejectProtectedPaths` / `rejectExecutorPaths`; added `rejectMemoryPaths`. Old symbols: zero residue repo-wide.
- `review.go` — panel facts block injects `riskNotes`.
- `autoland_test.go`, `m6_test.go`, `review_test.go` — table-driven updates, migration to `rejectMemoryPaths`, riskNotes assertions, new gate tests for `patch_sha16` byte binding and `.odo/` / wiki case-bypass protection.
- `docs/design/auto-land-zero-manual-lock.md` — doctrine workout chapter.

**Rebuild fidelity**: zero drift c166ea8→63d698a on those files (#26/63d698a touched only gui), all anchors unique-hit; 3 fuzzy anchors in `m6_test.go` (`/dev/null` vs `a/...` lines) corrected and applied as a net-effect cascade. prefs.md doctrine comment (#4139) survived in `~/.odo/`, not re-applied.

**Validation**: final-state full suite `go build && go vet && go test -count=1 ./...` → exit 0, 0 FAIL, 409s (original intermediate-state run: 426s). Post-landing re-verify: 429s, 0 FAIL. gofmt clean on all 6 touched files (`loop_audit`/`loop_journal` left as known historical debt).

## Deployment facts

- Binaries built from `5ec522b`, md5 `9371c0ce…`, swapped via atomic rename at both `~/Projects/odo/odo` and `~/.odo/bin/odo`.
- odo daemon: old PID 68480 dead; new PID 85528 auto-started 23:52:40, socket healthy, clean log (sweeper reclaimed 5 prompts, no pending-diff replay — #25 dedup in place).
- hermes-agent daemon: SIGTERM graceful exit took ~10s (slower than odo's typical, no errors); new PID 85939 up 23:55:02 on the doctrine binary; socket connect-probe passed. Boot: sweeper reclaimed 3 sessions + 5 prompts; recovery logged `1/1 pending diffs already adjudicated — skipping their re-fire`.
- One in-flight agent request was lost to the odo daemon restart (`daemon_restart` error at seq 4742); state was re-verified afterward, no corruption.
- Tooling caveat: bash `cwd` parameter was intermittently ignored mid-session, producing a false "22 files failed" reading (vitest scanned the worktree with no `node_modules`). Reported via `xd://report_issue`; explicit `cd` used for all subsequent main-repo commands.
- Main repo also contains an in-progress curator wiki migration (34 modified + 24 untracked) — not authored here; commits explicitly staged only this session's files.

## Open loops

- hermes-agent pending diff (store `id=1`, status `pending`, base `06168c8`; capture file and worktree `6a86ab36` intact): adjudicated and dedup-gated against re-fire — awaiting user accept/reject in the GUI.
- modernc.org/sqlite SIGBUS: unresolved. Recommended first attempt: `_pragma mmap_size=0` (one-line, zero dependency change); upgrade the dependency only if that fails.