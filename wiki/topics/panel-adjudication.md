# Panel Voting & Adjudication

- Three-round panel semantics: rounds 1-2 require unanimous accept (fail-closed, any reject auto-rejects); terminal round 3 passes on the majority valve with majority_accept evidence persisted; implemented as settleMaxReviseRounds 3->2 (main-epoch-21)
- The majority valve's only reachable composition is {2 accept, 1 needs_fixes} — a reject leg auto-rejects before the ladder and an infra leg blocks before evaluation; any truncated leg invalidates the valve (conservative fail-closed) (main-epoch-21)
- Truncated legs are forced to needs_fixes at verdict construction (reviewVerdict) plus an explicit cap-loop abstention as local self-proof against producer regressions; pinned by TestReviewVerdictTruncationForcesNeedsFixes (main-epoch-20)
- Unanimity is single-sourced: consensusVerdict's accept branch delegates to panelAccepts as the one rule; the reject short-circuit is unchanged (main-epoch-23)
- Panel claims are adjudicated against source before action: sameAutoDistillList's duplicate-id hole was valid-but-unreachable and fixed with consume semantics (3 tests), while the memo-defeat and Lstat-bypass charges were refuted with file:line evidence (main-epoch-33)
- Factually refuted rejections (glm's fabricated reasons on diff #41) are resolved by manual land plus a journal ledger_correction, not a re-panel that risks repeating already-refuted reasoning for zero information gain (main-epoch-27)
- Design-MoA keeps the design_proposals wire key because encoding/json's dominant-field rule would silently drop same-named siblings colliding with memory_proposals' proposals field (moa-chain-epoch-3)
- Design-MoA failure semantics: single-leg truncation degrades-and-continues (whole pipeline fails only if ALL legs truncate); all four failure sites attach proposals and a consolidator receipt block to the design/failed marker (moa-chain-epoch-3)
