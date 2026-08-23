# Odo — #34 Ledger Correction and Majority-Vote Doctrine for Diverged Panels (2026-08-22)

## Trigger
User confirmed the fix-status list (7 items from the prior session), then issued two directives: (1) reconcile the #34 ledger divergence by appending a journal correction record; (2) when a panel remains diverged after 3 review-and-fix rounds, decide by strict majority (>½).

## Prior state (baseline)
- Any panel reject — including `panel_mixed` — triggered immediate auto-reject; only `needs_fixes` entered the ≤3-round repair ladder.
- Existing valve from the 2026-08-16 tri-model decision: at round cap, land only if ≥2/3 accept with zero rejects (`majority_accept`); otherwise suspend for human.
- Root motivating case: #34 was a `panel_mixed` verdict (kimi accepted; another judge rejected due to the anchor bug) and was instantly rejected in the store while the git side was actually landed.

## Decisions
- **Ledger divergence (#5 from status list)**: reconcile via journal correction record, not silent DB mutation. Executed: `ledger_correction` entry appended to session 1's journal at seq 8017 (atomic SELECT-INSERT; `rejected` store state left untouched). Verified visible via CLI.
- **New settlement doctrine**: divergent verdicts (including `panel_mixed`) enter the repair ladder instead of being auto-rejected; if divergence persists past 3 review-and-fix rounds, a strict majority (>½ accepts) decides landing vs rejection; no majority → suspend for human review.
- **Accepted, non-actioned**: tail-leg starvation (5 slots × ~24min → false-positive `panel_infra`) remains an accepted fail-closed behavior; fix only if observed in practice.

## Code changes
- `autoland.go`: settlement route — `panel_mixed` no longer routes to instant auto-reject; it enters the repair ladder.
- `settle.go`: updated header doctrine comments (verdict classification table, round-cap semantics); `settleRevise` doc; new mixed variant in `settleRepairPrompt`; `settleDraft` cap branch implementing strict-majority (>½) decision at the round cap, replacing the ≥2/3-and-zero-reject gate for diverged panels.
- Tests: updated existing expectations (auto-reject → repair-ladder entry); added two cap-behavior tests; settled failing expectations after the first test run.
- Journal (task 1): `ledger_correction` at seq 8017.

## Verification
- `go build` / `go vet` clean; gofmt applied to touched files.
- Settle/autoland targeted tests green, including the new cap tests and prior regressions (`TestRecoverReFireAnchorsStoredGoal`, `TestAutoLandBlockPrecedence`).
- Earlier items (commits `a5c98ca` for #34 landing, `abca6f1` for the anchor bug + precedence fix) verified in the prior session: full suite green (468s).

## Open loops
- Tail-leg starvation → false-positive `panel_infra`: accepted fail-closed; revisit only if it occurs in practice.
- Known gofmt debt retained intentionally: `loop_audit.go`, `server_test.go` (brought in by #34; recorded in wiki).
- Full test-suite re-run after the majority-policy change is not evidenced in this trace (only targeted settle/autoland runs were captured green); commit hash for the policy change not recorded.