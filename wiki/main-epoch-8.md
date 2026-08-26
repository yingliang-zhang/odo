> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Session: UI fixes, slash-consult visibility, diff-20 settlement, M20 auto-land overhaul

## Landed + deployed: TopBar project name & sidebar create-input fixes (`ab70237`)

- **`TopBar.tsx`**: new `projectName` prop; brand renders `Odo · <project> · <workstream>` with 200px truncation (`.topbar-project` reuses the workstream truncation style). `App.tsx` resolves it via `projects.find(p => p.root === activeProjectRoot)?.name`, falling back to daemon `project.name`.
- **`Sidebar.tsx` root cause of stuck/leaking input**: `creating` was a single boolean; render guard `isActive && creating` made the box follow whatever project became active, and Esc was the only cancel. Fixes: `onBlur → resetCreate` (click-away dismiss) + `useEffect` on `activeProjectRoot` (input destroyed same-frame on project switch).
- **Verified**: `tsc --noEmit`, vitest 97/97, browser-drive (`Odo · odo · main` in TopBar, blur dismiss, no cross-project leak), e2e `sidebar.spec.ts` +2 regression tests (10/10). Accepted UX tradeoff: first click outside an open input only dismisses it (layout shift eats the click), second click switches — covered by e2e.
- **Deploy chain executed end-to-end**: worktree commit → FF merge to main → `npm run tauri:build` in main checkout → `ditto` replace `/Applications/Odo.app` → app restart. GUI-only change: daemon binary untouched (sha `12cb5178…` identical between `Projects/odo/odo` and `~/.odo/bin/odo`); daemon auto-respawned by the app via `ensure_daemon_running` (~16s after launch).
- **Incident**: the worktree copy of these edits vanished pre-commit; recovered from the journal `diffs` table (pending-diff capture), re-applied, verified, then committed.

## Landed, NOT deployed: slash-command activity visibility (`7ec13a3`)

Design rule: reuse existing channels — zero new RPC, zero journal changes.

| Layer | Change | Effect |
|---|---|---|
| daemon `server.go` | conversations with `slashing[conv] > 0` count into `running_workstreams` (display-only) | sidebar blue dot (fg) / purple dot (bg), StatusBar bg chip, "still running" activity row all light up via existing logic |
| daemon `protocol.go`/`server.go` | `PanelProgress.legs[]`: model names registered at fan-out in prefs order; done/error set per leg; poll path **deep-copies** (shallow Legs slice = encoding race, known trap) | per-leg progress via `poll_events` |
| GUI `ChatSurface.tsx` | spinner row → per-leg status card (keeps original text + e2e hooks): `✓ back` / `✗ error` / consulting | legs visible landing one by one |

- **Verified**: `go vet` + full `go test ./internal/ipc` (428s); new pins `TestPanelProgressHeartbeat` (extended: leg names/order/count) and `TestPendingCountsSlashConsult` (consult ⇒ running, finish ⇒ cleared); `tsc`/vitest 97/97; e2e `advisory-slash.spec.ts` 6/6 (first test extended to per-leg assertions); browser-drive (card 3 states, blue dot `dot-accent pulse`, card clears with tally, answer bubble lands).
- **Scope notes**: `/vision` & `/preview` get the blue dot but no card (not panelProg). During consult the activity line may show the conversation's last tool event (reuses `fgRunLabel`).
- Merge: `--ff-only` `ab70237..7ec13a3` (6 files, 242+/11−). **Incident**: `git stash pop` without `--index` dropped the index → first commit contained only `server.go`; `reset --soft` redone.

## Diff-20 zombie settlement (manual, event seq 4094)

- **Cause**: `auto_land_started` (seq 3896) → `auto_land_blocked` at verify gate (seq 3996); user then directed manual commit + FF merge. The pipeline only recognizes its own `git apply`, so "content already in main" was invisible to it — no reconcile channel → diff hung pending forever, also pinning worktree `6a86ad00` via WorktreeRefs hold.
- **Decision**: neither accept (mechanically impossible: base stale → `checkAndRefreshBase` re-applies → fails → rollback → diff stays pending + misleading journal row) nor reject (semantics inverted; pollutes autonomy reject stats).
- **Action**: one `BEGIN IMMEDIATE` transaction (daemon online, `busy_timeout`, `UNIQUE(conversation_id, seq)` race-safe): `UPDATE diffs SET status='accepted' WHERE id=20` + `review_action` event `{action:accept, diff_id:20, base_sha:ab70237…, head_sha:7ec13a3…, note:"landed externally as 7ec13a3 (manual commit + ff merge)"}` — payload same shape as daemon accept path; `actor` empty = human convention; extra keys ignored by consumers per ADR-0002. Verified accept side-effects were no-ops (no suspended ladder rows, no `auto_revise_round` chain; risk keys omitted per unreadable-patch precedent).
- **Result**: diff 20 accepted, diff 21 untouched, `6a86ad00` hold released (sweeper collects next cycle), GUI counts recompute from DB next poll — no daemon restart needed.

## Panel behavior baseline (answering "no response")

`/panel` runs 3 MoA legs (kimi-k3 / glm-5.2 / deepseek-v4-flash) synchronously, ≤16 tool-loop rounds each, and journals a single merged answer only after all legs finish → minute-level chat silence is by design. HTTP timeout floor 900s + output buoyancy (worst ~24 min/request), ×3 retries. `panel_progress` is in-memory, pushed via `poll_events`. Answers are raw per-leg concatenation — no consolidator (pre-existing wiki open item); a failed leg surfaces as a per-leg error line.

## M20 in flight: root-fix + removal of the human-review gate (uncommitted)

**Root fix (diff-20 class)**: reverse-apply probe in `checkAndRefreshBase` — `git apply --reverse --check` succeeding ⟺ post-image already in main → settle `already_landed` instead of a pending zombie.

**De-human-review cut points** (panel unanimity gate + revise ladder + majority valve replace the human gate):

| Old outcome | New outcome |
|---|---|
| normal diff → pending awaiting human accept (pref manual arm) | armed by default; `off` remains as kill-switch; no review models ⇒ unarmed, silent skip (models-first check; `no_review_models` journal block deleted) |
| panel unanimous/mixed reject → forever pending | auto-reject (`actor=auto_panel`, evidence row) |
| base_stale conflict → pending | auto-revise at current HEAD via ladder (chain supersede cleans old diff) |
| ladder suspended → only human accept resumes | resumes on any accept landing |
| GUI accept/reject buttons | kept — triage/override valve, no longer required path |
| policy blocks (protected_path / supply_chain / verify / infra) | stay pending — anomalies, not review |

**Implementation notes**:
- git layer: new reverse-check primitive execs `git apply --quiet` directly — helper `run` swallows `ExitError`, so exit-1 can't be distinguished through it.
- `handleDiffAction` accept: 3-state adjudication; fresh-base path probes reverse-check **before** any apply (read-only) — rollback would clobber uncommitted user edits on patch paths (pre-existing I7 hazard, rescue-after-rollback would be too late). Untracked new files are invisible to `git diff HEAD -- <path>` → always stage first, then judge commit necessity.
- `settle.go`: `settleRevise` refactored → shared `settleDraft`; new `settleBaseStale` + `settleRebasePrompt`; `startReviseRun` gains a trigger param; M20 amendment notes in file headers.
- Tests: autoland pref/models contracts rewritten; two P0a tests rewritten for auto-reject/auto-revise; +2 already-landed tests; settle_test 3 rewrites + flagship B-class full self-heal loop; git-layer tests (an edit accidentally deleted the `CleansUp` header — repaired).
- **Status**: mid-run the worktree was wiped (detached HEAD rsynced to `7ec13a3`, tree clean); recovered **byte-identical** (12 files spot-checked) from the diff-23 capture (21 files, 1519 insertions). A race in the flagship self-heal test fixture was found on full-suite re-run and fixed (×10 stress green). Full `./internal/ipc` suite re-run was kicked off at session end — result not yet observed.
- Working tree now = diff-23 content + race fix ⇒ diff-23 capture is stale. Diff 23 itself was blocked twice by the auto panel with `protected_path` (pipeline code is a protected path — by design pending for human).

## Conventions & gotchas recorded

- Wave convention: work stays uncommitted in the worktree until the user says commit/merge.
- Deploy chain (wiki deploy-verification): worktree commit → FF merge to main → `npm run tauri:build` in main checkout (~30–50s hot target) → `ditto` → restart app. GUI-only ⇒ no daemon restart; daemon Go changes ⇒ rebuild daemon too.
- bash tool `cwd` parameter is silently ignored — reported via `report_issue`; workaround: explicit `cd … &&` prefixes.
- vitest must be run with the explicit `gui/` directory — repo root has no vitest config and mis-collects e2e.
- gofmt: `loop_audit.go` / `loop_journal.go` non-clean at HEAD — pre-existing, deliberately untouched.
- Stray untracked `package-lock.json` at main checkout root (no root `package.json`; likely an accidental root-level npm run) — left alone.

## Open loops

- M20 uncommitted: working tree = diff-23 restore + race fix on top; full `./internal/ipc` suite re-run in flight at session end — confirm green, then commit + FF-merge to main.
- Diff 23 (M20 capture) is stale vs working tree (race fix uncaptured) and blocked on `protected_path` — needs re-capture plus a human-accept path once M20 lands (pipeline code can never auto-land under current policy).
- Diffs 21/22 (loop-A exemption stream) still pending, base stale (`ab70237`), hunks overlap `7ec13a3`'s `handlePollEvents`/`handlePendingCounts` — rebase to current HEAD required before landing.
- Deploy chain for `7ec13a3` (slash visibility) not executed — running daemon binary and Tauri package still old code; awaiting go-ahead.
- Panel consolidator still absent — answers remain raw 3-leg concatenation (pre-existing wiki item).
- Stray untracked `package-lock.json` at main checkout root — cleanup decision pending.