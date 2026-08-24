# Epoch-31 Rollout & Post-Review Fixes (odo)

## Rollout completed (diff #45 incident fix)

- Binaries atomically replaced at 19:17:02 at both install paths; embedded `vcs.revision=e9466e8` (HEAD, includes accepted diff #45). HEAD chain: `e9466e8 docs(wiki): distill main/epoch 31` ← `aec33e1 odo: accept diff #45`.
- Project daemon: old PID 768 terminated; new PID 95171 (19:17:22, on-demand), later superseded by GUI-spawned PID 99598 after app restart.
- Odo.app: Tauri cold build produced new bundle; user restarted app at 19:21. Installed `odo-gui` SHA-256 matches new bundle; GUI PID 99597 replaces old 688.
- Socket health verified: `odo journal tail 1` exit 0 with real data.
- Deviation from plan: hermes daemon 1243 (planned to keep) was a child of the old GUI and died on app quit; its sessions were lost. New hermes PID 99609 runs the new binary.

## Follow-up review: 5 verified findings (user-supplied, all confirmed against source)

| # | Priority | Finding | Root cause |
|---|---|---|---|
| 1 | P0 | Symlink injection into AGENTS.md | `internal/ipc/server.go:740` `generateAgentsMD()` reads project `memory.md`/`pins.md` directly |
| 2 | P1 | /preview external-redirect bypass | `internal/ipc/preview.go:433` — precheck (HEAD) and screenshot (GET) are separate requests; 204-to-HEAD + 302-to-GET bypasses without a race |
| 3 | P1 | Staged-only edits silently overwritten | `internal/git/git.go:351` compares working tree only; `server.go:3022` `git add` clobbers a divergent index |
| 4 | P2 | Resize grip unusable | `gui/src/components/ContextPanel.tsx:147` grip at `z-0` painted under header/body; Playwright cases fail, width pinned at 380px |
| 5 | P2 | Keep-alive tabs re-render all visited panels | `gui/src/App.tsx:2098` `hidden` skips layout/paint but not reconciliation; `App.tsx:405` writes fresh aggregate objects periodically |

## Fix decisions & changes (3 parallel lanes, 17 files, worktree HEAD = main HEAD = e9466e8)

- **P0 (Go server):** reads go through `readWithinDir` (reuse prior containment helper; escape → absent semantics); writes add `Lstat` symlink refusal (daemon-owned files).
- **P1 (staged-only):** new `git.IndexEditsBeyondHEAD` (real index vs HEAD stage-0 compare); unified refusal before adjudication reusing the refusal-message family; diff stays pending.
- **P1 (/preview):** in-process loopback-only filtering proxy, env-injected into the Playwright child; off-loopback requests logged and denied before dial; `--proxy-server` fallback; doc boundary clause updated; verified with real Chromium (out-of-bounds rejected before dial, control group renders via proxy).
- **P2 (resize):** grip `z-0` → `z-20`, hit area extended 4px right; `px-2` header contract preserved.
- **P2 (keep-alive):** six panels → `export default memo(...)` (ChatSurface pattern); App aggregate `setState` gets prev-bail comparator (reuse `diff_stable`); two inline callbacks → `useCallback`.

## Verification so far

- `go test ./internal/git/...` all green (independent re-run).
- `vitest` 158/158 passed (independent re-run).
- Worktree contains exactly the 17 files declared by the three lanes; `node_modules` symlink untracked.

## Open loops

- Go full test suite still running in background at transcript cutoff — result pending.
- Playwright resize spec re-run to confirm grip fix (was in flight at cutoff).
- The 17-file fix set is uncommitted in the worktree; apply/commit to main still pending.
- Known low-priority watchers remain: `switch-cache:64` mock race and mock state-flip parity gap (recorded, unfixed).