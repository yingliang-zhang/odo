# Session Summary — R-W4: Design-MoA Consolidator + Blind Proposal Legs

## Task
Implement the Design-MoA pipeline (verdict B, `router-vs-omp-eval-2026-08-14.md`; harness audit plan row #9) behind prefs flag `design_via: moa`, following R-W2 (`resolveDistillVia`) and R-W3 (`resolveVia`) migration patterns, with R-W1.5 wire-exact receipts.

## Key decisions
- **Gating**: `design_via: moa` routes through R-W3 `resolveVia`; absent, explicit `omp`, or garbage values all fail closed before any moa call (pinned by `TestDesignMoaPrefsOff` over `""`, `design_via: omp`, `design_via: banana`).
- **Blind legs**: 3 concurrent `moa.QueryWithTools` calls; models from prefs `review:` via existing `parseReviewModels()`; same goal+context prompt per leg; repo-root-scoped FS executor (new `newFSToolExecutorRooted(root)` extracted in `fstools.go`, /panel behavior unchanged); 16-round tool-loop cap retained; per-leg timeout via `moa.TimeoutForModel`.
- **Truncation — stricter than spec text**: a truncated leg is a *failed* leg — its partial text is dropped, never fed to the consolidator, but its receipts (final escalated wire body, 16384→32768) are kept. Pipeline degrades and proceeds on surviving legs; dies only when every leg fails. A truncated *consolidator* fails the command closed with no `design_lock` row (`runMoaOneShot` convention). This resolves the spec's apparent tension ("don't fail unless ALL legs truncate" vs `TestDesignMoaTruncationFailClosed`).
- **Consolidator**: single `moa.Query` (synthesis, not vote), labeled Leg A/B/C inputs; dropped legs are named in the prompt but their text is excluded (test guards against an "invisible vote").
- **Journaling**: one additive `review_action` with `action="design_lock"` carrying consolidated text + per-leg verdict metadata and each leg's `request_sha16`/`request_bytes` (ADR-0002 immune; no new event type). Failures journal `memory_update{layer:"design", cause:"failed"}` per the curate precedent, so long passes never die silently.
- **Concurrency guard**: `designing` single-flight field on `Server`, mirroring the `curating` precedent. Command is project-scoped (`resolveProject`).

## Code changes (all in `internal/ipc/`)
| File | Change |
|---|---|
| `design_moa.go` (new) | `handleDesignMoa`: leg fanout (goroutines + `sync.WaitGroup`) → consolidator → journal |
| `design_moa_test.go` (new, 430 lines) | 4 tests, 6 subcases |
| `protocol.go` (+38) | `CmdDesignMoa`; Request `goal`/`context_files`; Response `design_lock`/`design_proposals`; new `DesignProposal` type |
| `server.go` | dispatch case + `designing` single-flight field |
| `fstools.go` (+9) | `newFSToolExecutorRooted(root)` extraction |

## Verification
- `go build ./...` clean; `go vet ./...` clean; `go test ./internal/ipc/ ./internal/moa/ -count=1` — 4 new tests green (`TestDesignMoaConsolidator` incl. truncated-leg subtest, `TestDesignMoaPrefsOff`, `TestDesignMoaParallelLegs` timing assertion, `TestDesignMoaTruncationFailClosed` both positions).
- Confirmed on disk: files exist in worktree; test names and truncation semantics match grep of actual sources.

## Session events
- Prior panel cycle: `auto_land_blocked` (`repair_prompt_too_large`) → refresh → accept (seq 302); that accept landed as worktree commit `ecaa7ce "odo: accept diff #25"` (curator/learner/ADR-0003 batch — *not* R-W4) and is on `main`.
- R-W4 session closed with `auto_distill` scheduled/fired; no review accept for R-W4 appears in the transcript.

## Open loops
- R-W4 changes are **staged but uncommitted** in worktree `.odo/worktrees/6a805227-bb8879ac659e` (`A design_moa.go`, `A design_moa_test.go`, `M fstools.go/protocol.go/server.go`) — no `odo: accept diff` commit covers them yet, and they are absent from `main` HEAD; landing is still pending.