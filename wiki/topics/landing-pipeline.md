# Landing & Auto-Land Pipeline

- R-W4's design-MoA changes remain staged but uncommitted across surviving worktrees (6a80714e → 6a807575); landing is deferred to the auto-land/accept-diff pipeline and a prior attempt was blocked with reason repair_prompt_too_large (moa-chain-epoch-3)
- Round-2 revise imported the never-landed round-1 implementation into a fresh worktree via exported patch → git apply, then verified per-file parity with the audited diff before fixing on top (moa-chain-epoch-3)
- The prior panel cycle's accept (seq 302) landed as worktree commit ecaa7ce 'odo: accept diff #25', but that was the curator/learner/ADR-0003 batch — not R-W4, which no accept covers (moa-chain-epoch-2)
- R-W1's accept was recorded but the land was blocked on a stale base (auto_land_blocked, reason base_stale, seq 103); R-W3 was implemented and verified with no review/land event recorded (moa-chain-epoch-1)
- R-W1.5 was verified green but then blocked from landing by conflict + auto_land_blocked review actions on worktree 6a7f98f2 (daemon-misc-epoch-1)
- GUI Wave A (LedgerPanel receipts, background-runs visibility) and Wave B (meter, stats strip, panel picker) changesets are both uncommitted — commit decisions explicitly left to the user, with Wave B's files slicing cleanly onto three independent commits (gui-wave-epoch-1)
- Recurring pattern: verified-green changes repeatedly stall at the auto-land stage (repair_prompt_too_large, base_stale, conflict), leaving multiple workstreams with landed-tests-but-unlanded-code status (moa-chain-epoch-3)
