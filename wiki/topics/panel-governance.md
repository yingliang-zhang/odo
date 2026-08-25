# Panel Review Governance

- Current structure: 3 evaluations total — rounds 1–2 require unanimous accept (fail-closed); round 3 (cap) passes on the majority valve (`settleMaxReviseRounds 3→2`), persisting `majority_accept` evidence before land (main-epoch-21)
- Gate-source diffs require unanimous panel attestation since 2026-08-22; the majority valve has no attestation power and is permanently excluded for `protectedGateFiles` (main-epoch-21) (main-epoch-24)
- The majority valve's only reachable composition is {2 accept, 1 needs_fixes}: a reject leg auto-rejects before the ladder, an infra leg blocks before evaluation, and any truncated leg invalidates the valve (main-epoch-21)
- Truncated legs are forced to needs_fixes at construction (`reviewVerdict`, server.go:3251); an explicit abstention `continue` in the cap-count loop pins the invariant against future producer regressions, with `TestReviewVerdictTruncationForcesNeedsFixes` as the construction-side contract (main-epoch-20)
- `panel_mixed` is terminal auto-reject under the M20 charter; `panel_infra` is deliberately re-fired on restart because infra failure is not a verdict (main-epoch-27) (UI-epoch-9)
- Unanimity is single-sourced: `consensusVerdict`'s accept branch delegates to `panelAccepts`; the reject short-circuit is unchanged, with pins that infra-accept ≠ accept and reject overrides infra (main-epoch-23)
- Panel verdicts are byte-bound: `panelVerdictAttestsDiff` gates execution on `patch_sha16` matching the exact landing bytes (UI-epoch-11)
- Review objective anchoring: schema v3 added `diffs.goal` (954ff22, cherry-picked from an orphaned commit) so delayed/re-fired reviews bind the correct objective; its absence caused #34's wrongful rejection (main-epoch-20) (main-epoch-23)
- Panel slowness is not death: leg timeout ~24 min, serving produces no per-request log lines, and pgrep gives false negatives — use full `ps`, PPID lineage, and store queries before concluding a daemon died (bug-fix-epoch-4) (main-epoch-33)
