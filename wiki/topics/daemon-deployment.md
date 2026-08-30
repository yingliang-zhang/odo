# Daemon Deployment & Runtime Operations

- Deployment rule: build in the main checkout, cp-to-temp plus mv atomic replace at BOTH <project>/odo and ~/.odo/bin/odo, THEN kill the daemon - ensure_daemon_running would otherwise resurrect the old binary; the mv inode swap is safe for a running process. (main-epoch-21)
- Stale daemons misjudge diffs as a recurring incident class: an old binary ran abolished one-vote-veto rules, lacked schema v3, and could not enforce new daemon-side fixes (same incident class struck twice) - check binary revision before diagnosing panel or gate behavior. (main-epoch-41)
- Verify deployments behaviorally, not by process liveness: go version -m vcs.revision at both install paths equals HEAD, the socket answers (odo journal tail), and the new schema/columns are live in the running process; the installed Odo.app odo-gui SHA-256 must match the Tauri build bundle. (main-epoch-33)
- Serving produces no per-request log lines - log silence is not death; use full ps (pgrep gives false negatives) and PPID orchestration evidence (the user message's harness PPID equals the daemon PID) as the strongest live proof. (main-epoch-33)
- Graceful exit SIGKILLs in-flight agents, so restarts go through the GUI (a full app restart respawns the daemon as a sidecar; the project daemon and hermes daemon both refresh to the new binary, per-project stores unaffected). (main-epoch-41)
- Restarting the shared hermes-agent daemon kills its sessions - a standing user decision pending since epoch-23 (same binary, per-project stores, no divergence urgency). (main-epoch-23)
- Deploy-staleness witness: binary mtime more than 5 minutes older than HEAD triggers a WARNING (log-only). (main-epoch-14)
- Rust build output dominates disk: gui/src-tauri/target held 6.3GB of 6.6GB - deleting target/ and node_modules (gitignored, rebuildable via npm --prefix gui ci plus cargo) shrank the project to 128MB; archive tracked source via git archive HEAD. (main-epoch-27)
- IPC write failure in the Tauri bridge is terminal (no retry) - retrying risks duplicate daemon execution. (main-epoch-30)
