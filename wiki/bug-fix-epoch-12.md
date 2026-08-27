> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# P3 Hygiene Fixes: Heal-Row Window Eligibility + Paged Boot Replay (Revise Round 1)

## Status

Revise round 1 complete and verified; surviving a snapshot failure, the product was re-staged intact in a fresh worktree and awaits the pipeline verify gate. Diff: 7 files, +737/−142 — `auto.go`, `auto_cap_test.go`, `memory_replay.go`, `memory_replay_test.go`, `server.go`, `events.go`, `store_test.go`.

## Key Design Decisions

**Render/count split for heal-row exclusion**
- Render side unchanged: `foldExcludedMemoryUpdate` still excludes only `auto_distill`-layer `scheduled`/`skipped`/`cap_suspended_until`; heal rows continue to enter the distill prompt.
- Count side only (`measureWindow`): new predicate `windowExcludedMemoryUpdate`, a superset of the render predicate plus recovery-family causes (`recover`, `heal_merged`, `heal_conflict`, `heal_resolved`, plus defensive `heal_replayed`).
- Layer gate: counting exclusion applies only to the `replayLayerKind` family (memory/archive/user/pins/skill:*). Same-named causes on non-replay layers (note/wiki/auto_distill) still count as real activity — pinned by negative-space tests.
- Naming ambiguity resolved by evidence: repo-wide grep shows `heal_replayed` does not exist; `recover` is the only emitted log name. Predicate defensively excludes both spellings and a test pins this.

**Paged boot replay**
- `replayJournalReadPage = 512` with keyset pagination (`e.id > ? ORDER BY id LIMIT ?`), tested via the existing `replayJournalPageSizeForTest` convention including a cross-page equivalence drill.

**No-unbounded-API enforcement**
- `limit <= 0` in page listing is now a hard error (previously clamped to `1<<30`, silently resurrecting unbounded enumeration). Comments synced; `TestListProjectEventsPageContract` pins the hard error + keyset semantics.

**Replay fold pruning**
- `proposeByEpoch` unbounded growth bounded: on fold apply, superseded epochs are pruned while the current epoch is retained for same-epoch retry pairing. Worst-case degenerate path remains the pre-existing nil-propose conflict branch. Bounded by new `TestLaneMemReceiptFoldProposePrune`.
- Newest-lane selection reverted from map iteration order to lane first-seen-order traversal (unique event ids make `>` tie-free anyway; same posture as `orderedLayers`).

**Duplicated `rows.Scan`**: stale copy removed; error message unified to `list project events page: scan`.

## Verification (Round 1)

- `go build ./...`, `go vet ./internal/...` pass; `gofmt -l internal/` empty.
- 5 targeted tests green, including cross-page equivalence.
- Full suite: `go test ./internal/... -count=1 -timeout 25m` exit 0 (ipc 499.8s, total 501.9s).
- Untouched by design: GUI, cap numbers, landWG/auto-land, docs/attestation.

## Worktree Discipline Failure and Recovery

- Failure mode (occurred twice that day, tickets #81 and #83): round agent staged fixes into the **origin** worktree `6a8eba13-b8e39f721ac4` instead of its own freshly-bound worktree, so `drainRun` extracted an empty diff.
- Recovery: staged product extracted to `/tmp/p3-revise1.diff`, then a zero-change re-snapshot run bound fresh worktree `6a8ed6bd-a12e1152e3b6` (confirmed `pwd` + clean `git status`), applied the patch with `git apply --index` in one pass with no context mismatches, and verified `git diff --cached --stat` matches exactly (7 files, +737/−142). No code edits, no test runs, no sibling-worktree access in the re-snapshot run.
- Standing rule reinforced: the run's own bound worktree is the only canonical checkout; anything staged elsewhere does not exist.

## Open loops

- Pipeline verify gate must re-run against worktree `6a8ed6bd-a12e1152e3b6` and extract its staged diff.
- The diff still requires `odo accept`/`land` to main (out-of-band step, unchanged from prior rounds).