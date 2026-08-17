# Pipeline chip (auto-revise ladder) — revision loop summary

## Context
- Feature: StatusBar pipeline chip in `gui/` surfacing per-diff auto-revise pipeline state, derived journal-only. No daemon/`internal/` changes, no new IPC.
- Two auto-revise rounds; final review outcome: **accept** after two rejects.

## Daemon truth established (settle.go / settle_test.go)
- Round rows: `auto_revise_round{round, diff_id, origin_diff_id}`; round 1 has `diff_id == origin_diff_id`, repair products get increasing ids; blocked rows carry only the evaluated `diff_id`.
- Suspension: `memory_update{layer:auto_land, cause:ladder_suspended}` marker precedes `blocked{ladder_suspended}` (settle_test.go order 391→394).
- Original diff **stays pending** after a repair lands ("superseded, human-decidable", settle_test.go:584).
- Conversation scoping is guaranteed by protocol, not in-function filtering: bootstrap batch-replaces per conversation, poll filters by `conversationId`, daemon `ladderState` (settle.go:251) scans per-conversation.
- Shortcuts use `Meta+` suite-wide; the Linux-CI `Meta+,` concern was rejected — one convention, runner passes.

## Key decisions
1. **Journal-only derivation, zero latch**; chip timer governs only the ≤4s landed-flash window.
2. **Chain-root mapping** `diff_id → origin_diff_id` (isomorphic to settle.go:593-598): blocked/moa/accept rows propagate to every pending diff in the chain (DSF-1).
3. **Suspended posture uniformly overrides** all per-diff branches; only accepts with seq after the marker flash.
4. Derivation takes a **`now` param**: expired flashes are never produced, restoring "`length > 0` ⇒ visible" as the single visibility gate (ds#3).
5. Badge click fixed **type-level**: `onBadgeClick: (tab: PanelTab) => void`, `PanelTab` single-sourced in ContextPanel; second string union deleted — tsc enforces all callsites (glm#1).
6. Mock settings: `get_settings` returns a **shallow copy** (reference return + in-place mutate caused React `Object.is` bail-out — pref toggles never took effect); `update_settings` merges; `autonomy_status` mirrors prefs.
7. Fixtures rewritten to proven daemon shapes; new id family 8→9→10, blocked on 11; ledger counts unchanged (11/8/6/2). Accept rows pre-aged against boot flash (`ev()` derives timestamps from id).
8. glm#3 disproved: LedgerPanel.tsx:171 sorts by `seq`, not `created_at`.
9. Landed-flash scan switched to **newest-first** with `Number.isNaN` guards (oldest-first let stale rows override new accepts).
10. Lockfile hygiene: round 1 `npm install` pruned optional entries → reverted; round 2 used `npm ci`, untouched.

## Code changes (all under `gui/`)
- NEW `src/pipeline.ts` — `derivePipelineStates` (actor filter, chain-root map, suspended override, `now` param, residual-limitation comment).
- `StatusBar.tsx` (`PipelineChip`, placement between PanelChip and diff badge), `App.tsx` (memo + plumbing), `types.ts` (`origin_diff_id`, `PanelTab` typing), `app.css` (existing tokens only).
- `fixtures.ts` (daemon-truth journals), `mock-invoke.ts` (settings round-trip).
- NEW `e2e/pipeline-chip.spec.ts` — 9 tests incl. chain propagation, suspension override + resume, conversation-scope non-leak, pref-off round-trip; `e2e/ledger.spec.ts` updated.

## Verification
| Command | Result |
|---|---|
| `npx tsc --noEmit` | clean, both rounds |
| `pipeline-chip.spec.ts` | 9/9 |
| grep set (StatusBar, sidebar, background-runs, wave-b) | 21/21 |
| Full playwright suite | 83/83 (~2.5m) |

## Open loops
- Documented residual limitation (annotated in `pipeline.ts`): in the window between a repair product's arrival and the next `auto_revise_round` row, chain attribution relies on round rows being written; within that window the chip shows each diff's own latest attribution, with blocked priority as the floor for the dominant chain state.