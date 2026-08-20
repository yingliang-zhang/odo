> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# M19 `/loop` implementation — session snapshot

Implementing M19 `/loop` in the odo repo per `docs/milestones/m19-loop.md` and `docs/design/loop-design-lock.md`. The lock is the contract: C1–C12 base + V1–V13 verdicts, TDD against ~26 listed Go tests, mechanical extractions first with existing tests as regression witnesses, slash autocomplete (V13) and Tauri notification (V11) in scope.

## Key decisions

- **Mode A precedent**: `settle.go` auto-revise ladder is the architectural template for the loop drivers.
- **V12 model override lever**: `NewOMPForKey(stateDir, "loop_implementer")` plus a coding-model fallback in the adapter package; loop accepts run under a distinct adapter key.
- **Autonomy exclusion**: explicit `loopActor` filter so loop-generated accepts never count as human accepts in autonomy metrics.
- **Loop id sourcing**: derived from the started journal row's seq, not from the payload.
- **Tick/liveness protocol**: single owner map with explicit handoff to driver goroutines — replaced a first draft where the audit goroutine released ownership after ticking (would stall).
- **Tick sequencing in drainRun**: tick must fire *after* the ladder's `maybeAutoLand` completes, not beside it, on both diff-bearing and no-diff paths.
- **Visibility contract**: replay skip for loop kinds, distill tombstone render, `originGoal` skip in fold/render — multi-KB loop payloads must not leak into fold/render paths.
- **`launchAuditRound`**: takes a proper `conversationID` param; garbage guards removed.
- **Settings**: `loop_notify_on_complete` pref exposed read-only for the GUI (V11 base).

## Code changes

1. **Mechanical extractions (witness-guarded)**
   - `runVerifyGate` extracted from autoland.go verify/land path — byte-identical behavior.
   - `runDesignMoa` extracted from `handleDesignMoa`.
   - Full suite green after both (26.7s).
2. **store package**: `DiffRange` helper added; `CurrentSHA` completed (an edit truncated it mid-function; repaired and verified).
3. **adapter package**: OMP model override for key `loop_implementer` with coding fallback.
4. **`loop_journal.go` (new)**: kinds, marker, prefs, spill, fold; `fixOutcome` lifecycle added after engine referenced it.
5. **`loop.go` (new)**: `/loop` route, tick state machine, Mode A and Mode B drivers, recovery.
6. **`loop_ctl.go` (new)**: status/stop/resume + `CmdLoopCtl`; tick/liveness rewrite (see decisions).
7. **`server.go`**: `/loop` route, interleave hook, drainRun diff/no-diff branches with post-ladder tick.
8. **`protocol.go`**: `CmdLoopCtl`, new `Request` fields, dispatch wiring.
9. **Visibility edits**: replay skip, distill render, `originGoal` skip, autonomy `loopActor` exclusion.
10. **Settings/GUI body**: `loop_notify_on_complete` pref.
11. **`loop_test.go` (started, ~29.5KB)**: rig cloned from settle test patterns (stub mechanics, wait/poll helpers, prefs bootstrap) + pure-fold/parse table pins. Incomplete.

Build is green (`go build` clean after fixing a `kind` shadow and an unused `events` var; two leftover garbage blocks self-reviewed and removed).

## Verification status

- Baseline suite green before changes.
- Suite green after extractions.
- Compile green after full wiring.
- M19 test suite **not yet run**; test file partially written at snapshot.

## Open loops

- Finish `loop_test.go` (~28 pins planned; only rig + fold/parse tables written) and run the M19 suite plus full regression.
- V13 slash autocomplete upgrade: GUI surface mapped by scout, implementation not yet done in this snapshot.
- V11 Tauri notification: pref exposed read-only; GUI-side notification code not yet confirmed implemented.
- `auto_land` blocked by auto_panel (`protected_path`) — needs human review/land decision.