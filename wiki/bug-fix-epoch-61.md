# Quad-audit follow-up: GUI fork/subagent affordances + run_command process-group kill

## Context
- Quad audit of `1db418f..7ff83cf` (4/4 ACCEPT_WITH_NITS) left three items; this session implemented all three in one pipeline diff (19 files, +822/−19).
- Item 1 (P2): #152 GUI affordances never landed — `fork_conversation` was socket-only, `spawn_subagent` CLI-only; `gui/src` had zero fork/subagent references.
- Item 2 (P2): `run_command` killed only the `sh -c` wrapper on timeout; grandchildren (dev servers, watchers) survived, held pipe write-ends open, and wedged IPC past the 600s clamp.
- Item 3 (P3): `gui/src/commands.ts` folded a missing `exit_code` as `0` (green) — corrupt rows failed invisible.

## Key decisions
- **Item 2** mirrors the in-tree precedent at `internal/ipc/preview.go:816` exactly — `SysProcAttr{Setpgid: true}`, `cmd.Cancel` killing the negative pid, `cmd.WaitDelay` (1s) — rather than inventing a new mechanism (`internal/adapter/omp.go:329` was the second precedent).
- **RunsPanel subagent rows** open diffs through the panel's existing pending-diff flow; no new viewer built.
- Subagent row derivation is a pure function `subagentRows(events)` in `gui/src/lib/runs.ts`, folding lifecycle events by `subagent_id` — unit-tested like the other runs derivations.
- Fork affordance lives on USER message bubbles (natural branch point): hover-visible GitFork icon (lucide-react) beside the existing copy button (`bubble-copy` slot pattern), disabled while the agent runs (same guard as the DX Retry button in RunsPanel).
- Daemon-side IPC from #152 reused as-is — no daemon logic re-implemented; the diff is GUI + the `commands.go` kill fix only.
- Fresh worktree: `node_modules` APFS-cloned from the main checkout (project convention); the worktree's own dev server (port :1420 ownership verified, hub-managed) served e2e.
- Constraints honored: `.odo-verify` untouched, panel keep-alive contract untouched, one pipeline diff.
- Self-review fix: removed an awkward `as string` cast in the fork click handler.

## Code changes
- `internal/ipc/commands.go` — `handleRunCommand`: process-group setup + group kill on timeout + `WaitDelay`; new test spawns a command with a sleeping background child under a short timeout and asserts both processes die and the handler returns (no wedge).
- `gui/src/commands.ts` — `exit_code: p.exit_code ?? 1` (was `?? 0`) so malformed rows fail visible.
- `gui/src-tauri/src/lib.rs` — `fork_conversation` Tauri bridge mirroring the `send_message` bridge (pass-through to daemon socket).
- `gui/src/lib/api.ts` — `forkConversation(conversationId, fromSeq)` wrapper; `gui/src/types.ts` — fork response shape `{ ok, new_conversation_id, new_worktree_path }`.
- `gui/src/components/MessageBubble.tsx` — fork button: click → `fork_conversation` with the bubble's event seq (threaded through the events prop), transient "forking…" state, on success switches to the new conversation via App.tsx's existing conversation-switch path.
- `gui/src/components/RunsPanel.tsx` — nested subagent rows under the parent run: indented, "└ sub:" prefix, goal truncated to 60 chars, status (running/done/failed), "view diff" link when the subagent produced a diff; slot constants introduced.
- `gui/src/dev/fixtures.ts` + `mock-invoke.ts` — subagent lifecycle events + fork responses so the GUI renders daemon-less.
- Tests: vitest suites (fork button hover/disabled-while-running/click-with-correct-seq; RunsPanel nested rows; `subagentRows` pure tests) and a Playwright spec against the mock fixtures (fork button visible on user bubbles; subagent row renders under parent run), written after verifying the panel auto-open latch behavior.

## Verification
- Go: build/vet/test green, including the new process-group kill test (package run 6.2s; full suite green in 11.5 min); one gofmt fix applied.
- tsc clean (after node_modules APFS clone in the fresh worktree).
- vitest: runs derivation suite 29/29, component suites green, full run 575/575 green.
- Playwright: targeted runs spec 7/7 green first pass; **full Playwright run still in flight when the session ended**.

## Open loops
- Full Playwright suite result unconfirmed — the run was still executing at session end; must land green before this wave is done.
- Cargo gate (covers the `lib.rs` Tauri bridge change) was kicked off alongside Go ("go/cargo") but its outcome was never reported.
- contrib.test ≤9 static gate: constraint respected in scope, but no final confirmation output was observed in-session.
- Final delivery report (files + counts, per the done definition) not yet posted to the user.
- Worktree dev server (hub-managed, port 1420) teardown after Playwright finishes not yet done.