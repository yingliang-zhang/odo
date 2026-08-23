# Auto-Land Panel Voting Policy

- Panel = 3 legs (t9s/kimi-k3, glm-5.2, deepseek-v4-flash) armed via prefs `review:` line; legs run parallel QueryWithTools with ~24min timeout (main-epoch-21, UI-epoch-9)
- Final rule: 3 total review rounds — rounds 1–2 fail-closed unanimous accept (any reject → auto-reject); terminal round 3 lands on majority via the majority valve; implemented as `settleMaxReviseRounds 3→2` with `majority_accept` evidence persisted before land, pinned by new `TestSettleMajorityValveLandsAtCap` (main-epoch-21)
- Majority valve only reachable as {2 accept, 1 needs_fixes}: a reject leg auto-rejects before the ladder and an infra leg blocks before evaluation (main-epoch-21)
- Truncated leg invalidates the valve (fail-closed); relax needs explicit user decision — open loop (main-epoch-21)
- Truncated→needs_fixes is enforced at construction in `reviewVerdict` (server.go:3251), pinned by `TestReviewVerdictTruncationForcesNeedsFixes`; explicit `continue` in the cap loop kept as local self-proof against producer regressions (main-epoch-20)
- Diff #36 (2:1 carry) was auto-rejected under the old one-vote-veto because the live daemon was a stale #27-era binary; under the majority rule it would have landed — superseded by the cap 3→2 change, patch on disk as history (main-epoch-21)
- kimi-leg objections adjudicated: majority exists only in terminal round (veto retained rounds 1–2); `TestSettleMixedRejectAutoRejects` replaced by ladder variant leaving old property unguarded (main-epoch-21)
- tail-leg starvation → false panel_infra retained fail-closed by prior decision; no instances observed (main-epoch-20)
- Cap-round test flakiness root-caused to evidence→action→supersede ordering window (producer writes sequentially, test polls); 5 race points fixed, battery passed 5 consecutive runs (main-epoch-20)
