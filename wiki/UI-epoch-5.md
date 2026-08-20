> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Deploy & Diff 9 Fix Round (2026-08-19)

Session arc: deploy current main to `/Applications`, decide diffs 9/10, hand over daemon, then fix diff 9 per panel feedback.

## Decisions

- **Diff 9 (steer queue, +1311/−74, 14 files) — REJECTED.** Panel returned `needs_fixes` on 3 real defects; auto-fix exhausted the 85KB > 64KB size cap. Sent to a manual fix round instead.
- **Diff 10 (IME blue-box + verify-gate, +122/−3, 5 files) — ACCEPTED.** Gates: go 382s, vitest 57/57, e2e 102/102; blocked only on `protected_path` (by design). Landed as main `7cd0848 odo: accept diff #10`.
- **Daemon handover without nohup**: verified `ensure_daemon_running` in `gui/src-tauri/src/lib.rs` is idempotent — after `kill <pid>`, the app auto-relaunches the daemon from `<project>/odo` on next command failure; flock single-instance + exit-3 attach semantics, no race.
- **`~/.odo/bin/odo` upgraded in place**: it was a stale 8/12 copy (used to bootstrap daemons for projects lacking a local binary); atomically replaced via cp-to-temp → mv, sha verified.

## Code changes

### Deploy (verified)
| Artifact | Version | Evidence |
|---|---|---|
| main HEAD | `7cd0848` (diffs 8+10 in, diff 9 rejected) | git log |
| `/Applications/Odo.app` | rebuilt 19:49 from `7cd0848` | sha256 `8a9a59eb…` both ends; dist contains `compositionend` (IME fix) marker |
| `<project>/odo` daemon | rebuilt 19:49, 17.3MB | sha `7c21f50e…`; contains `auto_land_started` |
| `~/.odo/bin/odo` | same sha `7c21f50e…` | atomic replace |
| Running daemon | PID 71468, started 19:56 by app after `kill 27946` | log `listening on …/odo.sock`; journal event id 4496, continuous |

Note: Mach-O binary grep shows 0 hits for GUI markers because embedded assets are compressed — dist markers + hashes are the verification source. On startup the sweeper reclaimed one stale worktree (`6a8585eb`), by-design cleanup.

### Diff 9 fix round — worktree `6a859a11`, base `7cd0848`
| Panel finding | Fix | Pin |
|---|---|---|
| Ghost queued steers after daemon restart (undeletable) | Restore `queuedSteers` (with seq) from journal on startup; drop-recovery rejects stale entries | 2 new Go pins (recovery verified red-then-green by removing the NewServer hook) |
| Drop two-stage confirm reset by poll heartbeat | SteerQueue preserves confirm/disarm state across derived recomputation from poll ticks | e2e `drop confirm survives poll-tick re-derivation` |
| Mixed go+gui diff ran only go verify | Verify-command selection now runs all applicable `.odo-verify` matches (provisioning reuses diff 10 mechanism); changed both call sites (autoLand and loop); `.odo-verify` config updated | autoland_test subcases |

**Verification matrix** (all green): `go test ./internal/ipc/` 405s; vitest 80/80 (75 baseline + 5 new; one mid-round red was a test expectation typo, fixed); `tsc --noEmit` clean; playwright 106/106 (102 baseline + 4 from new `steer-queue.spec.ts`; vite port 1420 confirmed owned by this worktree, not a zombie).

**Result**: queued as **diff #11, `pending`**, bound to worktree `6a859a11`, visible in app review inbox.

### Process incident (self-cleaned)
A bare `odo` CLI probe accidentally spawned a daemon inside the session worktree; killed by 300s timeout, leftover sqlite deleted (self-created, self-removed). Main daemon 71468 and app unaffected.

## Open loops

- Diff #11 (steer-queue fixes) awaits manual Accept in the app — auto-land blocked it on `protected_path` (`internal/ipc/autoland.go` touched), by design; panel scope = the three fixes above.
- ui-message-stream project daemon (PID 71283) still runs the pre-update binary — out of scope; picks up the new binary on its next natural restart.