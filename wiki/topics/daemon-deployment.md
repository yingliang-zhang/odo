# Daemon Deployment & Lifecycle

- Deployment rule: atomically replace the binary first (cp-to-temp + mv at both <project>/odo and ~/.odo/bin/odo), then kill the daemon — killing first lets ensure_daemon_running resurrect the old binary (main-epoch-21)
- Stale daemons silently misgovern: #36 was auto-rejected under the abolished one-vote-veto rule because the running binary predated current policy; live schema_version, diffs.goal and vcs.revision are the behavioral proof of which code runs (main-epoch-21)
- diff52's daemon-side fixes were inert until a day-old stale binary (shared sha at both install paths) was rebuilt from HEAD and atomically replaced — the same incident class as the #36 misjudgment (main-epoch-41)
- Landing source ≠ landing behavior: after an accept, running daemons and the GUI still execute old code until redeploy + restart (main-epoch-31)
- Post-restart verification is evidence-based: new PID, vcs.revision == HEAD at both binary locations, socket health via journal tail, per-project stores unaffected; pgrep false negatives are corrected with full ps (main-epoch-33)
- Graceful daemon exit SIGKILLs in-flight agents, so restarts sequence through the GUI daemon_restart path or armed, setsid-isolated resurrect scripts (main-epoch-41)
- hermes-agent daemon restarts are a standing user decision — restarting kills its sessions; it shares ~/.odo/bin/odo, so swaps leave it on the old image until restarted (main-epoch-23)
- Disk hygiene: Rust target/ + node_modules dominated 6.6GB of the dev directory; both are gitignored and rebuildable — archive tracked source via git archive HEAD, not product copies (main-epoch-27)
