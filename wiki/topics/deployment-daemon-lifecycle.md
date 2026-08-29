# Deployment & Daemon Lifecycle

- Deployment discipline is write-then-kill: build in the main checkout, atomically cp-to-temp + mv replace at BOTH <project>/odo and ~/.odo/bin/odo, then kill — reversed order lets ensure_daemon_running resurrect the old binary (main-epoch-31, main-epoch-41)
- Liveness verification is behavioral: go version -m vcs.revision equals HEAD at both install paths, three-way SHA match, and the socket answers odo journal tail (main-epoch-41)
- Stale daemon binaries causally misjudge governance: a #27-era daemon auto-rejected diff #36 under the abolished one-vote veto, and diff #52's daemon-side fix semantics were inert until the rebuild+restart (main-epoch-21, main-epoch-41)
- Gate-side code changes take effect only after a restart on the new binary — registration-time refusals, heal semantics, and guards are inert while an old daemon runs (main-epoch-24, main-epoch-41)
- Graceful-exit SIGKILLs in-flight agent requests; a GUI full restart is the real restart path (the setsid-isolated resurrect script armed for the #52 deploy never needed to fire); hermes-agent daemon restarts kill its sessions and remain an explicit user decision (main-epoch-41, main-epoch-31)
- Daemon health evidence: log silence is not death (serving emits no per-request lines) — confirm via PID parentage (harness PPID), socket probes, and binary shas; a pgrep-only check once produced false 'dead daemon' negatives (main-epoch-33)
- SQLite hardening: mmap disabled and synchronous(FULL) on both read-write and read-only open paths as the journaled SIGBUS therapy (RO shares the WAL's SIGBUS class); recurrence remains under observation since root cause is unproven (main-epoch-15, UI-epoch-11)
- Doctrine rollout checklists land source first, then rebuild all three layers (two daemons + /Applications/Odo.app via Tauri cold build + ditto) in one trip to avoid double rebuilds; hermes-agent's data stores are decoupled from binary swaps (main-epoch-12, UI-epoch-11)
