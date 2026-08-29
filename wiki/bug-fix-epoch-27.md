# Odo Adoption Lock: P1 Pipeline Fixes, Re-apply, and P2 Implementation (Blocked)

## Key decisions

- **Grounded-leg infra semantics (`grounded.go`)**: tool-loop exhaustion now marks a review leg `Infra=true` regardless of posture — `rr.Infra = plan.required || loopExhausted`. Rationale: a burned-out tool loop is not direction evidence (P1 diff #101); `panelInfraLeg` then parks the round blocked-pending (recoverable) instead of counting a synthesized `needs_fixes` as real dissent.
- **Settle repair cap (`settle.go`)**: `settleDiffCapBytes` raised 64KB → 128KB with a rationale comment — the cap prevents truncation-hallucination, it does not size-limit scope. Comments cap stays 16KB; revise rounds stay 2.
- **Test contracts pinned**: caps asserted as `2 / 128K / 16K`; fixture resized to 6200 pad lines (~148.8KB, must exceed 128K to trigger `repair_prompt_too_large`); grounded loop-exhaustion test asserts `if !rr.Infra`.
- **P2.3 taxonomy**: replace single socket-down banner with typed overlay keyed by classification (socket closed / heartbeat timeout / version mismatch / verify infra / panel infra), each with title, one-line cause, leading action (Reconnect / Copy diagnostics / Open journal); extends P1 `errors.ts` summarizer map rather than forking it.
- **P2.4 LRU park**: ContextPanel keeps max ~3 tabs mounted (active + 2 MRU); older tabs unmount to state handles; Memory/Wiki editors with unsaved drafts are park-exempt; restoring a parked tab remounts and refetches (keep-alive contract).
- All new interactive elements use the P1 `slots.ts` data-slot contract (update slots.ts, never inline selectors).

## Landed changes

- **Commit `16f4c95`** — settle cap 128KB + grounded infra semantics (`internal/ipc/grounded.go`, `settle.go` + tests). Full suite evidence: `/tmp/ipc-full-fix.log` → `ok github.com/.../internal/ipc 510.406s / EXIT:0`.
- **Commit `169ca02`** — P1 adoption diff re-applied onto `16f4c95`: archived patch (94,727 B, 26 files, `gui/**` only) applied via `git apply --3way`, zero conflicts (settle/grounded touched only `internal/ipc/`). Features: journal search (`searchEvents` → `search_events` wire), central `slots.ts`, keybind registry + ⌘/ ShortcutsPanel, tool-result inline diff with "N files changed" chip → Changes tab, ordered error summarizer map. Gates: `tsc` exit 0; vitest 24 files / 262 tests pass; Playwright 137 pass.

## Dispatch history (P1 fix verification)

1. First verify dispatch found the fix absent from the assigned worktree (clean, HEAD `23b3f46`) — it lived uncommitted in the main checkout; gates ran against the main tree and the mismatch was reported.
2. Second dispatch reproduced it in the worktree from `stash@{0}`: `cherry-pick --no-commit` failed (stash is a merge node); fallback `git stash apply --index stash@{0}` applied all 4 files cleanly. Build + focused tests (`TestGroundedBudget`, `TestSettleRepairPrompt*`, `TestGateDiffRequiresGroundedLeg`) passed; worktree left dirty.

## P2 implementation (P2.1–P2.4, per `docs/design/adoption-lock.md`)

Work split: 4 leaf modules delegated to subagents (PreviewLeaf three-tier previews, RunsLeaf journal fold, taxonomy overlay, `lru.ts` park state machine); integration owner did `App.tsx` surgery, `MemoryPanel`/`WikiBrowser` `onDraftChange` draft signals, CSS, and 4 new e2e specs. tsc and vitest went green; **Playwright ran 146/150 with 4 failures**. Root-cause work was in progress on the PreviewFilePane effect (preview stuck in "Loading" state) when the session ended.

## Current status

- P2 diff is **not landed**: `auto_land_blocked`, `reason: verify_failed` (seq 13516).
- `stash@{0}` remains in the main repo's stash list (applied, not popped).

## Open loops

- P2 auto-land blocked on `verify_failed`: 4 Playwright failures (146/150) need root-cause fixes — investigation had narrowed to the PreviewFilePane fetch effect leaving the Preview tab stuck on "Loading".
- P2.1–P2.4 code is in the worktree uncommitted; requires a fix pass and full three-gate re-run (tsc / vitest / playwright) before re-submission to the pipeline.
- P3 of the adoption lock not started (explicitly deferred to a later diff).
- Stale `stash@{0}` (the P1 infra fix) still sits in the main checkout's stash list; candidate for cleanup once no longer needed as a recovery path.