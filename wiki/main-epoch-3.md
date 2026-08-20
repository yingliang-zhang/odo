> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# /loop Fix Round: Four P1s + Seven P2s (tri-model review follow-up)

## Context

- M19 `/loop` implementation never landed on main; code lived at `/private/tmp/odo-m19-review` (R-W4 scenario). Imported into the worktree via `git add -N` + patch against baseline `a97bd3d` (direct parent of `a87cdd0`); 19 files byte-verified identical.
- Scope: four verified P1 bugs from tri-model review + manual reproduction, then P2 items (a)–(g). All changes touch `internal/ipc/`.

## Key decisions & code changes

### P1 fixes

| # | Bug | Fix |
|---|---|---|
| P1-1 | `/loop` routed but missing from audit mirror lists | Added `"/loop"` to `auditSlashCommands` (`cmd_recall_audit.go:61`) and `rulesAuditSlashCommands` (`rules_audit.go:215`); synced server.go mirror comment |
| P1-2 (V12) | `startLoopRunLocked` never set `runMeta.adapter` → orphan `NewOMPModelOverride` while `adapterFor("")` resolved the default → `Events` returned "unknown run" forever | New `adaptersMu sync.RWMutex` guards the adapters registry; all direct map reads migrated to `adapterFor`/`adapterNamed`; `loopRunAdapterLocked` registers the override once under stable key `"loop"`; `runMeta.adapter: adName`. Mutex required: two sessions' concurrent first registration is a real race |
| P1-3 (C12) | `loopRowSpend` asserted `payload["legs"].([]interface{})`; journaled concrete slices (`[]auditLegResult`/`[]DesignProposal`) never satisfy it → leg `output_tokens` never accumulated | JSON round-trip the row before spend extraction so extraction matches wire encoding |
| P1-4 (V8) | Human send during a loop fix run hit "agent already running" (`server.go:758`) before `suspendLoopOnHumanSendLocked` — refused and swallowed | At the byConv refusal: if `meta.loopID != 0`, pass the concurrency check, cancel the loop run via new `cancelLoopRunLocked` (critical-section core split out of `cancelLoopRun`), let the send proceed → journal → suspend → new run starts normally |

**Necessary reinforcement beyond instructions (P1-4):** a canceled run's drain would clobber the `human_interleave` suspend row with `run_tainted`/`fix_no_diff`, and on a stopped loop would flip terminal back to suspended. Added `loopDrainActive` fold guard shared by `loopPipelineAfterRun`/`loopNoDiffAfterRun`, making the pre-existing "fold's stopped state makes pipeline a no-op" comment actually true.

### P2 fixes

- **(a)** Tests pin the four security gates: `risk:protected_path`, `risk:supply_chain` (suspend + zero accepts), stall (same fingerprint across landed fixes), budget overflow + `resume budget=` recovery, restart_mid_run recovery.
- **(b)** `loopStartAudit` preflight-refuses `auto_apply != "main"` — parity with Mode B.
- **(c)** C10 admission fold moved inside `loopStartAudit`'s mu critical section (TOCTOU closed structurally).
- **(d)** `loopAdjudicateTask` returns idempotently if a `loop_task_done` row already exists.
- **(e)** `loopOpts` deleted; new `loopLeadingFlags` parses only leading consecutive `k=v` tokens / `--k=v` at the head; body starts at first non-flag token. All `/loop tasks` source modes consume the stripped body. Design-doc command surface updated (trailing flags → leading).
- **(f)** `loopSeed` re-folds between diffs and honors pending stop/suspend (`tickWait`); re-validates once more after the last drive before launching round 1.
- **(g)** Mode B: adjudication scans `review_action{reject}`; a human reject of the pending diff inside the restart window records `loop_task_done{status:"skipped"}` (new constant; journal contract comment synced) instead of dead-ending in `restart_mid_run`. Priority: landed > revise > blocked > rejected > default suspend.

### Tests added (12)

`TestLoopRowSpendConcreteSlices`, `TestLoopImplementerAdapterPinned`, `TestLoopHumanSendSuspendsMidLoopRun`, `TestLoopDrainSkipsInactiveFold`, `TestLoopRiskGateSuspends` (two sub-cases), `TestLoopStallSuspends`, `TestLoopBudgetExceededResume` (also end-to-end pins P1-3: round row `spent_tokens == 120000`), `TestLoopRestartMidRunSuspends`, `TestLoopLeadingFlagsOnly`, `TestLoopTasksFlagsLeadNotTaskText`, `TestLoopAuditRequiresAutoApply`, `TestLoopAdjudicateSkipsRejectedDiff` (also pins P2-d idempotency).

## Verification

`go build ./...` ✓ · `go vet ./...` ✓ · `go test ./internal/ipc/ -count=1 -short -run Loop` ✓ (22 Loop tests, 12 new) · full repo `-short` suite ✓ · `-race -run Loop` ✓.

## Open loops

- Changes uncommitted in worktree; auto-land pipeline blocked them with reason `protected_path` (seq 977) — awaiting adjudication per R-W4 precedent.
- P2-c TOCTOU fix is structural (lock ordering) only; no deterministic concurrency test possible.
- Remaining P2-a coverage beyond the four load-bearing gate pins still unwritten (user marked it optional: "remaining coverage may follow").