# Auto-Land Governance & Panel Voting

- Gate-source files (protectedGateFiles: autoland, autonomy, learner, review, settle, ledger, risk, contradiction, design_moa) are panel-fact annotations fed into review, not blockers; since 2026-08-22 they require unanimous panel attestation because the majority valve has no attestation power (main-epoch-24)
- Three-round rule: rounds 1-2 stay fail-closed (any reject auto-rejects); the terminal round-3 evaluation applies the majority valve (>=2/3 accept, zero reject/infra/truncated) and persists `majority_accept` evidence; implemented by `settleMaxReviseRounds 3->2` (main-epoch-21)
- The majority valve's only reachable composition is {2 accept, 1 needs_fixes}: a reject leg auto-rejects before the ladder and an infra leg blocks before evaluation; a truncated leg invalidates the valve (fail-closed retained) (main-epoch-21)
- Doctrine (landed 5ec522b): gate-source edits, new top-level dirs, and net-new assertions downgrade from hard block to panel risk annotation; execution-layer gate `panelVerdictAttestsDiff` binds the verdict to the exact landing bytes via `patch_sha16` (UI-epoch-11)
- Non-panel_infra `auto_land_blocked` states are terminal for diffs (autoland.go:150): no replay on restart, no re-attestation; Accept-once is the only unlock, and re-running is equivalent but costlier (burns a run + panel, zero information gain) (main-epoch-25)
- Human accept bypasses gate-source restrictions (pinned by `TestHumanAcceptGateSourceAllowed`) and is the unconditional escape hatch for terminal blocks (main-epoch-22)
- Manual gates recur by design whenever diffs touch protected files; mitigation for review fatigue is batching protected-file changes into larger single diffs, not loosening the gate (UI-epoch-6)
- Unanimity single-sourcing: `consensusVerdict` accept-branch delegates to `panelAccepts`; the reject short-circuit is unchanged, with pins proving infra-accept is not accept and reject overrides infra (main-epoch-23)
