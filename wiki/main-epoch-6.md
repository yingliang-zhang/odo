> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Maintenance & UI Session — Decisions and Changes

## Scope

Session covered three workstreams: (1) executing a hygiene/lifecycle optimization plan (P0–P3) on the odo daemon and its memory/wiki stores, (2) diagnosing phantom-project and running-badge cross-talk bugs in the GUI, (3) UI polish on the composer area (alignment, spacing, floating style).

## Key decisions

**Plan premise corrections (scout-verified before coding).** Two premises of the original optimization plan were disproved against code:

| Claimed gap | Verified reality |
|---|---|
| Orchestrator leaves `*-brief.md` / `*-output.md` after consuming; needs deletion hook | Daemon never writes these files (in-memory `moa.Query` pipeline). The 105 dead files were legacy manual-flow artifacts — one-time deletion is the complete fix; no hook exists to add |
| Worktree reap only fires on no-diff | Already implemented: `handleDiffAction:2646` → `retireRunForDiff`, pinned by `TestVisibleLoopAcceptRejectRestore`. Boot sweeper converges crash-orphans |
| Sessions never cleaned | **Real gap**: `OMP.Start` creates `.odo/sessions/<runID>` + `.odo/prompts/<runID>.txt`, `Close` never deletes (source of 331 session dirs) |

**P1 design constraint discovered via regression**: prompt files must NOT be deleted at retire — `TestFalseStopRetryConsumesSteers` reads the original run's prompt as audit ground truth. Session dirs delete at retire; prompts converge via boot sweep only.

**P3 supersede mechanism**: curator declares a `superseded` field; daemon mechanically validates (name ∈ `notesRead` ∧ referenced by ≥1 written bullet) then atomically stamps a SUPERSEDED banner on the note. File stays on disk (citation liveness gate + `odo wiki read` intact); recall/recall_cross injection skips banner notes; curator input side deliberately still reads them (gen-2 topics rebuild from source notes each round — excluding would erase facts).

**Deployment conventions applied**: replace binaries before kill (`0ebc5dcb…` from HEAD `7b625c3`); use external `/bin/kill` (shell builtin refuses); `ensure_daemon_running` auto-respawns daemons from fresh binaries; GUI restart only needed when `gui/src` or `src-tauri` changed.

## Code changes (all landed or pending as diffs under review)

**Diff #16 (accepted)** — 10 files, 1332 lines:
- P0: runtime cleanup of 105 dead `*-brief.md` files + orphan worktree `6a85ca6b`.
- P1: `retireRun` deletes session dir; boot sweeper ages out orphan sessions/prompts; new sweeper aging test.
- P2: `memory/log.md` folded 818 → 64 lines (Phase 1/2 summarized, Phase 3 kept verbatim); done in run worktree after an accidental main-repo fold was rolled back.
- P3: curator `superseded` declaration + daemon double-validation + banner stamping + recall injection skip; tests for phantom/unreferenced declarations. Auto-land was blocked `protected_path` (touched `internal/ipc/ledger.go`, fail-closed); manually accepted.

**Registry/cross-talk fix (PENDING diff `6a86673a-84b998603453.diff`)** — 6 files:
- `gui/src/App.tsx`: `resetProjectAggregates` clears 6 project-level aggregate states on root switch + immediate re-fetch; mid-flight root check in `refreshPendingCounts`.
- `gui/src/dev/fixtures.ts` + `mock-invoke.ts`: per-root `countsByRoot` overrides.
- `gui/e2e/sidebar.spec.ts`: id-collision regression test (mutation-tested: fails without the App.tsx fix).
- `internal/ipc/registry.go`: `isLinkedGitWorktree` guard — `.git` file pointing at `/.git/worktrees/` → refuse auto-registration (submodule `.git/modules/` still allowed); unit tests including submodule pass.

**Diff #18 (accepted)** — composer UI, 5 files: container `flex flex-col gap-1.5`; removed SteerQueue `mx-4` (32px misalignment); added existing `shadow-soft` token to input form + both queue cards; stripped self-margins from AutoDistillChip/QueueDock/LoopChip/attachment chips/composer-hint. Browser-verified: all boxes `x=256/w=1008`, uniform 6px gaps, shadows rendered.

## Verified facts about incidents

- **Phantom tmp project**: `tmp.eUnKPPwpbm` (mktemp git repo) was auto-registered by `ensureProjectRegistered`; 5s `pending_counts` polling on dead socket respawned its daemon. Broken via sidebar remove-project (M11 F8 escape hatch) at 10:22:20; final stray daemon killed, no respawn.
- **`ui-message-stream`**: not a phantom — a real sibling git worktree that got registered; old guard only recognized `<project>/.odo/worktrees/*` shapes.
- **Running-badge cross-talk**: pure frontend bug; state keyed by workstream id (ids restart at 1 per journal) was not cleared on project switch → false "running" for up to ~6s (4 poll ticks). Daemon data verified correct.
- **Learner pipeline self-healed**: `.odo/memory.md` now landing on disk.
- Verification totals: e2e 113/113, vitest 97/97, `tsc --noEmit` clean, Go build/vet/tests green; `internal/ipc` full suite passed 3 consecutive rounds (~400s each) plus clean baseline.

## Open loops

- Accept pending diff `6a86673a-84b998603453.diff` (cross-talk fix + registry guard) — not yet in main; verify gate will re-run suites on accept.
- Rebuild and restart after that accept: GUI (`npm run build` + tauri build → `/Applications/Odo.app`, currently 8-19 23:45 build missing both UI diffs) and both Go binaries (`~/Projects/odo/odo`, `~/.odo/bin/odo`, currently 10:12 builds missing the registry guard), then restart app.
- Decide disposition of the `ui-message-stream` project: remove-project in sidebar if unwanted (stops the 5s polling keeping daemon 85177 alive); the registry guard prevents re-registration but does not clean existing rows, so the row must be removed manually once. Worktree directory and branch disposition is the user's call.
- Unexplained flake: first `go test ./internal/ipc/` round during #16 verification FAILED with truncated output (test name unrecoverable); three later rounds + baseline passed and the accept gate re-ran clean, but root cause was never identified.