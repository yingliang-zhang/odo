> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Session Summary — Advisory Fix Review, Deploy, `/panel` Incident, Three Hardening Fixes

## Context

Continuation of odo GUI/daemon work. Covers: re-implementation of reviewed advisory-slash fixes (diff #14), Odo.app redeploy, root-cause analysis of two unresponsive `/panel` commands, and implementation of the three resulting hardening fixes (uncommitted).

## Key decisions

- **Whitespace parity claim downgraded from "bug" to "verified-correct-but-unpinned."** Daemon routing (`server.go:744-774`) uses `strings.HasPrefix(rest, " ")` — literal space only. `/panel⏎…`, tab, `/ panel` fall through on both daemon and GUI sides. Fix = exact source citation in comments + 17 daemon-mirror unit cases to lock parity, not a behavior change.
- **Late advisory rejection must not clobber a newer draft.** Restore in `ChatSurface.submitDraft` made conditional: only restore draft/attachments/`slashToken` if `textarea.value` and `attachmentsLiveRef` show the composer untouched; otherwise keep user content and show only the error banner.
- **Advisory submits preserve one-shot park arm.** Daemon routes slash commands *before* the steer/park branches, so park is never queue-consumed by advisories; GUI uses `clearComposer({ keepPark: true })`.
- **`panelThinking` boolean → counter** (`setPanelThinking(n±1)`, render `panelThinking > 0`) so concurrent advisories share the spinner correctly; last one out turns it off.
- **SIGQUIT immunity over graceful-TERM-on-QUIT.** `srv.Wait` would hang shutdown on in-flight panels up to ~24 min, losing advisories anyway. `signal.Notify` + discard; SIGABRT retained for deliberate dumps.
- **Orphan sweep placed first in `NewServer` recovery sequence** (short-circuits before any boot recovery writes new expectation rows). Recognition is pure field-based (settle.go `originGoal` precedent): steer/park/`/loop` control rows carry no terminal expectation and are skipped; all other user_messages lacking `agent_done`/`agent_error` get a `agent_error{cause:daemon_restart}` closure. Self-excluding on next fold → idempotent.
- **Panel progress is memory-only** (`panelProg`, `previewEvent` precedent, never journaled); `poll_events` handshake copies the map to avoid an encode race.

## Code changes

### Diff #14 — advisory fixes (landed `f1dd14d`, 7 files), deployed
- `ChatSurface`: guarded restore; `keepPark`; `slashToken` re-highlight on restore; error banner path preserved.
- `App`: `panelThinking` counter + `> 0` render.
- `gui/src/slash.test.ts`: +17 daemon-mirror parity cases (tab/newline/NBSP/case/edge spaces).
- Test infra: `releaseAdvisorySends` latch semantics (`released` + `releaseError`) killing the release-before-registration race; QueueDock row assertions via `.queue-chip` popover (matches `parked-goals.spec.ts`).
- New advisory-slash e2e (5 cases: detach non-freeze, fast-reject restore+park, late-reject no-overwrite, late-reject empty-box restore, post-advisory park enqueue).
- **Deploy**: rebuilt from main @ `f1dd14d` (`tauri:build` 29.7s), kill PID 93950 → `ditto` replace `/Applications/Odo.app` → new PID 508, SHA-256 verified matching. Daemon untouched (pure GUI diff).

### Incident root causes (two `/panel` commands, "no response")
- **22:05 command**: daemon received external **SIGQUIT** ~22:07:25 (`handlePanelQuery` shown `WaitGroup.Wait, 2 minutes` in dump), exited; auto-restarted 22:08:10. Journal holds only the `user_message`, no answer ever. No code path in repo (Go/Rust/TS) sends SIGQUIT; sender unattributable from available logs.
- **22:48 command**: not dead — daemon (PID 93445) alive, WAL writes through 22:52 (legs journaling). Prior successful panel took 6 min; leg timeout ~24 min. Just slow.

### Three hardening fixes (uncommitted, 10 files)
1. **`main.go::installSIGQUITImmunity`** — `signal.Notify` replaces runtime default; each delivery logged then discarded.
2. **`server.go::deriveOrphanedRequest` + `recoverOrphanedRequests`** — boot-time sweep at head of recovery sequence; appends `agent_error{cause:daemon_restart}` for unanswered non-control user_messages; GUI renders a failure bubble instead of hanging.
3. **Panel progress heartbeat** — daemon `panelProg`: fan-out registers Total, each leg (incl. failures) increments Done, last-finishing panel deletes entry. GUI spinner shows `· N/M back`, lit independently by progress (visible even if GUI reopened mid-advisory); concurrent panels share the table with render-side `Math.min` clamp.

## Verification

| Check | Result |
|---|---|
| Diff #14: tsc / vitest / advisory e2e / full e2e | clean / 97/97 / 5/5 / 111/111 |
| Hardening: `TestSIGQUITImmunity`, `TestDeriveOrphanedRequest` (11 cases), `TestOrphanedRequestClosedOnDaemonRestart`, `TestPanelProgressHeartbeat` | pass |
| Hardening full: go test / tsc / vitest / e2e | all green / clean / 97 / **112/112** |
| Smoke (scratch project, new daemon binary): `kill -QUIT` → survives, logs immunity line, socket serves; `kill -TERM` → graceful `bye` | pass |

Housekeeping: `package-lock.json` rewritten by npm install — reverted; not part of the change.

## Incidental findings (not acted on)

- daemon.log 8/11: two `modernc.org/sqlite` WAL `_walIndexRecover` memcpy **SIGBUS** hard crashes (Go 1.26.5 era) — latent, not reproduced today.
- TERM exit logs `remove socket: no such file` — pre-existing dup between Go unix-listener auto-unlink and explicit `os.Remove`; untouched.

## Open loops

- Deploy the three hardening fixes (daemon binary rebuild + replace + Odo.app reinstall — broader than diff #14): offered, awaiting user go-ahead.
- Confirm the 22:48 `/panel` eventually answered (was still in flight at investigation time; no re-send needed).
- SIGQUIT sender for the 22:07 daemon kill unidentified — no actionable evidence in repo, omp session logs, or system logs.
- modernc.org/sqlite SIGBUS crashes (8/11) unaddressed — candidate for dependency upgrade / follow-up issue.
- `remove socket: no such file` on TERM exit — pre-existing minor dup, unfixed.