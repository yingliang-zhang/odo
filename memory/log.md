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

### M0 implementation — Step 2: Tauri 2 + React frontend (completed)

- Dispatched K3 via OMP wrapper (coupled-v1, implement, 600s)
- K3 wrote 23 source files: React components (App, ChatSurface, MessageBubble, ToolTicker, DiffViewer, Sidebar) + Rust lib.rs (Unix socket client + Tauri commands) + config + icons
- K3 ran full e2e smoke test with stub OMP wrapper: bootstrap → send → poll → accept → reject → restore ✓
- tsc --noEmit ✓ | vite build ✓ (39 modules) | cargo check ✓ | cargo test ✓ (2 skipped)
- tauri dev launched: daemon + vite on 1420 + app window running ✓
- K3 fixed UX issue: failed send no longer clears draft
- Committed: c3dac2c (79 files, 7912 insertions), pushed to main
- HEAD: c3dac2c

### M0 implementation — Step 3: Dual-model direction audit (in progress)

- Review prompt: /tmp/odo-m0-review.md (direction drift + schema compliance + code quality + invariants + infra budget)
- GLM-5.2 audit dispatched (proc_d00af52fb7e5, 900s)
- K3 audit dispatched (proc_76135922108a, 900s)
- Both running in parallel, normal tier, read-only

### M0 implementation — Step 3: Dual-model direction audit (completed)

- GLM-5.2 verdict: ✅ ACCEPT — zero direction drift, schema-perfect, all 3 invariants hold, worktree lifecycle correct, infra budget met (~30-35% plumbing)
- K3 verdict: ✅ ACCEPT — zero direction drift, schema exact, 0 lines trust/governance infra, tests re-verified
- Both flagged: hardcoded provider/model in omp.go (spec-authorized but deferred from prefs.md)
- K3 found 2 journal truth ordering issues in server.go (≤10 line fixes each)
- Applied 3 fixes: (1) advance consumed per journaled event, (2) set finished after diff insert, (3) update diff status before review_action event
- Committed: 19eaf37, pushed to main

### M0 implementation — Step 4: E2e demo verification (completed)

- Automated demo with stub OMP wrapper (writes "Hello from Odo" to hello.txt)
- Full visible loop verified: bootstrap → send_message → poll (agent_text + agent_done) → accept_diff → hello.txt exists with correct content → kill daemon → restart → 4 events restored
- All 9 M0 demo steps pass
- HEAD: 19eaf37

### M0 milestone closed — 2026-08-01

User ran the actual `tauri dev` Demo and confirmed:
- Send message → OMP completes → diff appears
- Accept → file created in project
- Quit + reopen → conversation restored

M0 "Visible Loop" milestone is CLOSED (human-verified, per governance rule: human-only close).
Pain points #2 (context loss on session switch) and #3 (can't see agent progress) are relieved.
HEAD: b5a8e9e
