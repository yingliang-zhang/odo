# Restart Recovery & Daemon Hardening

- Daemon restart used to re-fire verify+panel for every pending diff (≤10min verify + 4–24min panel each); fixed by `recoverPendingDiffs`+`strandedPendingDiffs`+`pipelineTerminalDiffIDs` classifier (UI-epoch-9)
- Terminal rows (skip re-fire): `auto_land_blocked` reason≠panel_infra, `moa_review{actor:auto_panel}`, `auto_revise_round`; non-terminal (still rescued): `auto_land_started`/`refresh_attempted` breadcrumbs, `panel_infra` (re-fire is its only retry channel), human moa_review (UI-epoch-9)
- Journal read failure during classification → fail-closed: abandon whole recovery pass, never burn money on uncertainty (UI-epoch-9)
- Dedup self-evidenced live: boot log 'recover-pending-diffs: 1/1 pending diffs already adjudicated — skipping their re-fire' (UI-epoch-10, UI-epoch-11)
- Orphan sweep runs FIRST in `NewServer` recovery: user_messages lacking agent_done/agent_error get `agent_error{cause:daemon_restart}` closure; control rows (steer/park/loop) skipped; self-excluding → idempotent (bug-fix-epoch-4)
- SIGQUIT immunity chosen over graceful-TERM-on-QUIT: `srv.Wait` would hang shutdown up to ~24min on in-flight panels; `signal.Notify` + logged discard; SIGABRT retained for dumps; 22:05 /panel incident root cause (external SIGQUIT, sender unattributable) (bug-fix-epoch-4)
- Panel progress (`panelProg`) is memory-only (never journaled); `poll_events` handshake copies the map to avoid an encode race; GUI shows '· N/M back' (bug-fix-epoch-4)
- 'resend' chat bubbles are orphan markers stamped when the daemon dies mid-request; they persist in history and do not indicate current failure (UI-epoch-10)
- modernc.org/sqlite WAL `_walIndexRecover` memcpy SIGBUS: recurring hard crash (2× on 08-11, 15:36 kill); candidate one-liner `_pragma mmap_size=0`, then dependency upgrade — unresolved open loop (UI-epoch-10, UI-epoch-11, bug-fix-epoch-4)
- Bare `odo help` can spawn an unintended daemon in the cwd (UI-epoch-6, UI-epoch-5); TERM exit logs 'remove socket: no such file' — pre-existing dup unfixed (bug-fix-epoch-4)
