# Tri-Model UX/Product Advice Brief: egoist/waku Architecture vs odo GUI

## Background — Both Systems

### Odo (your system)

**Stack:** Tauri 2 + React 18.3 + Vite 5 (TypeScript), Go daemon backend
**GUI:** Single-window SPA in Tauri webview, three-column layout (sidebar | chat | panel)
**Dependencies:** @tauri-apps/api, react, react-dom, lucide-react, clsx — extremely lean
**Transport:** Tauri IPC → Go daemon (not HTTP)
**Agent integration:** Single adapter (OMP adapter) → Hermes wrapper → OMP agent process in worktree
**Model resolution:** prefs.md key ("coding" or "orchestrator") → modelspec table (per-model context window, compaction ratio, output budgets)
**MoA client (internal/moa/client.go):** Direct Anthropic Messages API → Sudo gateway for /panel, /vision, review tasks. Output budget escalation ×2 on truncation. Read-only tool loop support.
**Store/journal:** SQLite-based conversation store with event journaling (agent_text, agent_tool_call, agent_tool_result, agent_done, agent_error, review_action, etc.)
**Workstreams:** odo/main internal branch, accept lands on main, AdvanceBranch fast-forwards. Git worktrees per run.
**Steering:** adapter.Send writes to steering.txt in session dir
**Streaming:** --mode json, JSONL event stream tailed via byte-offset cursor
**Key GUI pain points (current):**
- Epoch-fold chip UX (collapsible "已折叠 N 条" chip, schema records lastSeq/note_path)
- Streaming JSONL event rendering (in-flight preview with partial:true)
- Panel multi-model output layout (tri-model /panel output display)
- DiffViewer file navigation (recently added per-file chip row)

### Waku (reference system)

**Stack:** Rust + GPUI (Zed Industries' GPU-accelerated UI framework, Metal on macOS)
**License:** GPL v3.0, 374 stars, very new (Aug 2026)
**Platform:** macOS only (requires Rust 1.96+, Bun for build)
**Positioning:** "Control plane" — drives existing coding agent CLIs on your machine, centralizes sessions/transcripts/tool call logs/checkpoints into a native GUI
**Supported agents:** Amp, Claude Code, Codex CLI, Cursor CLI, Grok Build, OpenCode, Pi
**Agent integration:** Auto-detects installed CLIs, uses each provider's native structured protocol and session continuity (NOT a custom wrapper layer like odo's OMP adapter)
**Key features (from README):**
1. Keep projects and independent agent sessions in one native app
2. Switch models, reasoning effort, and access modes from a shared interface
3. Queue or steer follow-up messages while an agent is working
4. Rewind Git-backed tasks with conversation-aware checkpoints
5. Store app state locally, with no Waku account or remote service required
**Architecture references (from AGENTS.md):**
- References T3 Code for "what a coding-agent client should do"
- References Zed source code for "how a polished GPUI app implements it" (layout/styling idioms, focus/key dispatch, virtualized lists)
- AGENTS.md says: "Waku should keep native macOS conventions"
- "Split the two references by concern: T3 Code answers what a coding-agent client should do, Zed answers how a polished GPUI app implements it"

### Key architectural differences affecting borrowability

| Dimension | Waku | Odo |
|---|---|---|
| UI framework | GPUI (Rust, GPU-accelerated, native) | React 18.3 in Tauri webview (web tech) |
| Agent integration | Multi-CLI auto-detect, native protocols | Single OMP adapter via Hermes wrapper |
| Transport | Direct CLI spawning + native protocol | Tauri IPC → Go daemon → OMP process |
| Rendering | GPU-direct (Metal) | Webview (Chromium/WebKit) |
| Platform | macOS only | Cross-platform (Tauri: macOS, Linux, Windows) |
| Session management | Per-CLI, conversation-aware checkpoints | SQLite journal + worktrees + epoch folding |
| Model switching | "Switch models, reasoning effort, access modes" | prefs.md fixed model per key, moa client for /panel |
| State | Local-first, no account | Local-first (~/.odo/ + project .odo/) |

## Specific Questions

### Q1: Multi-CLI agent integration pattern

**What Waku does:** Auto-detects 7+ coding agent CLIs (Amp, Claude Code, Codex CLI, Cursor CLI, Grok Build, OpenCode, Pi), uses each provider's native structured protocol and session continuity. No custom wrapper layer — it speaks each CLI's own language.

**What odo does:** Single OMP adapter → Hermes wrapper → OMP agent process. All agent runs go through one adapter interface (Start/Send/Events/Cancel/Close). Model is fixed by prefs.md key. The moa client bypasses OMP for thinking tasks but is not a general agent adapter.

**Question:** Should odo adopt a multi-CLI auto-detection pattern? Is odo's single-adapter architecture a limitation or a deliberate design choice? What would the ROI be of supporting multiple agent backends?

### Q2: Conversation-aware checkpoint / rewind UX

**What Waku does:** "Rewind Git-backed tasks with conversation-aware checkpoints." Users can rewind to previous points in both code and conversation, not just git state. Each checkpoint is aware of the conversation context at that point.

**What odo does:** Git worktrees per run (physical isolation), accept/reject diff flow, journal replay (SQLite event log). Epoch folding collapses old conversation turns. No explicit "rewind to checkpoint N" UI — the journal has the data but there's no user-facing checkpoint browser.

**Question:** Should odo add a conversation-aware checkpoint browser? The journal already has per-event records — is a Waku-style "rewind to checkpoint" UI valuable, or does odo's worktree + accept/reject flow already cover this need better?

### Q3: Model switching from a shared interface

**What Waku does:** "Switch models, reasoning effort, and access modes from a shared interface." Users can dynamically change which model/reasoning-effort is used, presumably per-session or per-message.

**What odo does:** Model is fixed by prefs.md key. /panel uses tri-model MoA (3 models in parallel, orchestrator consolidates). There's no in-session model switch — changing models requires editing prefs.md and restarting the daemon.

**Question:** Should odo add in-session model switching? Is the prefs.md fixed-model approach a limitation, or is it a deliberate stability choice? What's the value of dynamic model switching vs the complexity it adds?

### Q4: Queue / steer follow-up messages

**What Waku does:** "Queue or steer follow-up messages while an agent is working." Users can queue messages to be sent when the agent finishes, or steer (inject mid-run).

**What odo does:** adapter.Send writes to steering.txt in the run's session dir. The wrapper may read it between turns. This is a best-effort hand-off, not a visible queue. There's no UI for queuing multiple messages or seeing the queue.

**Question:** Should odo add a visible message queue UI? Is odo's steering.txt approach sufficient, or would a Waku-style visible queue/steer UX improve the agent-run experience?

### Q5: GPUI native rendering vs Tauri webview — architecture lesson

**What Waku does:** Uses GPUI (GPU-accelerated, Rust-native, Metal on macOS). AGENTS.md references Zed's layout/styling idioms, focus/key dispatch, virtualized lists.

**What odo does:** React 18.3 in Tauri webview. All rendering goes through the browser engine. No GPU-direct rendering. Virtualized lists would need React virtualization libraries.

**Question:** Is GPUI a better architecture for odo's use case? Should odo consider migrating from Tauri+React to a native Rust UI (GPUI or similar)? Or is the Tauri+React stack the right choice for odo's cross-platform needs and lean dependency profile? What specific performance or UX gaps in the current stack would justify a migration?

### Q6: Session/transcript centralization UX

**What Waku does:** Centralizes sessions, transcripts, tool call logs, and checkpoints from multiple agent CLIs into one native GUI. Each project shows its independent agent sessions.

**What odo does:** SQLite journal stores all events. Conversations are per-project. Workstreams track per-run state. But there's no unified "browse all sessions across all agents" view — odo only has one agent (OMP).

**Question:** Is Waku's multi-agent session centralization relevant to odo, given odo's single-adapter architecture? If odo added multi-CLI support (Q1), would a unified session browser become necessary?

## Constraints

- Odo is 18K LOC (not 120K) — don't copy enterprise-scale architecture
- Odo is a desktop agent GUI, not an IDE — don't borrow IDE patterns
- Odo's workstream ≠ Waku's session — different semantics
- Odo's Tauri+React stack is a deliberate choice for cross-platform support and lean dependencies — migration cost would be enormous
- Odo's tri-model MoA is a different paradigm from Waku's single-model-per-session
- Any adopted feature must work within Tauri webview constraints (no GPUI-specific patterns transfer directly)

## Non-goal + Output Format

**Non-goal (REQUIRED):**
> This is **advice only**. Do NOT implement any changes. For each question,
> give a verdict (ACCEPT/conditionally recommend/not recommended) with
> rationale and an estimated code cost. End with a prioritized action list.

**Output format (REQUIRED):**
For each question (Q1-Q6):
1. **Verdict** — ACCEPT / conditionally recommend / not recommended
2. **Rationale** — why borrow or why not (grounded in both architectures)
3. **How** — concrete approach if borrowing (e.g., "add checkpoint browser component in sidebar", not "improve UX")
4. **Cost** — estimated lines of code, new dependencies, backend changes
5. **What NOT to borrow** — the parts of Waku's pattern that don't transfer

End with a **prioritized action list**: P0/P1/P2 ranked by ROI, with
explicit "do not do" items and their structural reasons.

**FINAL-MESSAGE CONTRACT:**
Your reply may take many tool calls. But the session's FINAL assistant
message must contain the COMPLETE structured deliverable (all 6 questions +
action list). Do NOT stop after dumping intermediate evidence notes; keep
writing inside the same turn until the full report is in the final message.

Write your complete analysis as text in your response. Do NOT write files
to the repository.
