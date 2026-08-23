# Daemon Binary Deployment & Recovery

- Deploy rule: atomic cp-to-temp+mv of `<project>/odo` AND `~/.odo/bin/odo` first, THEN kill daemon — killing first makes the app relaunch the old binary (main-epoch-21, UI-epoch-6, UI-epoch-10)
- Prove new code is live behaviorally: `go version -m` on the running process (vcs.revision) + DB checks (schema_version=3, `diffs.goal` column) — epoch-20's 'new binary =954ff22' claim was false; live daemon was 5ec522bd+dirty until the explicit rebuild (main-epoch-21, main-epoch-20)
- Orphaned commit recovery: #34 (`a5c98ca`, 8 P1 fixes) and schema-v3 anchor fix `abca6f1` sat on a deleted session worktree's detached HEAD; cherry-picked into main (→4345f24→954ff22), journal correction seq 8470 corrects seq 8017's false manual-land claim (main-epoch-20)
- Installed-app verification method: SHA-256 of `/Applications/Odo.app` vs bundle artifact + running pid's `txt` mapping + main HEAD — definitively answers 'is the app latest?' without rebuild (UI-epoch-3)
- App→daemon handover: `ensure_daemon_running` is idempotent — kill daemon and app auto-relaunches from `<project>/odo`; flock single-instance; daemon survives app exit (UI-epoch-5, UI-epoch-6)
- Mach-O binary grep is unreliable for GUI markers (Tauri compresses embedded resources); verify via `gui/dist` source markers + SHA parity instead (UI-epoch-6, UI-epoch-5)
- Manual dev worktrees must live outside `.odo/worktrees/` — the daemon sweeper reclaims unbound directories (UI-epoch-1)
- Sweepers auto-reclaim stale worktrees/prompts at boot; hermes-agent's pending-diff store is decoupled from odo binary swaps and daemon lifecycle (UI-epoch-11, UI-epoch-6)
