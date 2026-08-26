# P3 Memory Hygiene Fixes: Heal-Row Window Exclusion + Paged Journal Replay

Date: 2026-08-26. Base: main `ff2f121`. Diff lives **uncommitted** in odo worktree `.odo/worktrees/6a8eba13-b8e39f721ac4` (6 files, +568/−143). Main untouched as of writing.

## Decisions

1. **Render/eligibility split is load-bearing.** Two predicates now exist and "must not reconverge" (server.go:6814 comment):
   - `foldExcludedMemoryUpdate` (render, **unchanged**, server.go:6801) — excludes only auto_distill scheduler bookkeeping (`scheduled`/`skipped`/`cap_suspended_until`).
   - `windowExcludedMemoryUpdate` (eligibility, **new**, server.go:6843) — everything the render predicate excludes **plus** the boot-recovery family `recover` / `heal_merged` / `heal_conflict` / `heal_resolved`. Heal-row match has *no* layer check: they journal under the healed memory layer, never `auto_distill`, so the render predicate never matched them anyway.
   - Rationale: heal rows are **outcome rows** (same class as `fired`/`failed`) — real memory history that must keep rendering in the distill prompt. But they are boot/recovery bookkeeping for eligibility, and `heal_conflict` embeds KB-sized `stranded_body` payloads that inflated `window_bytes`.
2. **No unbounded full-list API.** `ListProjectEvents` was deleted outright, not deprecated. `ListProjectEventsPage(ctx, projectID, afterID, limit)` (store/events.go:112) is the only listing — keyset pagination `WHERE e.id > ? ORDER BY e.id ASC LIMIT ?`; `limit <= 0` clamps to `1<<30` (uncapped fold escape hatch for tests).
3. **Page size = 512** — `const replayJournalReadPage = 512` (memory_replay.go:107), with comment justifying the round-trip-vs-memory tradeoff.
4. **Streaming reduce across pages.** `replayMemoryJournal` keeps one `laneMemReceiptFold` per lane (propose→apply pairing + newest-per-layer candidate) carrying all order-dependent state across page boundaries; `afterID` advances per page; loop exits on a short page.
5. **Test page-size override via existing ForTest convention**: `Server.replayJournalPageSizeForTest` (server.go:429–433), never set in production.
6. **Scope honored**: GUI untouched; no cap numbers/eligibility thresholds changed (only what counts); no landWG/auto-land changes; no docs/attestation files.

## Code changes

| File | Change |
|---|---|
| `internal/store/events.go` | `ListProjectEvents` → `ListProjectEventsPage` (keyset cursor); doc keeps the archived-lane/coverage-boundary note verbatim. |
| `internal/ipc/memory_replay.go` | `replayJournalReadPage = 512`; `replayMemoryJournal` refactored into a paged streaming fold; per-lane fold state extracted for page-boundary carry. |
| `internal/ipc/server.go` | New `windowExcludedMemoryUpdate`; `foldExcludedMemoryUpdate` doc updated; `replayJournalPageSizeForTest` field. |
| `internal/ipc/auto.go` | `measureWindow` now calls `windowExcludedMemoryUpdate`; header comments restate the render/eligibility split. |
| `internal/ipc/auto_cap_test.go` | `TestHealRowsExcludedFromWindow` (all four heal causes → `window_events=0 window_bytes=0 below_min_events`, incl. KB `stranded_body`) and `TestHealRowsStillRenderInDistillPrompt` (heal rows render byte-identically; scheduler rows stay excluded). |
| `internal/ipc/memory_replay_test.go` | Test helper `projectHeals` cut over to `ListProjectEventsPage` (pages of 3–4 rows, so every existing heal-drill also exercises page boundaries); new `TestReplayJournalPagingEquivalence` — identical synthetic multi-lane journal folded at different page sizes (down to 2-row pages, receipts/heal rows straddling ≥3 pages), asserting byte-identical journaled outcomes, projections, and re-boot idempotence. |

## Verification

- `go build ./...` green; targeted suites (`memory_replay`, `auto`) green after the refactor, before new tests were added — semantics preserved.
- All four new/changed tests pass; full `go test ./internal/...` exit=0 with `-timeout 25m` (first run hit the default 10m panic at exactly 601s — environmental, suite takes ~615s here; rerun showed no panic, no failed test).
- `gofmt -l internal/` empty.

## Open loops

- **Land the diff**: uncommitted in worktree `6a8eba13-b8e39f721ac4`; needs the odo accept/land step onto main (`ff2f121`).
- **Duplicate `rows.Scan` defect in the worktree**: `ListProjectEventsPage` (store/events.go:132–136) calls `rows.Scan` **twice** per row with identical destinations — dead code left over from the mid-task edit repair (first copy carries the stale error message `"list project events: scan"` without `page`). Harmless at runtime, MUST be removed before landing.
- **Suite exceeds default test timeout**: full `go test ./internal/...` ≈ 615s > the 10m default in this environment; gate commands/CI need an explicit `-timeout`.