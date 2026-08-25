# Daemon Deployment & Binary Currency

- Deployment rule: atomically replace the binary (cp-to-temp + mv, permissions preserved) at BOTH <project>/odo and ~/.odo/bin/odo BEFORE killing the daemon — ensure_daemon_running resurrects whatever binary is on disk (main-epoch-31) (UI-epoch-6)
- Binary verification stack: go version -m vcs.revision, sha parity across both install paths, socket health (nc -U / odo journal tail), and behavioral proof such as schema_version=3 with the diffs.goal column present (main-epoch-21) (main-epoch-33)
- Stale-binary incident: an epoch claimed a fresh daemon but the live process ran #27-era code (schema v2, no goal column), causing diff #36's wrongful auto-reject under already-abolished one-vote-veto rules (main-epoch-21)
- Log silence ≠ death: serving produces no per-request log lines; the strongest liveness evidence is the harness process's PPID pointing at the project daemon (main-epoch-33)
- GUI deploy: npm run tauri:build (cold build after target/ cleanup takes minutes) → ditto replace /Applications/Odo.app → verify installed sha256 matches the bundle; Mach-O string grep is unreliable because Tauri compresses embedded assets, so verify via gui/dist source markers (UI-epoch-5) (UI-epoch-6)
- GUI-only diffs skip daemon redeploy entirely: rebuild and swap only the Tauri app when Go source is untouched (UI-epoch-7)
- App install verification method: compare installed binary sha256 against target/release/bundle artifact, check running pid's txt mapping, confirm main HEAD — answers "is the installed app latest?" without rebuilding (UI-epoch-3)
- hermes-agent daemon restart was deferred across many epochs because it kills its sessions; it eventually landed on the current binary via an atomic swap, with its per-project store untouched by odo's daemon lifecycle (main-epoch-31) (main-epoch-33) (UI-epoch-11)
- A bare `odo`/`odo help` CLI run can spawn an unintended stray daemon; a stale vite dev server from a fix worktree must also be killed during cleanup (UI-epoch-5) (UI-epoch-6)
