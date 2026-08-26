# Daemon Deployment & Staleness Doctrine

- Deploy order is mandatory: atomically replace binaries (cp-to-temp + mv) at BOTH `<project>/odo` and `~/.odo/bin/odo` BEFORE killing the daemon — otherwise `ensure_daemon_running` resurrects the old image; verify via `go version -m` vcs.revision == HEAD + socket health (UI-epoch-6)
- Daemon-side fixes are inert until restart: stale binaries repeatedly misjudged live traffic (diff #36 auto-rejected under an abolished one-vote-veto; epoch-41 daemon from the prior day couldn't enforce diff 52's heal semantics) — restart verification must prove pid, revision, and socket together (main-epoch-21)
- Stale-daemon incident repeated the same class: running daemon (pid 71154) was yesterday's binary while diff 52's core fixes were daemon-side; fixed by rebuild from HEAD + atomic replace + GUI-restart-driven daemon respawn (main-epoch-41)
- Deployment staleness witness: if the binary mtime is >5min older than HEAD commit time, log a WARNING (log-only, no gate) (main-epoch-14)
- Post-restart `pgrep` false negatives once misdiagnosed a live daemon as dead; use full `ps` cross-check plus journal WAL activity and orchestration lineage (harness PPID) as liveness evidence (main-epoch-33)
- GUI full restart is the reliable restart path; graceful-exit SIGKILLs in-flight agents by design (`main.go:193`) — expect one lost in-flight request per restart (main-epoch-41)
- The only unexpected daemon death class observed: modernc.org/sqlite SIGBUS in WAL-recovery mmap (recurring, low-frequency); remedies: dependency upgrade or `_pragma mmap_size=0` (UI-epoch-10)
- Running `odo status`/`odo diffs` (nonexistent subcommands) or bare `odo`/`odo help` in a worktree cwd falls into the daemon-spawn path and can hang bootstrap — a known UX trap, unfixed (main-epoch-7)
- tmp git repos and sibling git worktrees were auto-registered as phantom projects whose dead-socket polling respawned daemons; `isLinkedGitWorktree` registry guard refuses `.git` files pointing at `/.git/worktrees/` (submodules allowed); removal must use sidebar remove-project once since the guard doesn't clean existing rows (main-epoch-6)
- Deploy verification must check artifacts, not claims: commit HEAD == accept commit, both binaries hash-equal, `/Applications/Odo.app` == build bundle SHA, daemon pid started after install time (main-epoch-7)
