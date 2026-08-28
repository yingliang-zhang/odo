# Fix: TestRevertEpoch failure (diff #92 D4 memory revert, revise round)

## Context

- Project: `odo` (~`~/Projects/odo`), feature area D4 (memory revert authority) of `docs/design/control-plane-hardening-lock.md`.
- Trigger: auto-land of diff #92 blocked on `verify_failed` — `TestRevertEpoch` flaked in sandbox verify:
  `memory_revert_test.go:106: replay journaled 1 recover rows for a reverted epoch, want 0: [cause:recover ... (seq 3, conversation 1) layer:user]`.
- Objective: make human `odo memory revert <epoch>` authoritative downstream — reverted epochs' receipts must never re-fire, re-apply, or strand — without expanding scope beyond the defect.

## Root cause (two defects, one real + one test)

1. **Non-hermetic test (verify blocker).** The spurious row was **epoch 5's user.md scope-refusal probe receipt** (`seq 3, conversation 1, layer:user`), not the reverted epoch 2 — epoch-matching fold retirement had no jurisdiction over it. `TestRevertEpoch` was the only replay test without `t.Setenv("HOME", t.TempDir())`; the user-layer receipt's `before == sha16(nil)`, so the outcome depended on ambient `$HOME/.odo/user.md`:
   - Dev machine (real user.md present) → foreign state → `heal_conflict` → pass.
   - Verify sandbox (no user.md) → empty matches `before` → replay restores `"x\n"` and journals a `recover` row → assertion fails. Reproduced locally by both paths with the same patch.

2. **Real engine gap: stale-snapshot fold.** Fold-time revert retirement only covers revert rows journaled **after** the event snapshot; live passes (sweep repair, apply-retry, pins RMW) fold snapshots taken **before** a concurrent human revert, so evaluate-time would classify the post-revert pre-image state as a mid-write crash and restore over the revert.

## Decisions

- **Evaluate-time authority check** in `evalMemReceipt` (not only fold-time retirement): consult the lane's revert ledger before any recovery action; a reverted epoch is terminal.
- **Fail-closed**: if the revert-lookup query errors, skip recovery entirely — never re-apply over a human revert; no file write, no journal row.
- **Visibility + idempotence in one**: journal a `memory_update{layer:"apply", cause:"revert_suppressed_recovery", epoch, receipt_layer}` row on the receipt's own lane (same write position as revert receipts); the ledger row doubles as the idempotence record keyed by `(epoch, receipt_layer)`.
- **Zero-cost bypass** for `epoch == 0` (pins/legacy receipts).
- No changes needed to `unownedFoldGrowth` or distill rendering predicates: layer `"apply"` is already an attributed metadata layer, so suppression rows group with revert rows.
- GUI untouched: suppression rows are structurally identical to revert rows; additive key safely ignored.

## Code changes

| File | Change |
|---|---|
| `internal/store/events.go` | Added `ListApplyRevertRows` — LIKE-filtered query for one lane's `memory_update{layer:"apply", cause:"revert"|"revert_suppressed_recovery"}` rows, following the existing `ListHealLedgerRows` precedent (no full-lane materialization). |
| `internal/ipc/memory_replay.go` | `evalMemReceipt` entry: D4 authority check — epoch≠0 → consult lane revert ledger; reverted → skip restore/merge/conflict, idempotently journal `revert_suppressed_recovery`; lookup error → fail-closed no-op. Header D4 comment updated. |
| `internal/ipc/memory_revert_test.go` | `TestRevertEpoch`: added HOME isolation + pinned user.md to its after state (`"x\n"`) so the epoch-5 receipt deterministically reads as landed. Added `TestRevertSuppressedRecovery`: real `RevertMemoryEpoch` + stale snapshot through `replayLaneMemReceipts`, asserting no restore, no `heal_conflict`, exactly one well-formed suppression row, idempotent repeat, and fail-closed behavior with a broken ctx. |

Prep work: the revise worktree (clean at b71600d) lacked diff #92 content; applied archived patch `.odo/diffs/6a915dbf-b10fa1b6b28c.diff` (17 files) before fixing.

## Verification (all green)

- `gofmt -l .` empty; `go build ./...` and `go vet ./internal/ipc/ ./internal/store/ .` pass.
- Focused: `TestRevertEpoch` (0.24s), `TestRevertSuppressedRecovery` (0.10s) — pass.
- Replay/revert/autogate family (`TestRevert*`, `TestMemoryReplay*`, `TestReplay*`, `TestHeal*`, `TestPins*`, usermd/autogate/distill tests): ok, 13.3s.
- `go test ./internal/store/` ok 0.437s; `go test .` ok 0.628s.
- One full `go test ./internal/ipc/ -timeout=700s -count=1`: ok, 524.249s.

## Scope compliance

- Did NOT touch `gatepolicy.go`, `gate_manifest.json`, `.odo-verify`; no D4 surface expansion.
- Delivery surface vs. 1c31dea-ready base: diff #92 full set + the 3 revise files (13 modified/added, +658 −31).

## Open loops

None.