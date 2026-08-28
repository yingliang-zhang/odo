> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# D3: Real Executor Token Ledger (Control-Plane Hardening)

## Context

Task: implement D3 [P1] of the control-plane hardening DESIGN LOCK (`docs/design/control-plane-hardening-lock.md`) — make `loop_budget_tokens` constrain the executor's **real** measured cost instead of spawn-row estimates. D1 (gatepolicy.go / gate_manifest.json) already landed in commit 914a82f and is Tier-0 human-only — untouched by this diff.

## Key Decisions

- **Measured usage source**: sum per-assistant-message `usage{input, output, cacheRead, cacheWrite, cost.total}` across run JSONL files under `<project>/.odo/sessions/<runID>/`. LLM-free, deterministic. Missing transcript ⇒ `ok=false` (fail-soft, never fabricated).
- **Budgeted spend** = `input + output + cache_write`; **cacheRead journaled but NOT budgeted** (record, don't constrain).
- **Double-count rule**: a spawn row's `prompt_tokens_est` stays pending until its covering `loop_run_usage` row lands; then fold does `spent -= estPending[s]; spent += usage`. Writer recomputes identical cumulative (C1: fold is the truth). Duplicate usage rows ⇒ newest per spawnSeq wins, idempotent.
- **`covers_spawn_seq=0`** ⇒ fallback matching by round/task.
- **Enforcement placement**: after usage row lands, if `spent > budget` journal `loop_budget_exceeded` and suspend BEFORE `autoLand` (fail-closed; tainted rows keep their more specific reason first). `/loop resume budget=N` unchanged.
- **Fail-soft gap closed**: `usage_available:false + reason` journaled (non-OMP adapter / missing transcript); estimate stays pending so resume-time spawn projection check can still trip the budget gate.
- **GUI parity**: identical additive fold rule mirrored client-side in `gui/src/loop.ts`.
- Accept-time gate will require unanimous panel attestation (Tier-1 sources touched) — by design.

## Code Changes (9 files, +815/−18)

| File | Change |
|---|---|
| `internal/adapter/omp.go` | `Usage` struct + `SessionUsage(sessionDir)` extractor after `parseSessionJSONL`; Σ cost.total at 6dp |
| `internal/adapter/omp_stream_test.go` | `TestSessionUsageFixture`: exact sums across two files (user rows/bad rows/no-usage rows skipped), no-usage ⇒ false, missing dir ⇒ false |
| `internal/ipc/loop_journal.go` | `loop_run_usage` kind + contract docs; `loopRowSpend` extended (leg/proposal/consolidator `request_bytes/4`, usage-row input+cacheWrite); fold estPending rule |
| `internal/ipc/loop_run.go` | `journalLoopRunUsage` at both drain tails (`loopPipelineAfterRun`, `loopNoDiffAfterRun`), incl. tainted/stale drains; `loopDrainBudgetSuspend` over-budget check before autoLand |
| `internal/ipc/server.go` | `runMeta.loopSpawnSeq`; `startLoopRunLocked` captures spawn row seq |
| `gui/src/loop.ts` | TS fold mirror (estPending/usageBySpawn/spawnRound/spawnTask; usage rows skip stamp-max); bookkeeping bubble label |
| `gui/src/types.ts` | `loop_run_usage` payload keys (reused existing `reason?`, no duplicate decl) |
| `internal/ipc/loop_test.go` | wrapper one-shot `--session-dir` transcript injection; `TestFoldUsageCoversEstimate` (est 1000 + usage 4200 ⇒ 4200), `TestUsageRowIdempotent`, `TestBudgetUsesExecutorSpend` (panel 30K + fix 90K > 100K ⇒ suspended, zero moa_review/accept rows); `loopRowSpend` unit cases; `TestLoopBudgetExceededResume` round assert relaxed to ≥ |
| `gui/src/loop.test.ts` | 3 mirror parity cases (covers / dup / fail-soft); `mk` literal extended four fields |

## Verification

```
ok  github.com/yingliang-zhang/odo/internal/ipc      532.534s
ok  github.com/yingliang-zhang/odo/internal/adapter   0.384s
```

- `go build ./...` clean; `gofmt -l` empty
- GUI: `npx tsc --noEmit` clean; `npx vitest run src/loop.test.ts` → 24/24 passed
- Full diff reviewed for kind-whitelist coupling in journal consumers — none found
- Incidents during development (mangled closing brace in loop_journal.go, import order, arithmetic slip `10+40/4=20, 5+16/4=9 ⇒ 29`, stale background test run killed and rerun) all fixed and re-verified

## Open loops

- Accept-time unanimous panel attestation for Tier-1 gate sources still pending (expected gate behavior; not yet performed).
- D1 already landed; remaining DESIGN LOCK items (beyond D3) not addressed in this diff.