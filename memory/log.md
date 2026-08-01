# Odo Development Log

Append-only. If it's not in the log, it didn't happen.

## 2026-08-01

### Session start — Ananke → Odo restart

- Completed divergence analysis: Ananke 95% diverged from original intent (0% of 4 pain points addressed, 124K LOC of trust infrastructure nobody asked for)
- Dual-model review (GLM-5.2 + K3) confirmed: restart from scratch, cherry-pick store/Tauri/codegen
- Renamed github.com/yingliang-zhang/ananke → ananke-archive
- Created github.com/yingliang-zhang/odo
- Confirmed all design decisions: Odo name, Tauri 2 + Go daemon + React, conversation-centric + worktree plumbing, OMP+Pi adapter interface, MoA for all read-only perspective-diversity tasks, K3 coding + GLM-5.2 orchestrator
- Wrote ADR-0001 (M0 trust posture — human review only, no attestation)
- Wrote ADR-0002 (fresh journal schema — 5 tables, no Ananke inheritance)
- Wrote M0 milestone spec (m1-visible-loop.md)
- Created ~/.odo/prefs.md with model config (orchestrator=glm-5.2, coding=t9s/kimi-k3, review_panel=both)

### Next: implement M0 vertical slice

Per K3's recommendation: build end-to-end ugly vertical slice first, not layer-by-layer.
Order: type request → OMP runs (polled) → diff as text in chat → Accept runs git apply → kill+relaunch → conversation restored.

### Session switch — M0 implementation handoff

- Context full after multi-round dual-model design discussion
- Handoff prompt saved: docs/session-prompts/2026-08-01-m0-implementation.md
- New session should read that prompt + milestone spec + ADRs + log.md
- HEAD: 9e9aa44 (initial commit with docs only, pushed to origin/main)

### M0 implementation — Step 1: Go daemon (completed)

- Dispatched K3 via OMP wrapper (coupled-v1, implement, 600s)
- K3 wrote 15 Go files (2222 lines): store (6+test), adapter (2), git (1), worktree (1), ipc (2+test), main.go
- Schema matches ADR-0002 exactly (5 tables: projects, workstreams, conversations, events, diffs)
- modernc.org/sqlite v1.55.0 (pure Go, no CGO), WAL mode, MaxOpenConns(1)
- OMP adapter: 5-verb interface, subprocess via Hermes wrapper, print mode
- Worktree: git worktree at <project>/.odo/worktrees/<run-id>, persists until accept/reject
- IPC: Unix socket, line-delimited JSON (bootstrap, send_message, poll_events, accept_diff, reject_diff)
- Build: go build ./... ✓ | Vet: go vet ./... ✓ | Tests: 10/10 ✓ (store 9 + e2e 1)
- Committed: 09d1027, pushed to main
- HEAD: 09d1027

### M0 implementation — Step 2: Tauri 2 + React frontend (in progress)

- Dispatched K3 via OMP wrapper (coupled-v1, implement, 600s)
- Prompt: .odo/prompts/step2-tauri-react.md
- Target: Tauri 2 shell + React/Vite frontend + Rust Unix socket client + chat/diff/poll UI + session restore
