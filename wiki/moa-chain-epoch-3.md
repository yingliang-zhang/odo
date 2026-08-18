# R-W4 Design-MoA Auto-Revise Summary

## Context

Two auto-revise rounds against the R-W4 "design MoA" implementation (Go: `internal/ipc`, `internal/moa`; new files `design_moa.go` / `design_moa_test.go`, increments to `fstools.go`, `protocol.go`, `server.go`). Baseline `9904a7d`. Landing is deferred to the auto-land/accept-diff pipeline, not the session.

## Key decisions

1. **Worktree handling (round 2 precondition):** the audited implementation had never landed to main — it sat staged in surviving worktree `6a80714e`. Round-2 worktree `6a807575` (same baseline) imported it via exported patch → `git apply`, verified per-file parity with the audited diff, then fixed on top.
2. **F1 — wire key naming:** instruction demanded `proposals`; code uses `design_proposals`. Literal compliance rejected — `Response.Proposals` is already claimed by `memory_proposals`, and encoding/json's dominant-field rule would silently drop both same-named sibling fields, corrupting every memory_proposals response. `design_proposals` kept as minimal-fidelity deviation; rationale documented in `protocol.go` comments.
3. **F2 — single-leg truncation semantics:** instruction's test point (error) conflicted with its own constraint clause ("don't fail the whole pipeline unless ALL legs truncate") plus both design docs. Constraint + design docs won; degrade-and-continue behavior unchanged. Instead, the truncation test structure was consolidated.
4. **Fail-closed everywhere:** symlink escape, empty consolidator lock, and receipt loss are all refusal paths — never silent degradation.

## Code changes

**Round 1** (3 findings, all fixed):

| Finding | Fix | Test pin |
|---|---|---|
| `designContextBlock` symlink escape (lexical check only) | Mirror `fsToolExecutor.resolve`: `EvalSymlinks` root once (avoids macOS `/var`→`/private/var` false positive), lexical check per file + post-resolve validation; escapers rejected; `EvalSymlinks` failure falls into existing unreadable-note degradation | `TestDesignMoaContextSymlinkEscape` (escape rejected pre-fanout, secret never leaves; in-root symlink control passes) |
| Empty consolidator response counted as success | Fail-closed: whitespace-only lock → error + one `memory_update{cause:"failed"}` marker, no `design_lock` line; nonempty lock preserved verbatim | `TestDesignMoaEmptyLockFailClosed` (4 requests, error contains "empty design lock") |
| Label drift when mid leg drops (C relabeled "Leg B", colliding with dropped `legB@test`) | Labels by prefs index `'A'+i` (gaps persist); dropped section cross-references same label | Truncation subtest extended: `Leg A`/`Leg C`/`- Leg B (legB@test):` present, `Leg B (legC@` absent |

**Round 2** (3 findings):

- **F1:** new `TestDesignMoaResponseWireKeys` — marshals a Response carrying both field groups, asserts `design_lock`, `design_proposals`, `proposals` all present + proposal entries match spec shape `{model, text, request_sha16, request_bytes}` (catches wire-level conflicts struct assertions can't).
- **F2:** single-leg-truncation subtest migrated into `TestDesignMoaTruncationFailClosed` (full matrix: single-leg degrade / all-legs-fail / consolidator truncation); adjudication recorded in test doc comment; reverse pin added: degraded pass MUST NOT emit failure marker.
- **F3 (real gap):** `fail()` gained extras merge; all four failure sites (all-legs-fail, consolidator error, truncation, empty lock) now attach `proposals` (model/error/truncated/receipts; no text on truncated legs) and `consolidator` receipt block to the `design/failed` marker. `designConsolidatorReceipt` helper shared by success line and marker (no second convention). Three subtests re-pinned with wire-exact sha16 recompute; also pinned: no `consolidator` key when consolidator never ran.
- **Same-class audit:** pre-fanout failure sites (flag off, empty goal, resolveProject, no review models, ctx escape) issue no MoA calls → no receipts to lose; correctly left alone.

## Verification

Round 1: build/vet ✓, `TestDesignMoa` 6/6, full `internal/ipc` + `internal/moa` green (382s/0.4s).
Round 2: build/vet ✓, `TestDesignMoa` 7/7 (new wire-key pin + 3 extended failure subtests), full suites green (401s/0.46s).

## Open loops

- R-W4 changes remain uncommitted in worktree `6a807575-1016b373e065`; landing via auto-land/accept-diff is pending and was explicitly out of session scope.
- An earlier auto-land attempt was blocked (`needs_fixes`, reason `repair_prompt_too_large`); whether the pipeline successfully lands the now-fixed diff is unresolved in this log.