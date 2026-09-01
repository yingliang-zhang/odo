> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# UX-2 revise round 1 — JobsChip poll visibility gate + kubectl stderr tail delivery (diff #135 base)

## Context

Panel re-fired on diff #135: K3 accepted; GLM+DSF returned needs_fixes with three findings. Finding #3 (ChipKey union not extended) was **disproven** — `ChipKey` derives from `keyof typeof OVERFLOW_RANK` and the patch already adds `jobs:3` — so it was skipped per instructions. Only findings #1 (visibility-gate the 5s poll) and #2 (deliver the capped stderr tail) were fixed. Worktree base: fresh worktree with diff #135's patch applied (journal lookup: `sqlite3 .odo/journal.sqlite "SELECT path_on_disk FROM diffs WHERE id=135"`).

## Key decisions

- **Poll gating condition**: `polling = !off && !fold.hidden && docVisible`, where `docVisible` is latched via a `visibilitychange` listener mirroring the `App.tsx` `refreshInbox` gate (App.tsx:527-533). Folded (display:none) or backgrounded → interval fully cleared; background windows fork zero kubectl processes.
- **Single effect consolidation**: the original mount-fetch + interval dual effect was replaced by one effect; mount and `projectRoot` switch ride the same true edge. On becoming visible again → immediate one-shot refetch, then the 5s cadence resumes.
- **Comment posture**: JobsChip/JobsSummary headers now state the poll is visibility-gated *because each tick is a kubectl subprocess* — explicitly not copying OmpUsageChip's "stays mounted and polling while display:none'd" posture, which is a journal query (no subprocess exec).
- **stderr capture-time cap**: `cmd.StderrPipe()` + `io.ReadAll(io.LimitReader(pipe, 1024))` — no unbounded `CombinedOutput` buffer, no post-hoc string slicing. A stderr flood only blocks kubectl's write side until the 10s deadline kills it (classified `timeout`); daemon memory unaffected.
- **Protocol**: `K8sStatus.Detail` (`omitempty`) carries the capped tail; `k8sUnavailable(reason, detail)` takes two params — pre-exec failures (`bad_namespace`, `ENOENT`, spawn) pass `""` since no subprocess means no output.
- **GUI display**: popover renders a `.jobs-detail` row below the canned `reasonLabel` sentences — dimmed, monospace, display-capped at ~240 chars via the `capTransportErr` posture. The stale `reasonLabel` comment in `jobs.ts` (claiming stderr stays server-side) was corrected.
- **Out of scope by instruction**: finding #3 not re-argued; the chip `+ batches` D5b deferral remains A2-5 Stage 1, a separate diff.

## Code changes

Daemon:
- `internal/ipc/k8s.go` — stderr pipe + limit reader; two-param `k8sUnavailable`.
- `internal/ipc/protocol.go` — `K8sStatus.Detail` field.
- `internal/ipc/k8s_test.go` — `TestK8sStatusDetailCarriesCappedStderrTail`: fake kubectl emits 3017 bytes of stderr; asserts `Detail` is exactly 1024 bytes, keeps the `ERRDIAG-start` diagnostic head, and the `<TRUNC-END>` tail marker does not survive.

GUI:
- `gui/src/types.ts` — `detail?: string`.
- `gui/src/jobs.ts` — `reasonLabel` stale comment fix.
- `gui/src/components/StatusBar.tsx` — JobsChip gating logic, detail state, `.jobs-detail` popover row, rewritten comments.
- `gui/src/dev/fixtures.ts`, `gui/src/dev/mock-invoke.ts` — mock k8sStatus call counter (`k8sStatusFixture.calls`).
- `gui/e2e/statusbar.spec.ts` — new fold-drill assertion: viewport shrunk to 420px folds the chip (rank ctx=0 → omp=1 → jobs=3); mock k8sStatus call count shows zero growth within 6s after fold, then grows within 2s after restoring 1440px (proves one-shot transition refetch). 2 new specs.

This round is ~+166 lines on top of diff #135. Total staged worktree: 24 files, +1721/−17, zero commits, HEAD still at `4f1beca`.

## Verification

| Gate | Result |
|---|---|
| gofmt / `go build` / `go vet` | clean |
| `go test ./internal/ipc/ -run 'K8s'` | ok 8.5s — 10 tests, incl. new detail test |
| `tsc --noEmit` | exit 0 |
| vitest | 39 files, 458/458 |
| Playwright statusbar specs (after killing pre-existing :1420 process per task instructions) | 6/6, incl. 2 new specs |
| Stability runs | `--retries=0 --repeat-each=2` → 12/12; 18/18 across three runs |

Supporting checks: no shell `min-width` exists that would defeat the 420px fold drill; Playwright 1.62.1 supports `expect.poll`; `node_modules` APFS-cloned into the worktree per the worktree skill. One transient edit mistake (`protocol.go` struct opening lines dropped) was caught and fixed within the session; all gates ran after. Dev server on :1420 stopped after e2e; port released.

## Open loops

- **Land decision**: worktree left dirty and staged (24 files, +1721/−17, zero commits, HEAD `4f1beca`) — awaiting panel re-review / auto-land of this revise round (prior verdicts were `needs_fixes`: `auto_land_blocked` panel_infra, `moa_review` needs_fixes).
- **A2-5 Stage 1**: the chip `+ batches` D5b deferral remains a separate, unaddressed diff.