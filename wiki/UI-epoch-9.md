# Dedup Daemon-Restart Recovery of Pending Diffs (Plan A)

## Problem

After every daemon restart, the GUI showed diff panels firing again for already-reviewed diffs. Root cause: `NewServer`'s recovery routine `recover-pending-diffs` (`internal/ipc/server.go:289–323`) called `ListAllPendingDiffs` (project-wide, all workstreams) and unconditionally re-fired `maybeAutoLand` for every pending diff — no dedup against existing `review_action` journal rows.

Why it actually ran (arming conditions in `~/.odo/prefs.md`):
- `review: t9s/kimi-k3,glm-5.2,deepseek-v4-flash` → panel armed (`autoland.go:255`).
- `auto_apply: main` → M20 default; `maybeAutoLand` only exits silently on explicit `off`.

Cost: every restart re-ran verify (≤10 min) + panel (measured ~4–24 min) per unresolved diff. Idempotent (final accept gated by `acceptMu` + base freshness), but pure waste — e.g. diff #21 already awaiting reject was re-verified and re-paneled. Restart was likely to pick up the epoch-7 16k token-cap fix, which lit the recovery loop.

## Decision

User picked **Plan A**: before recovering a diff, check whether the pipeline already has a terminal verdict for it; skip those, rescue only genuinely stranded diffs. (Rejected: Plan B, mere advisory requeueing — stranded diffs would never auto-recover; Plan C, status quo.)

## Changes (+266/−37 across 3 files)

| File | Change |
|---|---|
| `internal/ipc/autoland.go` | New `recoverPendingDiffs` (inline closure from server.go moved in, method-ized), `strandedPendingDiffs` (store filter), `pipelineTerminalDiffIDs` (pure classifier); header journal-contract comment gained a dedup-classification section |
| `internal/ipc/server.go` | Recovery closure → `go s.recoverPendingDiffs(ctx)`; 42 lines of comment+closure collapsed to 6 |
| `internal/ipc/autoland_test.go` | Two new tests |

### Dedup rules (classifier: terminal row ⇒ skip)

- `auto_land_blocked` with reason ≠ `panel_infra` — any landed conclusion (panel_mixed, verify failure, ladder stop, …).
- `moa_review{actor:auto_panel}` — pre-land evidence row; a lost land race leaves pending + already-judged.
- `auto_revise_round` — ladder has taken over; awaiting round-land supersede.

### Explicit non-terminal (still re-fired = genuinely stranded)

- `auto_land_started` / `refresh_attempted` breadcrumbs — restart mid-pipeline is exactly the shape to rescue.
- `panel_infra` — by design: infra failure is not a verdict; restart re-fire is its only retry channel (per `panelInfraLeg` comment in `autoLand`; preserved).
- Human `moa_review` (no actor) — pipeline never ran for that diff.
- Journal read failure → fail-closed: abandon whole recovery pass and log; never burn money on uncertainty.

## Verification

- `TestPipelineTerminalDiffIDs` — 16-payload classification matrix incl. garbage payloads, `diff_id:0`, crumb/infra/human exclusion pins.
- `TestStrandedPendingDiffs` — real store, 2 workstreams × 7 pending diffs; asserts survivors `[d2,d3,d5,d7]`, cross-conversation isolation, order preservation.
- Full-repo regression: green except two **pre-existing** reds — `TestGetSettings` (server_test.go:2357/2379, `auto_apply` off→main default flip) and `TestPhantomDiffVerdictBlocksAutoLand` (runverdict_test.go:314, prefs missing `review:` line). Signatures match the epoch-10 note: both are #24 leftover stale tests riding with #22's snapshot; not folded into this diff to avoid double-carry rebase conflicts.
- Panel outcome on this change: `auto_land_blocked`, actor `auto_panel`, reason `protected_path` — blocked, awaiting human decision.

## Deployment note

Fix requires rebuild + daemon restart — and that restart itself triggers one final old-logic full re-fire (#21/#22 burn one more panel round). Rejecting #21 in the GUI before restarting saves a round.

## Open loops

- This diff blocked by panel (`protected_path`) — human accept/reject decision pending.
- Rebuild + restart daemon to activate the fix; reject #21 in GUI first to avoid one wasted panel round.
- Two pre-existing red tests (`TestGetSettings`, `TestPhantomDiffVerdictBlocksAutoLand`) still unfixed on main; carried by #22's snapshot per epoch-10 decision.