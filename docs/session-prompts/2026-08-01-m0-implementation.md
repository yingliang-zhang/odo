# Odo M0 Implementation Handoff Prompt

## Session Goal

Implement Odo M0 "Visible Loop" end-to-end. Auto-develop until M0 demo passes. Use K3 for coding (via OMP wrapper), GLM-5.2 + K3 dual-model for review/audit.

## Repository

- **Repo**: `/Users/yingliangzhang/Projects/odo`
- **GitHub**: git@github.com:yingliang-zhang/odo.git (SSH push, HTTPS fetch)
- **Branch**: main (direct push, pre-v0.1)
- **HEAD**: 9e9aa44 (initial commit with docs only)
- **Old repo**: github.com/yingliang-zhang/ananke-archive (reference only, do NOT modify)

## First Steps (Read These Before Coding)

1. Read `docs/milestones/m1-visible-loop.md` — the M0 milestone spec (Pain/Demo/Not-built)
2. Read `docs/adr/0001-m0-trust-posture.md` — no attestation, human review only
3. Read `docs/adr/0002-fresh-journal-schema.md` — 5-table schema (projects, workstreams, conversations, events, diffs)
4. Read `README.md` — 7 development principles
5. Read `memory/log.md` — development log (resume context)
6. Run `git status` and `git log --oneline`
7. Read `~/.odo/prefs.md` — model config (orchestrator=glm-5.2, coding=t9s/kimi-k3, review_panel=both)

## M0 Scope (from milestone spec)

Build end-to-end ugly vertical slice, NOT layer-by-layer:

1. User types message in chat → message journaled as `user_message` event
2. Go daemon spawns OMP (print mode, polled) in a worktree
3. OMP output parsed into `agent_text` / `agent_tool_call` / `agent_tool_result` events
4. Agent completes → diff extracted from worktree
5. Diff appears as text in chat with [Accept] [Reject] buttons
6. Accept → `git apply` onto project root (in Go daemon) → `review_action` event journaled
7. Quit Odo completely → reopen → conversation restored from SQLite journal

## Architecture

```
Tauri 2 shell (React + Vite in native WebView)
    ↕ Unix socket IPC (typed JSON)
Go daemon
    ├── SQLite journal (5 tables per ADR-0002)
    ├── Adapter runner (5-verb interface: Start/Send/Events/Cancel/Close)
    │   └── OMP adapter (subprocess, print mode, polled events via Go channels)
    ├── Worktree lifecycle (persist until accept/reject, NOT /tmp — use <project>/.odo/)
    └── git apply on Accept (in Go daemon, serialized per project)
```

## Key Design Decisions (All Confirmed)

| Decision | Value |
|---|---|
| Name | Odo (ὁδός, "way/path") |
| Stack | Tauri 2 (Rust shell) + Go daemon + React (Vite) |
| Frontend | React (not Vanilla TS — both models agreed) |
| Memory | SQLite journal (authority) + per-project markdown wiki (context) + ~/.odo/prefs.md (global) |
| Adapters | 5-verb interface (Start/Send/Events/Cancel/Close) + capability flags; OMP only for M0, Pi for M1 |
| Event transport | Polling (1.5s), declared as polling — NOT SSE/WebSocket |
| Storage | `<project>/.odo/worktrees/<run-id>` + `<project>/.odo/diffs/` (NOT /tmp) |
| git apply | In Go daemon (Invariant 1: Go owns state) |
| Worktree lifetime | Persist until accept/reject/close (no defer RemoveAll) |
| Restore | Full-quit journal restore on next launch |
| In-app orchestrator | None in M0 — user IS the orchestrator; chat talks directly to coding adapter |
| Model config | ~/.odo/prefs.md: orchestrator=glm-5.2@sudo, coding=t9s/kimi-k3@sudo, review_panel=[both] |
| Trust | Human review only (ADR-0001), no attestation |

## OMP Wrapper (for coding tasks)

```bash
# K3 coding (via OMP wrapper)
export HERMES_CODING_WORKFLOW=coupled-v1
~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh 600 \
  <prompt_file> <output_file> \
  --workflow "$HERMES_CODING_WORKFLOW" --role implement \
  --run-id <stable-id> \
  --hermes-provider custom:sudo --hermes-model t9s/kimi-k3 \
  --task-tier normal --session-dir <isolated-dir>

# GLM-5.2 review
~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh 900 \
  <prompt_file> <output_file> \
  --workflow "$HERMES_CODING_WORKFLOW" --role audit \
  --run-id <stable-id> \
  --hermes-provider custom:sudo --hermes-model glm-5.2 \
  --task-tier normal --session-dir <isolated-dir>

# K3 review (parallel with GLM)
~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh 900 \
  <prompt_file> <output_file> \
  --workflow "$HERMES_CODING_WORKFLOW" --role audit \
  --run-id <stable-id> \
  --hermes-provider custom:sudo --hermes-model t9s/kimi-k3 \
  --task-tier normal --session-dir <isolated-dir>
```

**Provider note**: Both GLM-5.2 and K3 use `custom:sudo` (NOT custom:sudo-kimi-k3 — that was a config bug, now fixed). Model param: `glm-5.2` vs `t9s/kimi-k3`.

## Governance (Prevents Direction Drift)

1. **Milestone Spec Gate**: M0 spec is in `docs/milestones/m1-visible-loop.md`. No code outside this spec.
2. **Demo = Acceptance**: M0 closes only when user runs the Demo and pain is relieved.
3. **Human-only close**: Orchestrator never marks own milestone complete.
4. **Divergence Budget**: Infrastructure commits >30% of M0 diff → justify in spec.
5. **Per-milestone Direction Audit**: MoA panel (GLM+K3) reviews M0 diff range: "what was built that no spec authorized?"

## Session Switching Protocol

If this session needs to compress/switch:
1. Append to `memory/log.md`: done / decided / next / branch+HEAD
2. New session reads: milestone spec → memory/decisions/ → memory/log.md tail → git log
3. If it's not in the log, it didn't happen.

## Environment

- macOS 27.0 (Apple Silicon)
- Go 1.26.5 darwin/arm64
- Node 22.22.3 at ~/.hermes/node/bin (system Node 26.5.0 breaks TS/tests)
- cargo: source ~/.cargo/env
- OMP: /opt/homebrew/bin/omp
- Hermes profile: orchestrator
- No `timeout` command on macOS — use `sleep` or the OMP wrapper's built-in timeout

## Verification Commands

```bash
# Go
cd /Users/yingliangzhang/Projects/odo && go build ./...
go test ./...

# Rust
source ~/.cargo/env && cd gui/src-tauri && cargo check

# TypeScript
cd gui && PATH=/Users/yingliangzhang/.hermes/node/bin:$PATH npx tsc --noEmit

# Tauri dev
cd gui && PATH=/Users/yingliangzhang/.hermes/node/bin:$PATH npm run tauri dev
```

## Auto-Development Rules

- Pre-authorized: edits, tests, feature-branch git (commit/push to main), verification
- Needs approval: main merge (N/A, already on main), destructive operations
- Coding: delegate to K3 via OMP wrapper
- Review/audit: dual-model (GLM-5.2 + K3 parallel, coverage-union)
- All user-facing messages in Chinese; code/comments/commits in English
- Every commit traces to M0 milestone spec pain point
- Commit format: feat: ... / fix: ... / refactor: ...
- Push to main after each verified step

## Cherry-Pick from Ananke (Reference Only)

The old repo is at `~/Projects/ananke-p0a-schema-codegen` (remote: ananke-archive). Useful patterns to study (NOT copy directly):
- `internal/store/events.go` — AppendEvent/ListEvents pattern (adapt to fresh schema)
- `internal/store/store.go` — SQLite WAL + migration pattern
- `gui/src-tauri/src/lib.rs` — Tauri → Unix socket → Go daemon bridge pattern (rewrite from scratch, much simpler)
- `internal/repairrunner/omp_adapter.go` — OMP subprocess invocation pattern

Do NOT copy Ananke code verbatim. Study the pattern, write fresh Odo code.

## What NOT to Build in M0

- No attestation (ADR-0001)
- No sandbox
- No Pi adapter (M1)
- No file attachments (M0.1)
- No steering (M1+)
- No multi-workstream UI (M1)
- No memory distiller (M1)
- No MoA review fan-out (M2)
- No settings panel (M2)
- No syntax highlighting (M0.1)
