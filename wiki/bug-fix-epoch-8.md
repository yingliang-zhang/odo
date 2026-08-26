> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Round 4: Conflict-Retirement & Sweep Fixes (Stranded Memory/Pins Replay, bug-fix-epoch-7)

## Context
REGENERATE round 4 of the boot-time stranded-intent recovery feature. Round-3 panel verdict: K3 accept + GLM accept + DSF needs_fixes; replay semantics and the merge path were already settled. This round: 3 functional fixes + 1 test + 1 comment-only fix, all in the conflict-retirement/sweep half. Base: diff #79 (`6a8e6ea4-4fb53c76b6c5.diff`) reapplied onto current main (21 files, +3183/−255).

## Decisions
- **Retirement authority (FIX 2):** conflict retirement fires only under project-wide fold context. Chose *unconditional skip* for lane-local evals over runtime project-wide-newest confirmation: the caller statically knows its fold context, confirmation would re-fold the full project journal per evaluated receipt for zero behavioral gain, and the boot pass owns retirement anyway.
- **Gate placement:** new `evalScope` (`evalLaneLocal`/`evalProjectWide`) threaded through `evalMemReceipt`; the gate lives *inside* `retireSupersededConflicts` (single point) so no future callsite can forget it.
- **Legacy receipts (FIX 3):** a legacy terminal-landed boundary is a landed attestation, so it retires older open conflicts on the layer under the same FIX 2 scope gate.
- **Archived-lane actionability (FIX 4):** no gate fix needed — verified `checkConversation`'s chain (`GetConversation` → `GetWorkstream` → `GetProject`) carries no status predicate; delete is a status flip, so surfaced archived-lane rows are already actionable.
- **Comment correction (FIX 5):** archived-lane fold guarantee is attributed to the data lifecycle (delete = status flip, rotation never retires conversations, joins carry no status predicate), not JOIN flavor — `WHERE w.project_id = ?` filters a right-side column, collapsing LEFT to INNER whenever a lane row exists. SQL untouched. Doctrine boundary kept: hard cascade-delete of conversations/workstreams, or whole-journal `rotate` moving the SQLite file, is unrecoverable by construction (destroyed journal row has no replay source).

## Code changes
- `internal/ipc/memory_autogate.go` (FIX 1): `sweepPendingBatch`'s `batch.consumed` early-return now takes `memMu`, re-reads the journal fresh (mirroring `handleApplyMemory`'s consumed branch), and runs `replayLaneMemReceipts(..., replayApply)` — a live failed-write marker repairs or journals at sweep time instead of the next apply/restart.
- `internal/ipc/memory_replay.go` (FIX 2): `evalScope` threaded through `evalMemReceipt`; `replayMemoryJournal` passes project-wide; `replayLaneMemReceipts` (and through it the apply-retry, pin RMW, and sweep paths) passes lane-local.
- `internal/ipc/memory_replay.go` (FIX 3): `r.legacy` branch calls `retireSupersededConflicts(ctx, r, scope)` before returning `replayNone`.
- `internal/store/events.go` (FIX 5): both LEFT JOIN comments rewritten plainly; SQL unchanged.
- `internal/ipc/memory_replay_test.go`: 4 new drills —
  - `TestSweepConsumedBatchRepairs`: consumed marker + disk at before → sweep restores memory.md, one `recover` row, no second `memory_apply` marker, no `memory_gate`; second sweep no-op.
  - `TestReplayLaneLocalSkipsRetirement`: lane A (older) landed + lane B (newer) conflict open → lane-local pass leaves count 1, zero `heal_resolved`; newer landed + boot retires B.
  - `TestMemoryReplayLegacyRetiresConflict`: open pins conflict + legacy receipt newest → retirement fires, count 0, projection untouched, third boot journals nothing.
  - `TestResolveHealConflictArchivedLaneActionable`: archived lane strands pins + skill conflicts; Resolve (body restored, 2→1) and Dismiss (file untouched, 1→0) both succeed via IPC with `actor:"human"` and archived lane key.

## Verification
| Gate | Result |
|---|---|
| `go build ./...` / `go vet ./internal/...` | green |
| `gofmt -l internal/` | empty |
| `go test` full repo | all `ok` (ipc 555s incl. 24 replay drills, 4 new); legacy-boundary drill stays green |
| `tsc --noEmit` (gui) | clean |
| playwright full gate | 123 passed (5.7m), incl. `memory-stranded.spec.ts` |

Operational incidents: stale Vite from round-3 worktree `6a8e6ea4` (started 13:09) held `:1420` — same sabotage class recorded in round 3; killed before the gate. Root-cwd `npx playwright test` resolves vitest; gate ran from `gui/`. Exclusions honored: no landWG/auto-land run-lifecycle changes; no docs/panel-evidence/attestation files; completion note in agent text only.

## Open loops
- Round-4 changes are complete with all gates green but not yet landed — awaiting round-4 panel review / auto-land decision.
- Stale dev-server port squatting (`:1420`) has now sabotaged two consecutive rounds; whether to automate stale-server cleanup before the playwright gate remains undecided.