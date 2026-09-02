# Odo P1 Borrow ⑥⑦ — Turn-fork + Subagent report/isolation (diff #149)

## Context

Implements the last two P1 borrow items from the 2026-08-13 tri-model harness audit (`docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md` item #12; `docs/compare/harness-tri-model-audit-2026-08-13/leg-k3.md`): journal-preserving turn-fork and subagent report + worktree isolation. Two rounds:

1. **Round 1 — implementation**: full daemon + store + GUI + CLI as one pipeline diff. Verify gate: tsc / vitest / playwright green, go segment red (2 real test failures) → auto-land blocked (`verify_failed`), diff archived as #149.
2. **Round 2 — fix round**: applied patch #149 to a fresh worktree, fixed the 2 reported bugs plus 3 latent bugs they exposed, staged everything, stopped (full gate deliberately not re-run, per instruction).

## Key decisions

- **Turn-fork is a journal COPY, not a revert or rollback.** `fork_conversation` reads source events with `seq <= from_seq`, inserts a NEW conversation (`conversations.forked_from`, schema v5), copies events with fresh seq numbers starting at 1 (same type/payload, receipts preserved), and creates a new worktree from the HEAD of the source's last accepted diff (or main). The original conversation is never modified.
- **Subagent diffs are PROPOSALS, never auto-landed.** `spawn_subagent` runs OMP in an isolated worktree (`sub-`-prefixed runID — the security boundary), journals events in the PARENT conversation with an optional `subagent_id` payload field (no schema change), emits `subagent_done` `{subagent_id, goal, diff_path?, exit_code, summary}` (summary = final agent_text, truncated to 2 KB), and stores the extracted diff for the parent to accept (cherry-picked into the parent worktree) or reject; the subagent worktree is removed after the decision. Recursion refused in the handler — one isolation level only.
- **Schema v5 columns are NOT in the `schemaV1` DDL.** Fresh DBs seed at v3 and fall through v4, so a `migrateV5` ALTER would hit duplicate-column errors on fresh installs if the columns were also in the base DDL. `migrateV5` now adds them uniformly for fresh and legacy DBs; three existing version assertions updated 4 → 5.
- **Boot-recovery auto-land excludes subagent diffs**; the recover filter was refactored into a pure, testable helper.
- **Subagent drain strips the adapter's trailing partial preview event** (mirrors `drainRun`) so transients are never journaled.
- Constraints honored: no new dependencies; reuse of `worktree.Manager` / `ExtractDiff` / `EnrichedEnv`; all changes flow through the auto-land pipeline; `.odo-verify` untouched.

## Bug fixes (round 2)

| # | Location | Root cause | Fix |
|---|---|---|---|
| 1 | `internal/ipc/fork_test.go:144` | Reported. Refusal table misused `rig.call` (Fatalf on any refusal), so the seq-0 floor error blew up the first case | Switch to `rig.callExpectErr` (existing convention, `server_test.go:1791`); handler's `from_seq >= 1` pre-validation was already correct and deterministic — unchanged |
| 2 | `internal/ipc/server.go:519` | Reported. `NewServer` initialized `runs`/`byConv` etc. but not `subagents` → nil-map panic at `subagent.go:189` (`s.subagents[runDirID] = &subAgentRun{`); production path would crash too | `subagents: make(map[string]*subAgentRun)` added to the constructor |
| 3 | `internal/ipc/fork.go:64-78` | Latent, exposed by fix 1. Store-level refusal (from_seq above maxSeq) fired after the handler had already created an empty `main-fork-1` worktree lane; the store transaction rolls back only the conversation row | Upper-bound pre-check (`ListEvents` maxSeq under `s.mu`, exactly matching the store check) before any lane creation; refusal text aligned with store semantics |
| 4 | `internal/git/git.go:968-974` | Latent. `GitDir` returned untrimmed `rev-parse` output; embedded `\n` broke recursive marker writes (ENOENT). Convention: `run` doesn't trim, `CurrentSHA` does — `GitDir` missed it | `strings.TrimSpace` added |
| 5 | `internal/ipc/subagent_test.go:45` | Latent. `fmt.Sscan(runID, "subrun-%d", &idx)` — Sscan takes no format string, always errors → fake `Events` always returned nil → drain silently produced no report lines (confirmed by empirical probe) | `fmt.Sscanf` |

## Code changes

**Store / daemon**
- `internal/store/store.go` — schema v5 (`conversations.forked_from`, subagent diff marker), `ForkConversation` journal-copy op, `InsertDiff`/`InsertSubagentDiff` pair, `ListWorkstreams` fork-provenance join; version assertions 4 → 5.
- `internal/ipc/protocol.go` — `CmdForkConversation` (`fork_conversation`), `CmdSpawnSubagent` (`spawn_subagent`), request/response types.
- `internal/ipc/fork.go` (new) — `handleForkConversation`: event copy, new conversation, worktree from last-accepted-diff HEAD, floor + upper-bound refusal pre-checks.
- `internal/ipc/subagent.go` (new) — `handleSpawnSubagent`: `sub-` worktree, OMP run (goal prompt, context prepended, `--mode json`), `subAgentRun` tracking, preview-stripping drain, `subagent_done`, recursion guard.
- `internal/ipc/server.go` — `Server.subagents` field + `NewServer` init; `send_message` dispatch (~:779) and `poll_events` (~:789) wiring; liveness; boot recovery; auto-land exclusion; pure recover-filter helper.
- `internal/git/git.go` — `GitDir` trim fix.
- New agent-facing CLI command file (OMP child → daemon IPC).

**GUI**
- `gui/src/components/MessageBubble.tsx` — fork affordance on user_message bubbles: hover GitFork icon (lucide-react) beside the copy button, "forking…" spinner, switches to the new conversation on success, disabled while `agentRunning` (Retry pattern).
- `gui/src/components/RunsPanel.tsx` — nested `└ sub:` rows under the parent run (goal, running/done/failed status, view-diff link); accept/reject work on subagent diffs, accept lands in the parent worktree.
- `gui/src/App.tsx` / `api.ts` — conversation-switch path on fork; IPC wrappers.

**Tests**
- New store fork/subagent tests; new `internal/ipc/fork_test.go`, `subagent_test.go`; test-harness fixes (`rig.callExpectErr` refusal table, `fmt.Sscanf` fake).

## Verification

- Round 1 gate: tsc ✅, vitest ✅, playwright ✅, go ❌ (the 2 real failures above) → `auto_land_blocked`, reason `verify_failed` — diff #149.
- Round 2 (focused, per instruction): `go test ./internal/ipc/ -run 'TestFork|TestSpawnSubagent' -count=1` — **8/8 green** (includes the 3 subagent tests that previously failed alongside); `go test ./internal/git/ -count=1` green; `go build ./... && go vet ./...` clean; `gofmt -l internal/` clean.
- Final staged state: 18 files, +2211/−34; `git diff --cached --check` clean; worktree has zero commits and zero unstaged residue — extraction left to the auto-land pipeline.
- Full verify gate NOT re-run in round 2 (explicit instruction: stage and stop).

## Open loops

- Diff #149 (with round-2 fixes staged) still needs the full verify gate — go full suite + tsc + vitest + playwright — before auto-land can proceed; round 1's `verify_failed` block has not been cleared.
- Audit item #12 also names subagent "resume" (`resume_from`); this diff implements report + isolation only, no resume IPC was specified or built — decide whether it's a follow-up item or intentionally out of scope.
- GUI render arm for the fork-receipt event: round 1 noted the default render arm dumps raw JSON; confirm a dedicated render arm shipped with the diff or file it as a P2 polish item.
- Store-side weakness remains: `ForkConversation`'s transaction rollback only reverts the conversation row, not any created worktree lane (the handler-side upper-bound pre-check guards the only current caller) — a store-level cleanup was not built.