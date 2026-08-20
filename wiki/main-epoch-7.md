> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Session Summary: #17/#18 Deployment Verification and /panel Routing Investigation

## Deployment of #17 + #18 (completed, verified)

**Decision:** Verify acceptance claim against artifacts, not verbal confirmation; then follow the UI-epoch-6-validated deploy order: build + atomically replace all binaries → kill daemon (app relaunches on new binary) → replace + restart app.

**Final state (all evidence-checked):**
- Main checkout HEAD `a024b77` = accept #17, with #18 landed beneath it; dirty files were wiki/distill artifacts only, so build equaled clean HEAD.
- Binaries installed 12:08 (SHA `12cb51…`); `Projects/odo/odo` and `~/.odo/bin/odo` hashes match.
- `/Applications/Odo.app` matches the 12:09 bundle (SHA `2c0faf…`); app PID 96949 started 12:10:14.
- Main daemon PID 96951 started 12:10:14 (after 12:08 install — correct order); ui-message-stream daemon PID 96929 restarted 12:10:01 via `~/.odo/bin/odo` after its active turn ended.
- Pending diffs: **0** (18 total terminal: 14 accepted / 3 rejected / 1 superseded), queried read-only from `journal.sqlite`.
- #17 registry guard proven live: a daemon mis-launched inside a worktree was rejected with `registry: refusing to register worktree path …6a869fd2`.
- User killed the leftover vite dev process (PID 95226, port 5173); verified: no vite process, port released.

**Incidental findings:**
- `odo status`/`odo diffs` do not exist as CLI subcommands; running them in a worktree cwd fell into the daemon-spawn path and was killed by the 300s timeout; in the main checkout it hits the flock guard and exits 3. Harmless, but a UX trap.
- One in-flight request was lost when the daemon restarted mid-turn (transient, resent by user).

## /panel routing investigation (resolved: no cross-project leak)

**Question:** Did the `/panel` prompt issued in the odo workstream appear in hermes-agent's `main` workstream?

**Conclusion: No.** The `/panel` message landed only in odo project conv1 (this session). Evidence cross-checked across both journals, both daemon logs, and GUI state:

| Source | Finding |
|---|---|
| odo journal | seq 3071 `/panel` user_message (14:45:40) → EOF answers → done |
| hermes journal | seq 1–15 only (the 14:44:35 PR-check run); no `/panel` row |
| odo daemon.log | 14:45:45–50 three moa EOF retries — odo daemon executed the panel |
| hermes daemon.log | 3 startup lines only; never received the request |
| GUI localStorage | `odo-active-project = /Users/yingliangzhang/Projects/odo` |

**Why cross-delivery is impossible:** the bridge connects per `projectRoot` to that project's `.odo/odo.sock` with a fresh connection per call (`lib.rs round_trip`); hermes conv1 had an active run, so a stray send would be rejected as "agent already running" and write nothing; `applyBootstrap` fully replaces the event list on project switch (App.tsx:598), so no bubble residue.

**Root cause of the perception:** both projects have a workstream named `main`, and the TopBar renders only `workstream?.name` (App.tsx:1646) — no project name anywhere on screen except the sidebar highlight. The panel bubble the user saw was in odo's `main` view; timeline shows the user had switched back to odo before 14:44:50.

**Other findings:** the panel's three routes all returned EOF due to `coding.sudoai.cc` network failure, unrelated to routing; and even on success the panel would have run with odo project context (receipt contained odo memory/pins), so reviewing hermes orchestrator design requires resending `/panel` from the hermes view.

## Open loops

- TopBar UX gap: show `project / workstream` when more than one project is registered (one-line change at App.tsx:1646) — awaiting user confirmation to open a diff; not yet implemented.
- hermes PR run (omp PID 4934, started 14:44) showed no new journal events after 14:46:49 while the process was still alive — unchecked whether stalled or finished.
- `/panel` was never successfully executed (provider EOF); the requested review of odo memory/skills/orchestrator design remains unanswered, and a hermes-scoped review would require resending from the hermes view once its active run ends.