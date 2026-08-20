> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# M19 GUI Wave — /loop DESIGN-LOCK Items (V1, V13, V11)

## Context & key decisions

Goal: implement the three remaining DESIGN-LOCK GUI items for `/loop` as a GUI-only diff. Daemon contract (commit `042ab4b`, "odo: accept diff #3") treated as authoritative — no Go files touched; the daemon fold was mirrored, not modified.

Daemon fold semantics recovered from `internal/ipc/loop_journal.go` and mirrored exactly:

- `loop_started` carries `loop_id: 0` — the loop's ID is its journal **seq**.
- `loop_budget_exceeded` folds to `status=suspended, cause=budget_exceeded`.
- `review_action{accept|auto_land_blocked, actor:"auto_loop"}` closes fix phases (same-session attribution).
- `notifiedKinds` dedups notification receipts; first journaled `notified` row for a terminal kind prevents re-fires.
- Terminal kinds: `loop_completed | loop_stopped | loop_suspended | loop_budget_exceeded`.
- `loop_completed` carries `rounds` (not `round`); findings count key is `findings_count`.

Design choices:

- **V1**: `deriveLoopStates` as a pure TS mirror of the Go fold; `LoopChip` styled after `AutoDistillChip`, re-deriving purely from the event stream (loops continue daemon-side while GUI is closed). `loop_event` rows render as one compact bookkeeping bubble, never agent text, and survive the distill fold filter.
- **V13**: slash registry entries gain a `description` field; five `/loop` subcommand entries (audit|tasks|status|stop|resume); "/" at composer start opens the full list immediately; Up/Down + Tab/Enter + Esc keyboard model; accepted command renders as a background-pill token overlay (overlay `z-0`, textarea `z-10`, caret survives). Menu-open condition widened from `!val.includes(" ")` to a regex allowing `cmd` + one subcommand word; render gated on non-empty items.
- **V11**: `@tauri-apps/plugin-notification` was already installed and registered (M3) — no new dependency; only wiring: `notifyLoopTerminal` helper + App-level derive watcher firing once per terminal kind, then journaling the receipt via `loop_ctl {action:"notified"}`. Honest limit kept: daemon never talks to the OS; firing requires the GUI open.
- **Esc safety** (16 prior regressions): `.slash-menu` class registered in App.tsx's window-level Esc gate chain (same pattern as `.bg-runs-menu`) **and** `e.stopPropagation()` in the React `onKeyDown` Escape handler — both layers.

## Code changes (13 files, staged in worktree)

| File | Change |
|---|---|
| `gui/src/loop.ts` | **New.** TS mirror of Go `deriveLoopStates`/`foldLoopRow`; terminalKinds first-sight tracking; `phase`/`loopMode` helpers |
| `gui/src/loop.test.ts` | **New.** Table-driven vitest suite (20 tests): started→rounds→verdicts→suspended/completed/stopped, notifiedKinds dedup |
| `gui/src/types.ts` | `loop_event` event union member + payload keys; compact system-bubble render case + fold filters |
| `gui/src/components/LoopChip.tsx` | **New.** Mode, phase (seeding/auditing round N/fixing/suspended: cause/stopped/completed), round N/maxRounds (pref `loop_max_rounds`, default 10, display only), spent tokens; stop/resume → `loop_ctl` |
| `gui/src/components/ChatSurface.tsx` | LoopChip integration; V13 registry + descriptions + Tab accept + token overlay |
| `gui/src/components/MessageBubble.tsx` | Compact bookkeeping bubble case for `loop_event` rows |
| `gui/src/App.tsx` | `.slash-menu` in Esc gate chain; `loopStates` derivation; notify watcher journaled via `loop_ctl notified`; ChatSurface prop pass |
| `gui/src/notify.ts` | `notifyLoopTerminal` + `__odoLoopNotify` e2e seam; platform-gated lazy import (§3b posture, exception named) |
| `gui/src/api.ts` | `loopCtl` client for cmd `loop_ctl` |
| `gui/src-tauri/src/lib.rs` | Rust bridge command for `loop_ctl` |
| `gui/src/dev/fixtures.ts`, `gui/src/dev/mock-invoke.ts` | Fixture/mock wiring for the loop e2e seams |
| `gui/e2e/loop.spec.ts` | **New.** 7 playwright tests: autocomplete immediate full list + descriptions + keyboard nav + Esc-without-cancel; chip phase/round/spent + stop; notify mocked-fire + no re-fire with receipt |

## Verification state

- `npx tsc --noEmit` — clean. `cargo check --manifest-path gui/src-tauri/Cargo.toml` — clean. Full vitest suite — green.
- `e2e/loop.spec.ts` first run: 4 passed / 3 failed. Root causes found and fixed: (1) menu open condition killed two-word commands; (2) `pickSlash` parked caret before the placeholder — `/loop tasks` registry args set to `""`, test types the space; (3) V11 fixture needed audit-round rows journaled (`rounds.length` is fold-truth). Fixed spec re-run passed; final full-suite e2e run was started but its result was cut off by session end.
- **The diff did not land.** Auto-land was blocked: `auto_land_blocked, reason: verify_failed` (seq 1439). Verified against the repo: `main` HEAD is still `042ab4b`; the work survives **staged but uncommitted** in worktree `/Users/yingliangzhang/Projects/odo/.odo/worktrees/6a8514d2-d2401f305d79`.

Prior fail-open signal in the same stretch: repeated `auto_land_blocked, reason: protected_path` on `internal/ipc/autoland.go` (invariant 1: agents never write memory) before diff #3 was accepted — unrelated to this wave but the second recent auto-land friction point.

## Open loops

- Auto-land verify gate failure (`verify_failed`) is undiagnosed — the failed gate's logs were not surfaced in the transcript; determine which check failed (tsc/playwright gate or the truncated full e2e run) before re-landing.
- M19 GUI diff needs review + manual land from worktree `.odo/worktrees/6a8514d2-d2401f305d79` (staged changes only, base `042ab4b`); confirm the full e2e suite (not just `loop.spec.ts`) is green at land time.
- `/loop audit base=a97bd3d` was attempted, suspended (`subject_too_large`), and stopped by the user — the intended audit of the daemon contract diff never ran; if still wanted, restart with a narrowed subject.