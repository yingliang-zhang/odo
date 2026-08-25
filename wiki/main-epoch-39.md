> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Memory Distill Reliability — Auto-Revise Round 1

## Context

Auto-revise round on a previously reviewed diff for the Go server (memory distillation, workstreams, IPC handlers). The worktree was clean and lacked the reviewed diff, so the diff was re-applied first; base matched the diff pre-image, and the baseline built cleanly before fixes began. Three review findings were addressed.

## Key decisions

- **Marker-first recovery for memory apply (Finding 1).** `applyResolvedBatch` restructured to journal an intent marker *before* performing writes, closing the crash window where a batch is applied but never marked. Entry heal runs first; recovery types plus two heal functions added after `applyResolvedBatch`. Hashes hoisted and unified across cause rows.
- **Heal semantics upgraded to multi-marker, per-layer newest-wins claims.** The heal walks all stranded markers rather than a single one, because `handleApplyMemory` refuses consumed batches *before* the core heals — so heal must also run on that refusal path and in the auto-gate sweep; otherwise a distill right after a crash retires the stranded marker unhealed.
- **Journal-first pins, heal-before-RMW (Finding 1, pin path).** Reaffirm is a set-to-epoch (not a counter), so double-apply risk is archive duplicates/ordering; marker-first journaling closes the class regardless.
- **Atomic registration guard for run-start duplication (Finding 2).** New struct field + init, guard helpers placed next to `checkConversation`. `handleDeleteWorkstream` restructured around the atomic flag. All start sites wired: `handleSendMessage` tail, slash routes, auto fire/arm, slash slot in `preview.go`, distill entry, loop start. Placement verified airtight: `handleSendMessage` holds `s.mu` for its whole body; slash routes lock inside their own handlers.
- **Collision-free v4 dedupe in the store (Finding 3).** Store-layer change with a dedicated v4 collision test.
- **Failpoint seams** added to the Server struct to enable crash-injection tests of the recovery protocol.

## Code changes

- Store: v4 dedupe made collision-free; v4 collision test added.
- Server: atomic guard field + init; guard helpers near `checkConversation`; `handleDeleteWorkstream` restructure; guards wired at every distill/run-start site; `preview.go` duplicate `w` load after the slash slot fixed.
- Memory apply: marker-first block in `applyResolvedBatch`, hoisted hashes, unified cause rows, recovery types, two heal functions; heal hooked into the consumed-refusal path and the auto-gate sweep.
- Pins: journal-first write with recovery; heal-before-RMW.
- Tests: three review-follow-up tests appended (helpers verified to exist beforehand).
- GUI (`App.tsx` from the base diff): `tsc` clean, vitest 166/166.

## Verification status

- Baseline build clean; post-edit build clean.
- Store suite green (twice, including after the v4 change).
- Full Go suite: root/adapter/git packages green.
- An earlier background ipc run (`bg_10`) was cancelled — it tested a stale build predating the claims rewrite, sweep hook, refusal heal, and new tests.
- One `gofmt -l` check was invalidated (stash baseline mixed untracked test files with stashed sources); formatting provenance unresolved.
- A self-review pass over the final diff was completed; final shape of the `applyResolvedBatch` marker section verified by read.

## Open loops

- IPC suite failed on the final build; `tail` swallowed the failing test name — re-run with a FAIL filter was in flight; the failing test is not yet identified or fixed.
- Whether the `gofmt -l` flags are pre-existing or introduced by the diff remains undetermined (first baseline attempt was invalid).