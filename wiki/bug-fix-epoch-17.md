# D7 Verdict Policy Implementation (Control-Plane Hardening)

Implementation of **D7 [P1] Verdict policy** from `docs/design/control-plane-hardening-lock.md` (binding spec, ruling ③ = K3 general settle table). D1/D3/D5 already landed (914a82f, d0f0a5c, 8adc8d9) and were not reworked. Tier-0 (`gatepolicy.go`, `gate_manifest.json`) and `.odo-verify` untouched.

## Goal

End the single-voice coin-flip: one dissenting review leg (or same-family correlated dissent) SUSPENDS for human triage instead of auto-rejecting the chain. Only ≥2 rejects from ≥2 distinct model families auto-reject. Verify failures stay out of the auto-reject set entirely.

## Key decisions

- **Two new settlement classes** replace the old reject split:
  - `reject_independent`: ≥2 reject legs from ≥2 distinct model families ⇒ auto-reject via existing M20 mechanics (reject row + supersedeChain + advisory).
  - `reject_minority`: exactly 1 reject leg, OR ≥2 rejects all same family, no infra ⇒ SUSPEND via journaled `auto_land_blocked{reason:"panel_minority_reject"}`; no reject row, no supersedeChain, diff stays PENDING, transcript advisory for inbox triage.
- **Model family** = basename prefix before first `-`, else the basename; lowercase-folded, pure, LLM-free, deterministic. Exported as `modelspec.Family` for D6 reuse. Same model across provider tags ⇒ same family.
- **Single-family panels have zero auto-reject capacity** — everything minority-suspends: fail-closed and visible.
- **Bounded re-panel on recovery**: `panel_minority_reject` is non-terminal while `repanel_count < 2` (restart recovery re-fires the pipeline once with a fresh panel); `repanel_count >= 2` ⇒ terminal, parked human-only, inbox still shows the diff.
- **Fail-closed ledger**: `minorityRepanelCount` is journal-derived; on read failure it returns the max (parked) rather than risking an unbounded re-panel loop.
- **Additive journal only** (ADR-0002 immune): new `journalAutoLandBlockedExtra` variant carries `repanel_count`; the ~14 other blocked reasons keep byte-identical payload shape.
- **`consensusVerdict` tally unchanged** (server.go ~4074): classification lives in `settlementClass` + the recovery filter. Old class names (`reject_unanimous`/`reject_mixed`) removed as classes; reason strings keep the unanimity distinction.
- **Verify failures remain implementation evidence** in the blocked+revise lane; no verify-reject path introduced.
- **Loop interaction required no edit**: `loop_journal.go:605` already folds any attributed blocked row to `fixOutcome="unlanded"` (comment anticipated D1+D7); no loop suspension — the audit engine owns convergence.

## Code changes

| File | Change |
|---|---|
| `internal/modelspec/modelspec.go` | New exported `Family(model)` pure function (`t9s/kimi-k3`→`kimi`, `deepseek-v4-flash`→`deepseek`, `gpt-5.6`→`gpt`, unknown ⇒ raw basename) |
| `internal/modelspec/modelspec_test.go` | `TestFamily` table incl. provider-tag folding |
| `internal/ipc/settle.go` | `settlementClass` reject region re-split by family; `panelMinorityRepanelMax = 2`; `minorityRepanelCount` journal-derived ledger (fail-closed); header contract comment updated |
| `internal/ipc/autoland.go` | autoLand switch: independent → M20 mechanics; minority → journal blocked row + advisory only, diff stays PENDING; `journalAutoLandBlockedExtra` for additive `repanel_count`; `pipelineTerminalDiffIDs` treats minority rows as non-terminal below count 2, terminal/parked at ≥2 |
| `internal/ipc/settle_test.go` | `TestSettlementClass` table rewritten (model:verdict pairs); new `TestSettlementMinority`, `TestMinoritySuspends` (single reject + same-family 3-reject subcases + repanel_count increment), `TestIndependentRejectAutoRejects`, `TestRepanelBounded`, `TestLoopFixMinorityUnlanded`; stale `TestSettleMixedRejectAutoRejects` pin replaced by `TestMinoritySuspends` |

~40 lines of production logic; no new event types.

## Verification (verbatim)

- `go build ./...` clean; `go vet ./internal/ipc/` clean; `gofmt -l` empty
- Focused: `TestSettlementClass|TestSettlementMinority|TestMinoritySuspends|TestIndependentRejectAutoRejects|TestSettleUnanimousRejectAutoRejects|TestRepanelBounded|TestLoopFixMinorityUnlanded` → `ok github.com/yingliang-zhang/odo/internal/ipc 3.993s`; `ok .../internal/modelspec 1.971s`
- Adjacent contract sweep (`TestSettle*|TestPipelineTerminalDiffIDs|TestStrandedPendingDiffs|TestRecoverReFireAnchorsStoredGoal|Autonomy×2|TestLoopFoldAttributesPanelLandedFix`) → `ok` 60.369s
- Full ipc suite, single run: `ok github.com/yingliang-zhang/odo/internal/ipc 634.927s`

Incident during implementation: the edit engine dropped the `git` import line in settle.go (build error `undefined: git`); restored immediately and re-verified.

## Open loops

- Changes are uncommitted in the worktree, awaiting pipeline verify + panel review before landing.
- D6 (which reuses `modelspec.Family`) not yet implemented.