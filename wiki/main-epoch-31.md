# Diff #45: Fix, Verify, and Accept of Blocked Diff

## Context
- `auto_land` blocked diff #45 with `verify_failed` (seq 11484); user asked whether to accept anyway.
- Verdict after investigation: blocking was correct **before** the fix; safe to accept **after**. User accepted (seq 11643).

## Root cause of the verify failure
- Full e2e run: 119 specs, single deterministic failure at `review-inbox.spec.ts:78` (`reject path mirrors accept`): `getByText("Diff #1")` expected 0, got 1, stuck for the full 5s timeout (12/12 polls) — not a load flake.
- Mechanism: the keep-alive fix (review finding #5) keeps hidden Changes panels mounted in the DOM; `ListDiffs` (`store/diffs.go:99`) has no status filter, so resolved diffs intentionally still render as record cards titled `Diff #1`. The test's page-global `getByText` matched the hidden panel and could never reach 0. Product behavior correct; the test assertion was over-broad.
- This closes the epoch-30 open loop about the same test/line recurring — confirmed keep-alive-related, product side sound.

## Code changes
- `review-inbox.spec.ts:57` and `:77` (accept + reject paths): scoped the two global assertions to the `.review-inbox` container, with a comment documenting the scoping rule. Repo-wide grep confirmed this was the only spec with the pattern.
- Mock left untouched — verified it already matches daemon semantics (inbox = pending filter, poll returns all).
- Patch regenerated pre-panel (epoch-22 precedent; no panel attestation bound): `git add -A && git diff --cached HEAD` → `.odo/diffs/6a8c171e-…diff`, 30 files, +2104/−221.

## Verification
- Spec solo: 4/4 green; `--repeat-each=3`: 12/12.
- Full e2e: 118 passed + 1 flaky (`switch-cache:64` passed on first retry; known epoch-28 mock race, unrelated).

## Accept confirmation (four-way evidence)
- Store: diff #45 `status=accepted`; `base_sha f3536ef` matches commit parent.
- Commit: `aec33e1 odo: accept diff #45` on main HEAD, 30 files +2104/−221 — byte-identical to regenerated patch.
- Worktree lifecycle: staging tree `6a8c171e-…` reclaimed; current tree `6a8c24b5-…` detached at `aec33e1`, clean.

## Stale runtime artifacts (restart needed)
Accept landed source only; three running artifacts predate it:
| Artifact | PID / started | Image | Missing from diff #45 |
|---|---|---|---|
| Project daemon `/Projects/odo/odo` | 768 / 16:24 | 14:36 build | P0 symlink containment (11 files), `alreadyLanded` byte-compare, `/preview` redirect validation, `read_file` race |
| Odo.app GUI | 688 / 16:24 | built 08-20 15:17 | keep-alive 6 panels, ChatSurface memo, MemoryPanel nonce, `lib.rs` IPC write-failure terminal state |
| hermes daemon (`~/.odo/bin/odo`) | 1243 | same 14:36 | same Go-side; restart kills its sessions |

Planned order (per wiki deployment doctrine):
1. Go binary: build in main checkout → cp-to-temp + `mv` atomic replace at both `<project>/odo` and `~/.odo/bin/odo` → kill PID 768 (order matters: `ensure_daemon_running` would resurrect with the old binary). Verify via `go version -m` vcs.revision + socket health. `mv` inode swap is safe for the running hermes daemon.
2. Odo.app: `npm run tauri:build` (cold build — `target/` cleaned in epoch-27, minutes) → `ditto` replace `/Applications/Odo.app` → restart. Verify installed vs bundle SHA-256.
3. hermes daemon: left running unless user directs otherwise.

## Open loops
- Restart/deployment not yet executed — awaiting user go-ahead to run step 1 (Go binary atomic replace + daemon 768 restart) then step 2 (Odo.app cold build + replace).
- hermes daemon (PID 1243) restart decision still pending since epoch-23 — restarting would kill its sessions; untouched unless user approves.
- Observation item: `switch-cache:64` mock timing race — still open, low priority; root cause in the mock, not product (epoch-28 classification).
- Observation item: mock parity gap — status doesn't flip in the active poll after resolve; no assertions depend on it, recorded but unfixed.