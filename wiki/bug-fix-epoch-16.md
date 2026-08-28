> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Control-Plane Hardening: D3 Re-Apply and D5 Finding Identity v4

Session covering two pipeline tasks against the binding spec `docs/design/control-plane-hardening-lock.md` (repo `~/Projects/odo`): re-applying the proven-but-blocked D3 token-usage-ledger diff, then implementing D5.

## Key decisions

- **D3 re-entry as a fresh pipeline run.** Diff #87 was fully implemented and proven (full ipc suite PASS, 534.9s) but verify failed solely on a capacity bug: go test's default 600s timeout vs a ~535s suite under daemon load. Human commit `8edcbb9` fixed verify (`go test -timeout=14m ./...`, guard 15m). The original run was journal-terminal (`auto_land_blocked`), so the existing patch `6a91167f-0d8a19295f59.diff` was re-applied onto HEAD `8edcbb9` rather than resurrecting the old run.
- **Minimal-verify discipline.** Agent ran only focused tests (`TestLoopRunUsage|TestSessionUsage|TestFoldUsage|TestBudgetUsesExecutorSpend|TestUsageRowIdempotent`, adapter suite, build, vet) and stopped with changes staged — full suite left to pipeline verify; no commit, no touching main.
- **Tier-0 off-limits.** `.odo-verify`, `internal/ipc/gatepolicy.go`, `internal/ipc/gate_manifest.json` untouched in both tasks (accept gate would refuse).
- **D5 fingerprint = structural identity, not wording.** FP v4 = `sha16(norm(file) + "|" + norm(symbol) + "|" + category [+ "|" + rule])`; `title`/`evidence`/`expected`/`actual` demoted to mutable description (most-severe sighting wins, never hashed). Category is identity, never severity — `blockingFindings` semantics unchanged. Purpose: stop model-wording drift creating phantom findings.
- **Per-leg dedup before Legs counting.** Each leg's findings deduped by FP (max severity kept) first, then folded across legs; `Legs` = distinct legs; additive `leg_ids [int]` journaled, aligned to journaled `legs[]` fan-out positions for falsifiability. `perLeg` in `loop_run.go` made position-preserving (degraded leg → nil) to keep that alignment.
- **Migration = append-only, no rewrite.** Old v3 FPs stay as historical identifiers; boundary findings count as new for one round (no false stall — `loopStallCheck` arms only after a landed fix); C6 closure matching stays on verbatim row text, unaffected. Old 4-field finding lines parse with `cat=other` (backward-compatible).

## Code changes

**D3 re-apply (accepted by auto_panel, landed as `d0f0a5c`):** patch applied clean at `8edcbb9`, 9 files, +818/−18 — `internal/ipc/loop_journal.go`, `loop_run.go`, `loop_test.go`, `server.go`, `internal/adapter/omp.go`, `omp_stream_test.go`, `gui/src/loop.ts`, `loop.test.ts`, `types.ts`. No conflicts (base `914a82f` → HEAD delta touched only `.odo-verify` and `autoland.go`).

**D5 implementation (changed files):**

| File | Change |
|---|---|
| `internal/ipc/loop_audit.go` | FP v4 hash; `finding` +`cat`/`rule`/`leg_ids` JSON fields (additive); `findingLineRe` parses `cat`/`rule` additively (old lines ⇒ `cat=other`); `unionFindings` = per-leg FP dedup then cross-leg fold with `Legs`/`leg_ids`; `normFindingCategory` fixed set (`correctness\|contract\|security\|resource\|test-integrity\|drift\|other`, unknown ⇒ `other`); `findingLineText` shared renderer; rubric +1 line (category set + optional `rule`); C6 closure discipline untouched |
| `internal/ipc/loop_run.go` | `perLeg` position-preserving so `leg_ids` indexes match journaled `legs[]` |
| `internal/ipc/loop_test.go` | +4 tests: `TestFindingFingerprintV4`, `TestUnionPerLegDedup`, `TestParseBackwardCompat`, `TestUpgradeBoundaryNoFalseStall` |

**Verification (verbatim):** focused tests all PASS; one full ipc run `ok github.com/yingliang-zhang/odo/internal/ipc 531.238s`; `go build ./...`, `gofmt -l`, `go vet ./internal/ipc/` clean. D3 ledger code and Tier-0 files untouched (`git diff` empty).

## Incidents

- First D3 re-apply spawn failed: `omp_with_timeout.sh: permission denied`; replay succeeded.
- Mid-D5, an edit-engine half-application mangled `loop_audit.go`; repaired from a fresh read of the damaged region, then all tests green. Trivially recoverable, no lasting effect.

## Open loops

- D5 run must still pass pipeline verify (`-timeout=14m`) and auto_panel before landing.
- Whether other hardening-lock items exist beyond landed D1 (`914a82f`), D3 (`d0f0a5c`), and now-implemented D5 — numbering implies D2/D4 slots in the spec, not covered this session.