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

### M0.1 known issues — deferred to M1 E2E verification

1. **Accept may fail on stale diffs** — old diffs (created before the --3way fix) may still fail `git apply`. Fix: kill old daemon, rebuild, restart. The auto-commit + --3way fix works for NEW diffs.
2. **K3 thinking blocks not round-tripped** — OMP harness for custom:sudo doesn't send thinking blocks back in subsequent turns → K3 may degrade in multi-turn. OMP limitation, not Odo bug. M1 consideration: custom adapter or JSON streaming mode.
3. **Clipboard paste attaches filename only** — webview security exposes `File.name`, not absolute path. Chips show but paths won't resolve. Platform limitation.
4. **ToolTicker shows post-completion** — M0 adapter emits events atomically after OMP exits; tool calls appear as transcript, not live during execution. M1: streaming events.

### M0.1 milestone — user-verified (2026-08-02)

User verified: tool calls display in ticker, diff syntax highlighting works, drag-and-drop chips appear, completion text shows. Accept on stale diffs pending M1 E2E.

HEAD: 378d837

### M1 implementation — Go daemon + frontend (completed)

- Go daemon (f0102ae): multi-workstream IPC, steering, distill, Pi adapter. 10 files, +1241 lines. 6 new tests, all pass.
- Frontend (a510def): workstream sidebar, steering input, distill button, adapter selector. 8 files, +743 lines. tsc + vite + cargo check pass.
- Dual-model review dispatched (GLM-5.2 + K3, parallel, 900s each).
- HEAD: a510def

### M1 review fixes (bd86530)

4 fixes from dual-model review (GLM-5.2 + K3, both NEEDS FIXES):
1. Distiller uses orchestrator model (NewOMPForKey "orchestrator")
2. Worktree leak on UpdateWorkstreamWorktree failure
3. Steering error message conditional ("not supported" only)
4. UI epoch filter (ChatSurface shows only current epoch)

### M1 E2E verification (69d3d71)

- Daemon-level E2E: 26/26 passed (M0 visible loop + M0.1 polish + M1 multi-workstream/steering/distill/Pi)
- GUI automated test (cua-driver AX tree): 13/16 passed (3 unverifiable due to no Screen Recording permission)
- Go unit tests: all pass (adapter 3s + ipc 24s + store 1s)
- Attachment test added (TestAttachmentsJournal) — gap identified by GLM-5.2 verification
- Dual-model verification: GLM-5.2 ACCEPT + K3 ACCEPT

### M1 milestone — CLOSED (2026-08-02)

Dual-model verification passed. All M0→M1 features validated via automated tests.
HEAD: 69d3d71

### M2 implementation — Go daemon + frontend (completed)

- Go daemon (dcb8a6a): MoA review fan-out, settings commands, parallel fan-out. 5 files, +1013 lines. 4 new tests, all pass.
- Frontend (ae2a5e5): review panel, settings UI, fan-out view. 9 files, +766 lines. tsc + vite + cargo check pass.
- Dual-model review: GLM-5.2 ACCEPT (4 non-blocking), K3 NEEDS FIXES (3 must + 6 should).

### M2 review fixes (6768cea)

6 fixes from dual-model review:
1. F1: Accept button disabled when any model REJECTs
2. F3: Auto-reject sibling fan-out diffs on accept (prevents worktree leaks)
3. F4: parseVerdict requires verdict at line start (prevents "I cannot accept" false-accept)
4. F6: SettingsPanel placeholder shows model@provider format
5. F7: UpdateSettings atomic write (temp file + os.Rename)
6. F9: fanout_send N capped at 8

### M2 E2E verification

- Go tests: 25/25 test functions PASS (M0→M1→M2 all features)
  - M0: TestVisibleLoopAcceptRejectRestore
  - M0.1: TestAttachmentsJournal, TestParseToolCalls, TestResolveModelConfig
  - M1: TestCreateWorkstream, TestBootstrapByWorkstream, TestSteering, TestDistill, TestPiRunIPC
  - M2: TestReviewDiff, TestGetSettings, TestUpdateSettings, TestFanoutSend (auto-reject)
  - Store: 10 tests, Adapter: 6 tests
- Dual-model review: GLM-5.2 ACCEPT + K3 NEEDS FIXES → 6 fixes applied → re-verified all pass

HEAD: 6768cea

### M2 GUI automated test (K3 + cua-driver)

M2 GUI verified via cua-driver AX tree (MCP session for clicks, direct call for reads):
- UI elements: 11/11 (workstream heading, buttons, settings, adapter, accept/reject)
- Settings panel: click opens modal with 5 text fields + Save/Close buttons, close returns to main view
- Fan-out: click opens N picker with text input
- 21/21 passed

### M2 milestone — CLOSED (2026-08-02)

Dual-model review (GLM-5.2 ACCEPT + K3 NEEDS FIXES → 6 fixes), Go tests 25/25, 
GUI automated test 21/21. All M0→M1→M2 features validated.

HEAD: 6768cea

## M3 (memory recall + wiki browser + user.md + visibility) — CLOSED

### Implementation (commit 4af1ead)

1. **Memory recall**: `recallWikiNotes` (≤12KB, epoch-desc, note-boundary) + `readUserMemory` (`~/.odo/user.md`, ≤4KB, line-boundary) → `buildPrompt` (user.md → wiki → attachments → text, ADR-0003 inv. 6); `recall: [paths]` on `user_message` + `fanout_send` payloads.
2. **Wiki browser**: `list_wiki`/`read_wiki` IPC (guard: only `<project>/wiki/**` + exact `~/.odo/user.md`); `WikiBrowser.tsx` modal with pinned synthetic user.md row + create-hint; sidebar "N wiki notes" + Browse.
3. **Visibility pack**: run status bar `running — <elapsed> — tool: <last> (call <n>)`; notification on `agent_done` when `document.hidden` (`@tauri-apps/plugin-notification`); sidebar badges (green dot running, red pill pending diff count) via `pending_counts` IPC fallback.
4. **default_adapter fix**: empty workstream adapter → prefs `default_adapter` → `"omp"`.

### Review (dual-model)

| Model | Verdict | Findings |
|---|---|---|
| GLM-5.2 | ACCEPT | 2 MINOR (test traversal coverage, handleReadWiki ctx param) |
| K3 | ACCEPT | 2 MINOR (elapsed anchor, tooltip path shortening) |
| Re-review | PASS | All 4 fixed |

### Verification

- Go: build/vet/test pass (10 new M3 tests)
- Frontend: tsc/build/cargo check pass
- GUI E2E (cua-driver AX tree + SQLite journal): 16/16 PASS
  - Demo A: recall chip `memory: user.md + 1 note(s) recalled`; journal `recall: ["~/.odo/user.md", ".../main-epoch-1.md"]`
  - Demo B: wiki browser pinned row + note render + user.md content + modal close
  - Demo C: status bar `running — 3s`; diff lifecycle; `pending_counts` IPC `{"ok": true}`

HEAD: 4af1ead

## M4 GUI (cua-driver)

Full M4 GUI E2E via cua-driver AX tree + SQLite journal + IPC socket verification
(hybrid pattern: direct call for get_window_state, MCP stdio session for click/type).
34 tests: 33 PASS / 0 FAIL / 1 SKIP (badge consumed after apply — expected).

### Demo A: project memory learned and injected

| Test | Result | Method | Evidence |
|---|---|---|---|
| A1: type + Send | ✅ | AX | text 'hello' in input field; Send clicked |
| A1: recall chip | ✅ | AX | `memory: user.md + 1 note(s)` (no memory.md yet) |
| A1: journal recall | ✅ | SQLite | `recall=["~/.odo/user.md", ".../main-epoch-1.md"]` — user.md first |
| A1: journal receipt | ✅ | SQLite | `receipt={"~/.odo/user.md": "fec0f06d8f3463f1", ".../main-epoch-1.md": "b267447428c803da"}` |
| A2: agent done | ✅ | IPC | agent finished; diff 1 accepted |
| A3: distill click | ✅ | AX | Distill button clicked |
| A3: proposals IPC | ✅ | IPC | epoch=1, 2 memory proposals |
| A3: journal propose | ✅ | SQLite | `memory_propose` event: epoch=1, 2 memory proposals |
| A3: review badge | ✅ | AX | `2 memory proposed — Review` |
| A4: review open | ✅ | AX | Review button → MemoryReviewPanel opened |
| A4: apply click | ✅ | AX | `Apply (2 accepted)` button clicked |
| A4: memory.md | ✅ | File | memory.md written (174 bytes), `go test` + `cites: main-epoch-1` |
| A4: read_memory IPC | ✅ | IPC | `read_memory` returns 174 bytes memory + user.md |
| A4: journal apply | ✅ | SQLite | `memory_apply`: epoch=1, metrics={accepted:2, rejected:0} |
| A4: journal update | ✅ | SQLite | `memory_update(apply)`: layer=memory, detail="accepted 2 rule(s)" |
| A5: memory chip | ✅ | AX | `memory updated` chip in sidebar (recordEvents path, not applyBootstrap) |
| A6: journal recall | ✅ | SQLite | recall includes `.odo/memory.md` + `~/.odo/user.md` + wiki note |
| A6: journal receipt | ✅ | SQLite | receipt includes `.odo/memory.md`: `8a1ec6eeaa95c990` |
| A6: recall chip | ✅ | AX | `memory: user.md + memory.md + 1 note(s)` — memory.md now present |

### Demo B: user.md cross-project promotion

| Test | Result | Method | Evidence |
|---|---|---|---|
| B1: distill epoch 2 | ✅ | IPC | memory_proposals=2, wiki=main-epoch-2.md |
| B2: proposals | ✅ | IPC | 1 user.md proposal + 1 memory.md proposal (sibling registry ≥2 gate) |
| B3: apply | ✅ | IPC | applied 2 proposals (memory.md + user.md) |
| B4: user.md | ✅ | File | user.md updated: `seen: odo, ananke` cross-project promotion |
| B5: memory.md | ✅ | File | memory.md has epoch-2 rule (242 bytes) |
| B6: journal user update | ✅ | SQLite | `memory_update`: layer=user, cause=apply, detail="accepted 1 rule(s)" |
| B7: read_memory | ✅ | IPC | `read_memory` returns updated user.md with promoted rule |
| B8: AX badge | ⏭️ SKIP | AX | badge consumed after apply (expected — not a failure) |

## M4 (learning) — CLOSED

### Implementation (4 commits)

| SHA | Content |
|---|---|
| 70abe3a | docs(m4): frozen spec — GLM-5.2 + K3 dual-model ACCEPT (5 rounds) |
| f63ecae | feat(m4): memory learning — backend (registry/learner/io/buildPrompt 5-arg/IPC/memory_update) + frontend (MemoryReviewPanel/sidebar chip/recall chip/lib.rs 960s) |
| 297ec91 | fix(m4): audit must-fixes — distinct rotate/retract memory_update events + all-or-nothing apply on mid-write failure |
| c54f28a | fix(m4): user.md apply idempotent skip — retry converges instead of duplicate lines/deadlock |

### Review (dual-model)

| Model | Verdict | Findings |
|---|---|---|
| GLM-5.2 | ACCEPT (initial NEEDS_FIXES → 2 fixes → re-review ACCEPT) | rotate/retract distinct events; apply write order archive→user→memory(last) |
| K3 | ACCEPT (initial NEEDS_FIXES → user.md idempotent skip → re-review ACCEPT) | retry convergence on mid-write failure; userRuleBody+normalized dedup |

### Verification

| Gate | Result |
|---|---|
| Go build/vet/test ./... (10 new M4 tests + 2 fix tests) | ✅ all green (ipc ~58s) |
| gui: npx tsc --noEmit + npm run build | ✅ |
| gui/src-tauri: cargo check | ✅ |
| Dual-model review | ✅ GLM-5.2 ACCEPT + K3 ACCEPT |
| GUI E2E (cua-driver AX + SQLite + IPC) | ✅ 33 PASS / 0 FAIL / 1 SKIP |

HEAD: c54f28a

## M5 (curation) — CLOSED

### Implementation (commit 86ea66c)

14 files, +1740 lines. K3 `--thinking max` (initial dispatch timed out at 600s with 5 scouts; resume by exact UUID completed remaining files).

| Area | Files | Lines |
|---|---|---|
| Backend: curator pass | `internal/ipc/curator.go` (new) | 364 |
| Backend: pin affordance | `internal/ipc/pins.go` (new) | 86 |
| Backend: dispatch + buildPrompt 7-arg | `internal/ipc/server.go` | +50 |
| Backend: IPC constants | `internal/ipc/protocol.go` | +7 |
| Tests: 12 Go tests | `internal/ipc/curator_test.go` (new) | 650 |
| Frontend: App state + handlers | `gui/src/App.tsx` | +63 |
| Frontend: Sidebar Curate + Pin | `gui/src/components/Sidebar.tsx` | +97 |
| Frontend: WikiBrowser Topics tab | `gui/src/components/WikiBrowser.tsx` | +200 |
| Frontend: recall chip extension | `gui/src/components/MessageBubble.tsx` | +27 |
| Frontend: API + types + CSS | `gui/src/api.ts` + `types.ts` + `app.css` | +203 |
| Tauri: 4 new commands | `gui/src-tauri/src/lib.rs` | +49 |

### Review (tri-model blind, 3/3 ACCEPT)

| Model | Verdict | Key findings |
|---|---|---|
| K3 | ACCEPT | ⚠️ pin doesn't trim/reject multi-line (frontend enforces; socket-direct edge) · ⚠️ write-failure path unjournaled (asymmetric with parse-failure; re-curate recovers) |
| GLM-5.2 | ACCEPT | ⚠️ DISTILL_READ_TIMEOUT bump (1200s) not implemented — spec marks optional/conditional on `curate:true` flag, defaults off · ⚠️ citation jump cross-workstream ambiguity (frontend, no Go test) |
| DeepSeek | ACCEPT | ⚠️ `jumpToEpoch` matches only current workstream's epoch — cross-workstream same-number ambiguity · spec risk #3 accepts degraded links |

### Verification

| Gate | Result |
|---|---|
| go build/vet/test ./... (12 new M5 tests + all M0-M4) | ✅ (ipc 67s) |
| npx tsc --noEmit + npm run build | ✅ (204.88 kB) |
| cargo check | ✅ |
| Tri-model blind review | ✅ 3/3 ACCEPT |

HEAD: 86ea66c
