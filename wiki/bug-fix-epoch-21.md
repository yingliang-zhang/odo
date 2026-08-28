# D6 Design-MoA Diversity Gate (Control-Plane Hardening)

Final item of the control-plane hardening wave per `docs/design/control-plane-hardening-lock.md`. Enforces: the auto design gate requires ≥2 successful legs from ≥2 distinct model families; otherwise the design lock is journaled but stays pending for the human gate. D1–D5/D7 landed previously and were not reworked. Tier-0 files (`gatepolicy.go`, `gate_manifest.json`) and `.odo-verify` untouched.

## Key decisions

- **Family = model, not label.** `model_family` derives from the bare model id (`m.model`, `@provider` suffix stripped) via D7's exported `modelspec.Family`. Same model under two provider labels ⇒ same family ⇒ refused; label diversity is not model diversity. Stricter than D7's whole-label call, matching spec text.
- **Diversity computed over the good set only.** `designDiversityOf` counts successful legs: `{legs_successful, distinct_families, distinct_endpoints, single_endpoint}`. Failed legs still carry the per-leg receipt.
- **Fail-closed to the stronger gate.** Auto-gate refusal journals the lock row with `auto_gate:"refused_diversity"`, skips `spawnLoopImplement`, and leaves `designLockSeq` pending — exactly the state `loop_design_gate: human` produces. Task never skipped; `loop_ctl approve_design` consumes the same pending gate.
- **Manual path unchanged.** `handleDesignMoa` still emits a lock on a single successful leg; the diversity block is journaled into the row for visibility only.
- **Endpoint honesty.** Receipt uses `scrubBaseURL` of the shared client — explicitly records the single gateway rather than pretending leg endpoints differ.
- **Zero modelspec changes.** D7's `Family` is exported and `TestFamily` already covers the D6 table (unknown models ⇒ raw basename).
- **Journal-first, additive payload keys only** (ADR-0002 immune). GUI needs no changes: additive keys are safely ignored and the refused state reuses the existing "awaiting design approval" UI.

## Code changes

| File | Change |
|---|---|
| `internal/ipc/protocol.go` | `DesignProposal` += additive `endpoint`, `model_family` JSON fields |
| `internal/ipc/design_moa.go` | Per-leg receipts (`scrubBaseURL(client.BaseURL)` + `modelspec.Family`); new `designDiversity` struct, `designDiversityOf`, `designGateAdmits` (`legs ≥ 2 && fams ≥ 2`); outcome carries diversity; manual path journals diversity block |
| `internal/ipc/loop_run.go` | `runLoopDesign` auto gate routes through `designGateAdmits`; refusal ⇒ `auto_gate:"refused_diversity"` row, no spawn, pending fold; stale comment corrected |
| `internal/ipc/loop_journal.go` | `loop_design_lock` journal-contract comment documents `diversity{}` / `auto_gate?` |
| `internal/ipc/design_moa_test.go` | New tests (below) |

## Tests

- `TestRunDesignMoaDiversityGate`: 2 legs same family (`kimi-k3@t9s, kimi-9x@other`) ⇒ refused, diversity `{2,1,1,true}`; 1 leg ⇒ refused `{1,1,1,true}`; 2 legs 2 families ⇒ admitted, no `auto_gate` key, `loop_task_spawn` row lands.
- `TestAutoGateFallsBackToHuman`: refusal leaves exact human-gate fold state (`designLockSeq > 0`, no spawn, loop active); `approve_design` then spawns; manual `/design_moa` with 1 leg still locks with diversity in row.
- Debugging notes: implement-run drain requires client poll (`pollDone` drives `drainRun`); no-diff implement drain's actual suspend cause is `fix_no_diff`, not `restart_mid_run` — pinned accordingly.

## Verification

- `gofmt -l` empty; `go build ./...` pass; `go vet ./internal/ipc/ ./internal/modelspec/` pass.
- Focused D6: `ok github.com/yingliang-zhang/odo/internal/ipc 3.988s` (5 subtests).
- Changed-surface family (TestDesignMoa/TestLoop*/RowSpend): `ok … 4.067s`.
- One full suite run `go test ./internal/ipc/ -timeout=700s -count=1`: `ok … 533.969s`.
- Root package `ok … 0.928s`; modelspec `ok … 0.340s`.

## Open loops

- D6 diff is uncommitted in the assigned worktree, awaiting the auto-land pipeline.