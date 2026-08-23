# Gate-Source Protection & Landing Doctrine

- Nine protectedGateFiles (autoland, autonomy, learner, review, settle, ledger, risk, contradiction, design_moa): touching any forces `protected_path` auto-land block + manual human accept — by design, not a fault (UI-epoch-6, bug-fix-epoch-1, UI-epoch-5, main-epoch-21)
- Zero-manual-lock doctrine (landed main `5ec522b`): three-valued `autoLandCheck` downgrades gate-source/new-dir/assertion edits from hard block to panel risk annotation; hard blocks remain only for memory and supply-chain paths (UI-epoch-11)
- Execution-layer evidence gate `panelVerdictAttestsDiff` binds the panel verdict to exact landing bytes via `patch_sha16`; `rejectProtectedPaths`/`rejectExecutorPaths` deleted, `rejectMemoryPaths` added, zero residue (UI-epoch-11)
- Doctrine rebuilt from journal replay of 24 verbatim write/edit payloads (UI session seq 3933–4204) after transcript prune; original run died mid diff-registration on its own daemon restart (UI-epoch-11)
- Doctrine was lost once before: completed green at 22:11 but never registered as a diff when the #25 accept advanced main and wiped worktrees (UI-epoch-10)
- Human diff review declared low-value by user; future reviews fully automatic (UI-epoch-10)
- Review-fatigue mitigation: batch protected-file changes into larger single diffs (UI-epoch-6)
- #22's `verify_failed` block was a stale pre-rebase verdict; snapshot rebased to `9c7e52d` byte-identical, main verified green after landing (UI-epoch-10)
- Human reject always passes even when GUI is locked: daemon `handleDiffAction` has no pipeline lock, only acceptMu + status checks; used to reject #21 via `odo.sock` (UI-epoch-10)
