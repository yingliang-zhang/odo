# Deployment & Daemon Lifecycle

- Deploy rule: replace binaries first (atomic cp-to-temp + mv of both `<project>/odo` and `~/.odo/bin/odo`), then kill the daemon — killing first relaunches the old binary; the Tauri app auto-respawns the daemon from `<project>/odo` (UI-epoch-6)
- Stale-binary forensics: `go version -m` vcs.revision against main HEAD plus behavioral proof (`schema_version=3`, `diffs.goal` column); epoch-21 proved a live daemon believed rebuilt was actually #27-era code with schema v2, causing #36's wrongful one-vote-veto rejection (main-epoch-21)
- Auto-reject under the abolished one-vote-veto rule showed a stale binary silently mis-adjudicates; after any rule change, restart verification (new PID, socket, schema) is the acceptance gate (main-epoch-21)
- Boot recovery sequence: orphan sweep runs first (unanswered non-control user_messages get `agent_error{cause:daemon_restart}` closure, idempotent on next fold), then pending-diff recovery; SIGQUIT is discarded via `signal.Notify` because graceful TERM-on-QUIT would hang shutdown on in-flight panels up to ~24 min (bug-fix-epoch-4)
- modernc.org/sqlite WAL-recovery SIGBUS is a recurring low-frequency daemon kill; first remedy is one-line `_pragma mmap_size=0`, upgrade the dependency only if that fails (UI-epoch-11)
- "daemon restarted while this request was in flight" bubbles are orphan markers stamped on in-flight requests at daemon death; they persist in history and do not indicate current failure (UI-epoch-10)
- Bare `odo` CLI runs can spawn unintended daemons inside worktrees; external `/bin/kill` may be needed where the shell builtin refuses PIDs (UI-epoch-6)
- The hermes-agent project daemon (~/.odo/bin/odo, its own store) is decoupled from odo's daemon lifecycle; restarting it kills its sessions and remains a pending user decision (main-epoch-23)
