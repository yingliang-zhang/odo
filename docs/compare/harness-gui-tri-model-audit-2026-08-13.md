# Harness GUI/Interaction Tri-Model Audit (2026-08-13): deepseek-harness vs grok-build vs openai/codex

> GUI- and interaction-focused companion to
> `harness-tri-model-audit-2026-08-13.md` (same-day architecture audit).
> Consolidated from a blind-sealed tri-model MoA run. This document compares
> **how the three harnesses feel to use** and which interaction features are
> worth borrowing into Odo.
>
> Raw leg outputs and the frozen brief: `./harness-gui-tri-model-audit-2026-08-13/`
> (`brief.md`, `leg-k3.md`, `leg-glm.md`, `leg-dsf.md`, `evid-dsh-scout.md`).

## 0. Provenance & method

| Leg | Model | Output | Notes |
|---|---|---|---|
| 1 | kimi-k3 (--thinking max, 900s) | 46.8K, highest precision | All 12 groups evidenced with `path:Symbol`; minor duplicated tail in F |
| 2 | glm-5.2 (--thinking max, 900s) | 35.7K, most complete tables | 3 read-only scouts + first-hand verification of transport claims |
| 3 | deepseek-v4-flash (--thinking max) | 20.6K, 3rd attempt | 1st: 900s timeout mid-gathering; 2nd: exact-UUID resume exited instantly ("Working..." only); 3rd: fresh leg with pre-injected dsh scout evidence (`evid-dsh-scout.md`, recovered from attempt-1 submodule JSONL) + hard time discipline → completed. dsh groups 4–12 honestly marked "not gathered"; several D-items miss full fields |

Conventions: **3/3** = all legs agreed; **2/3** = converged with one recorded
dissent/imprecision; **1/3+** = unique addition adopted as gap-fill.

Orchestrator mechanical arbitrations (greps on the frozen clones, no model
judgment involved):

- **grok rewind semantics** — K3: "restores conversation+code with conflict
  reporting"; GLM: "history-only, no file restore". Both cite real artifacts.
  Verified truth: `RewindMode::{All, ConversationOnly, FilesOnly}`
  (`grok:crates/codegen/xai-grok-shell/src/session/acp_types.rs:303-313`) —
  the **documented `/rewind` UX truncates conversation only** (user-guide
  17-sessions.md:151), while the **`x.ai/rewind/execute` RPC exposes file
  time-travel** via the mode parameter. Each leg held half the fact.
- **codex `resume_picker.rs` size** — GLM claimed 7,010 lines / 233 KB.
  Verified independently in two frozen clones: exactly 7,010 lines, 238,847
  bytes. Session management is an entire sub-application in codex.

## 1. Interaction postures (what each one IS to use)

| | deepseek-harness | grok-build | openai/codex |
|---|---|---|---|
| Shape | **Web-only React 18 SPA**, Cordis slot-plugin tree in the browser; no TUI | **Fullscreen ratatui TUI**, mouse best-in-class, alternate screen, Kitty keys | **ratatui TUI** (zero mouse capture) + app-server + IDE/desktop peers |
| Posture | **Chat-host**: one host process, browser tabs are projection clients of a durable log | **Multi-session orchestrator**: dashboard-roster of agents, per-agent task/queue panes | **Focused single thread**: meticulous transcript ledger; approval decisions are transcript cells |
| Authority | Host `ctx.sessions`; UI = server-side `session/projection` fold | `xai-grok-shell` SessionActor per session; leader bridge serves surfaces | One app-server owns threads/turns; every client is a protocol peer |
| Multi-client | Yes (tabs, tab-sync) | Protocol-capable via `leader_bridge.rs`; TUI surface is one fullscreen (2/3; DSF read it as single-client) | Yes — TUI embeds `InProcessAppServerClient`; unresolved prompts reconcile across clients (2/3; DSF imprecise) |

**3/3 divergences that matter for Odo:**

1. **Where UI state is computed.** dsh pushes server-side *projections*
   (`ContextMeter`, todos, queue, titles are host-folded from the log);
   grok/codex fold streams in the client. dsh's model is the closest to Odo's
   "daemon = authority, GUI = view" — GLM scored it "already adopted" in Odo
   (P0, no action needed beyond formalizing the pattern).
2. **Concurrency gravity.** grok = worklist-first dashboard
   (state-grouped rows, peek-reply, dispatch); dsh = single-occupant stage
   with a sidebar list; codex = one primary thread, cross-thread state only
   via an approval inbox. None offers Odo-style cross-workstream concurrent
   runs as the primary surface.
3. **Review philosophy on display.** dsh reviews only *before* execution
   (one-shot approval takeover; diffs display-only). codex reviews before
   (full-screen patch gate) but *records every decision as a journaled
   transcript cell* (incl. auto-review and timeouts) and rewinds after.
   grok reviews *after* (hunks applied, then accept/reject at
   hunk/file/turn/all scope). This is the human↔autonomy spectrum rendered
   in UI form — grok is the only peer shipping post-hoc review, but it is
   manual; Odo's DiffViewer + MoA grading + parked async review is the only
   design that is post-hoc, machine-scored, AND non-blocking.

## 2. Interaction-pattern inventory — who is best per group (3/3 unless noted)

| # | Group | Best | Why / evidence (see legs for full rows) |
|---|---|---|---|
| 1 | Streaming rendering | codex | Two-region `StreamCore` + `TableHoldbackScanner` (tables never flicker); newline-gated commit. grok close (line-source map for copy). dsh: token stream + interrupted markers |
| 2 | Approval/permission UX | grok | Richest prompt: word-scope ←/→, freeform glob edit w/ live preview, MCP tool-vs-server scope, `Allow all edits this session`, reject-with-inline-feedback. codex: session-scoped remembers + **journaled decision cells**. dsh: allow-once only (by design) |
| 3 | Interrupt & steering | grok | `InterjectPrompt` "send now = cancel-and-send", follow-up queue pane with auto-drain on turn end, Ctrl+B backgrounding, adaptive Esc. codex: interrupt coalescing + queued input. dsh: Send↔Stop + durable queue rows (DSF-unique: `Session.prompt(mode: queue\|steer)`) |
| 4 | Input editor | grok | Atomic chips (`[Pasted: N lines]`, `@file`, image), kill ring, mouse select, vim default, history fuzzy, voice. codex: external `$VISUAL` mid-turn. dsh: paste-componentizing undo, IME-safe, `/`+`@` triggers |
| 5 | Command discovery | codex | ~60 slash commands w/ aliases + availability gating + fuzzy popup. grok: registry + chained arg submenus + Ctrl+Space palette (DSF) + MRU. dsh: command directory, 3 dispatch kinds |
| 6 | Session management | codex | `resume_picker.rs` (7,010-line sub-app: preview, archive, fork, density modes, orchestrator-verified) + Esc-Esc backtrack fork-before-turn. grok: picker w/ FTS5 + foreign-session scan + `/rewind`/`/jump` split (DSF). dsh: workspace rows + atSeq fork; viewing cold history *spawns the agent* (anti-pattern) |
| 7 | Background work visibility | grok | Unified tasks pane (BgTask/Agent/Scheduled/Workflow) + kill buttons + live line badges + **persistent "N still running" cue**. codex: footer count + OSC9 + subagent status feed. dsh: header badge + read-only jobs popover, no completion notify |
| 8 | Plan & todo | grok | Plan approval with **per-line-range comments** (`@plan.md:12-18`) + 4-state plan machine + todo pane. codex: goal system w/ pause/resume. dsh: TodoPanel + plan review card (Approve/Refuse/Chat) |
| 9 | Change/diff presentation | grok | Hunk/file/turn/all accept-reject (`xai-hunk-tracker`), edit blocks off-thread highlighted. codex: full-screen diff approval. dsh: display-only `DiffBlock`. **None has Odo-style parked Accept/Reject lanes** |
| 10 | Model & effort UX | dsh | Two-level Model/Effort composer seat + unroutable block contract (admits failure before submit). codex: plan-mode effort separate + service tiers. grok: `/effort` chained submenu + per-row model badge in dashboard |
| 11 | Status & context | dsh | `ContextMeter` pressure ring + composition breakdown + per-turn `StatsLine` (billed/cache-hit %/TTFT/tok/s/wall). grok: context bar + credits. codex: 28-item reorderable statusline |
| 12 | Mouse/keyboards, copy, a11y/i18n | grok / dsh | grok: mouse + clickable hits + verified clipboard delivery (Confirmed/Unverified/Failed) — for TUIs. dsh: the only one with **i18n (zh/en)** + real web a11y (aria-live, reduced-motion). codex: remappable runtime keymap; no mouse at all |

## 3. Features worth borrowing into Odo (ranked, convergence-marked)

| # | Feature (best source) | Conv. | What to take / NOT take | Odo mapping | Cost | Pri |
|---|---|---|---|---|---|---|
| 1 | **Unified background-work pane + persistent "still running" cue** (grok `views/tasks_pane.rs`, `turn_status.rs:still_running_label`) | **3/3** | One registry for commands/monitors/loops/subagents; status line counts running work; click-through; completion notifies once. Do NOT ship the cue before the daemon registry exists (empty chrome) | StatusBar chip + ContextPanel Background tab + daemon task registry (IPC `pending_counts`-style) | M | **P0** |
| 2 | **Attention-ordered workstream cockpit** (grok `docs/user-guide/23-dashboard.md` state-grouped rows + dsh `Rows.tsx:sessionStatuses` pending dot) | **3/3** (K3+DSF rank P0, GLM supplies evidence) | Sidebar rows sorted Needs-input → Working → Idle → Done, per-row current-activity line ("Running: go test 12m"); pending review shows as a chip, **never a focus-stealing popup** (DSF: dashboard peek-answer precedent) | Sidebar workstreams section; daemon per-workstream activity field. Guard: rank only inside the workstreams section — don't hide wiki/memory hierarchy (K3 caveat) | S | **P0** |
| 3 | **Review-decision ledger cells with actor + outcome + timeout** (codex `history_cell/approvals.rs:ReviewDecision{Approved,ApprovedForSession,NotApproved,TimedOut,…}` + actor `User\|Guardian`; GLM corroborate: `auto_review_denials.rs`) | **3/3** (DSF found the seam, GLM/K3 root evidence) | Every review decision renders as a visible ledger row: who decided, what, outcome incl. **TimedOut**; denied auto-decisions stay user-overridable (`ThreadApproveGuardianDeniedAction`). This is the GUI half of the morning audit's P0#1 (Guardian taxonomy + receipts) | LedgerPanel rows + daemon `review_action` journal (already journaled — add render + outcome classes) | S | **P0** |
| 4 | **Durable steering queue + GUI dock** (dsh `QueueDock.tsx` durable journaled queue + per-row edit/delete/steer + busy-Enter pref; grok `queue_pane.rs` auto-drain on turn end; codex `SqliteQueueStore` restart-survival) | **3/3** (priorities P0–P1) | Queue rows editable while the turn runs, auto-send on turn end, "send now" = cancel-and-send chord (grok `InterjectPrompt` — DSF); durability via journal (dsh proves the invariant; codex proves restart survival). The substrate is morning audit P0#3 (`Agent.steer` durable inbox) — this adds the GUI dock | ChatSurface composer dock; daemon queue entity + journal `steer/queued/spliced` events (every splice journaled — same discipline as prompt receipts) | M | **P1** |
| 5 | **Context-pressure meter with breakdown** (dsh `ContextMeter.tsx` ring + system/tools/messages split) | **3/3 feature; priority split** (K3 P0, DSF P2 → record P1) | One glanceable occupancy ring + click-through per-layer breakdown; k3: "the only one-instrument readout" | StatusBar ring + ContextPanel breakdown; daemon computes per-layer token splits (receipt machinery already counts bytes) | S–M | **P1** |
| 6 | **Plan review with per-line-range comments** (grok `plan_approval_view.rs:PlanApprovalViewState`, comments quoted back as `@plan.md:12-18`) | **2/3** (K3 P1, GLM P1-High; DSF inventory-only) | Annotatable approve/request-changes; comments ride the steer back to the agent. Caution (K3): this re-introduces a synchronous gate — keep opt-in per workstream, never default under the ladder | PlanChip → full plan view + comment ranges; daemon steer IPC | M | **P1** |
| 7 | **Graduated remembered trust — start with session-scoped edit grant** (codex `AcceptForSession`; grok `ALLOW_EDITS_SESSION_OPTION_ID` + word-scope editing; static rules = morning audit #4) | **3/3** (all caution) | First rung: "allow all file edits this session" toggle, displayed only *after* a reviewer pass, journaled with actor+scope. Do NOT graft grok's per-tool word-scope system beside the ladder (GLM/DSF fit-risk); do NOT let a writable rule file bypass ratchets (morning audit: file-can-only-tighten) | DiffViewer/MessageBubble action row + SettingsPanel grant view; daemon enforcement in autoland gates | M–L (full word-scope) / S–M (session rung) | **P1** |
| 8 | **Per-turn stats strip** (dsh `StatsLine.tsx`: billed input, cache-read/write + hit %, TTFT, tok/s, wall time) | **1/3+** (K3 P1-High gap-fill) | Dim footer per completed turn; aggregate + per-model tooltip under MoA (K3's attribution caveat) | MessageBubble footer; reuse daemon usage numbers already feeding LedgerPanel | S | **P1** |
| 9 | **Two-level model + effort picker in composer/status bar** (dsh `ModelSelect.tsx` + unroutable block contract) | **1/3+** (K3 P1) | Seat shows current selection and *admits unroutability with a reason before submit*; under MoA default-on, render **panel composition** (per-slot picks), not a single fake "the model" | StatusBar MoA chip + SettingsPanel defaults; daemon model-catalog IPC | S–M | **P1** |
| 10 | **Fork-at-turn GUI (backtrack) + rewind split** (codex `app_backtrack.rs`; grok `/rewind` mutating vs `/jump` read-only viewport jump, Esc restores) | **3/3 feature; priority P2** (backend = morning audit #6 P1) | Message-level "fork to new workstream"; rewind affordances split mutating vs read-only (DSF's `/rewind`+`/jump` find). Memory-scoping rules MUST land first (K3: epoch notes must not cross branches) | MessageBubble action → daemon replay-≤seq into new workstream | L | **P2** |
| 11 | Small S-cost items | per-leg | **Paste-chips** `[Pasted: N lines]` w/ expand (grok, DSF+K3) — ChatSurface composer (S, P2); **compaction disclosure rows** w/ counts (dsh, K3) — ChatSurface timeline, show true context compaction only, learning stays in MemoryPanel (S, P2); **cross-session FTS over the journal** (grok xai-grok-session-search; dsh ships FTS *off* as cautionary) — CommandPalette ⌘K (M, P2); **28-item reorderable statusline** (codex, GLM) — cherry-pick 8–10 items for StatusBar+SettingsPanel (S, P2); **git-action directives → message buttons** (codex `git_action_directives.rs`, GLM) — generalize to daemon-action buttons in MessageBubble (S, P2); **`/loop` + `monitor` streams** (grok, GLM) — needs a "loop without review" mode to avoid flooding the ladder (M, P2) | — | S–M | P2 |

## 4. Explicitly NOT borrowing (3/3 convergent)

| Anti-pattern | Why not for Odo | Evidence |
|---|---|---|
| Synchronous blocking approval modals with **no park path** (all three) | Directly fights the parked-async-review vision; one unanswerable gate kills a long run | GLM E1; DSF E; dsh headless resolves approvals `unavailable` and dies (K3 E6) |
| Presentation-only or modal-only diffs (dsh display-only; codex fullscreen patch gate; grok post-hoc manual) | Odo's DiffViewer + MoA grading is already post-hoc AND machine-scored AND non-blocking — do not regress | GLM E2; K3 #9 |
| Reject-reverts-file against a shared working tree (grok `reject_hunk`) | Clobbers sibling workstreams under concurrency; scope any batch review to worktree/commit-sets | K3 E3 |
| Resume-on-view spawns the agent (dsh cold-session path) | Skimming ten old workstreams must not cost ten live processes; Odo's bootstrap replays the journal without an agent — keep that invariant | K3 E5 (dsh `.agents/notes/…/2026-07-19-gui-layering-and-rpc-protocol.md`) |
| Single-occupant / one-fullscreen-at-a-time surfaces | Odo's concurrent workstream model + always-visible multi-panel layout already beats it | GLM E3; DSF E |
| Hidden modality: double-key state machines (Esc-Esc), unremappable chord stacks | Powerful in a lived-in TUI, undiscoverable in a desktop GUI; keep affordances visible, one-level, remappable (codex `RuntimeKeymap` is the good reference if ever needed) | K3 E4; GLM E4; DSF E (bare-Esc cancel default; number-key peek answers inside scrolling views) |
| One-shot-only approval vocabulary (dsh) | Maximizes synchronous interruptions per autonomy-hour; borrow graduated trust, never zero-recall | K3 E1 (dsh `user-approval/README.md`) |

## 5. What Odo's GUI already does better (3/3 convergent)

1. **Parked async review with machine grading.** DiffViewer Accept/Reject
   lanes + daemon MoA `review_diff` + `autonomyStatus` ladder — no peer has
   post-hoc review that is asynchronous, machine-scored, and non-blocking;
   grok's is manual, codex/dsh gate synchronously.
2. **Cross-workstream, cross-project attention surfacing.** Sidebar badges
   from `pending_counts` over a daemon-per-project fleet — grok's dashboard
   is process-local, dsh is single-occupant, codex is single-primary-thread.
3. **Research-OS panels as first-class surfaces.** WikiBrowser, MemoryPanel
   (proposals + retractions), LedgerPanel, SkillsPanel; peers stop at
   `/usage` modals and per-session stats lines — no curated long-lived
   research artifacts (K3), and Odo adds daemon-driven auto-distill with
   countdown disclosure without owning the trigger (GLM).
4. **Always-visible multi-panel layout + mouse-first affordances** (DSF):
   TUIs report one fullscreen at a time; desktop GUI is the right host for
   continuous monitoring, and Odo separates "command as typed"
   (CommandPalette) from "plan as committed" (PlanChip) cleanly.

## 6. Surprises / unexpected finds (selected)

1. codex records **approval decisions as journaled transcript cells** with
   actor (User|Guardian) and outcomes including `TimedOut` (DSF) — the
   ledger-as-UI pattern, the closest peer to Odo's review receipts.
2. codex `resume_picker.rs` = 7,010 lines (orchestrator-verified ×2 clones) —
   session management as a sub-application.
3. grok treats the **clipboard as a distributed system**: delivery
   verification, native/tmux/OSC52 routes, SSH-forwardable sink (K3).
4. grok scans **competitors' session stores** (Claude/Codex/Cursor) into its
   picker (K3: `xai-grok-foreign-sessions`).
5. dsh's push channel is **two strictly downlink-only WebSockets** (client
   frame → close 1008; no SSE fallback) with full reconnect-rebuild
   discipline (K3).
6. dsh references a TUI composition everywhere but **ships none** — design
   residue for a surface that doesn't exist (K3).
7. grok `RewindMode::{All, ConversationOnly, FilesOnly}` — conversation and
   file time-travel are independent axes (orchestrator arbitration); DSF
   adds the `/rewind` (mutating) vs `/jump` (read-only viewport) split.
8. codex TUI has **zero mouse support** yet ships sixel pets and a
   CSP-sandboxed inline HTML viewer (K3).
9. codex queues unresolved human prompts and **reconciles across clients** —
   an answer from any attached client resolves the FIFO (K3:
   `InterruptManager`).
10. grok's `monitor` tool turns any long-running script's stdout into a
    notification stream with auto-stop volume control (GLM) — agent
    self-monitoring without polling.
11. codex ships a **second standalone ratatui app** (cloud-tasks) sharing the
    app-server protocol — a template for an Odo parked-review-queue surface
    (GLM).

## 7. Mapping to the outstanding ledger (cross-ref morning audit §9)

| Ledger / audit item | GUI-audit interaction |
|---|---|
| Morning audit P0#3 (dsh `Agent.steer` durable inbox → goal-queue park-and-switch) | **Add #4 here** (queue dock + auto-drain + send-now chord) to the same design session — substrate + surface belong in one brief |
| Morning audit P0#1 (Guardian risk taxonomy + receipts on `review_action`) | **Add #3 here** (ledger cells with actor/outcome/TimedOut rendering) — journal schema and GUI row ship in the same wave |
| Morning audit P1#4 (declarative rules file, file-can-only-tighten) | **#7 here** is its interactive face — session-scoped grant toggle first, word-scope deferred until the rules file exists |
| Morning audit P1#6 (turn-fork store op) | **#10 here** is its GUI face — MessageBubble fork action waits on the store op + memory-scoping rules |
| fix-INT W1/W2 (in flight) | No interaction — GUI waves queue after them |
| **NEW GUI wave A (P0)**: background-task registry + StatusBar cue (#1) + attention-ordered Sidebar (#2) + ledger-cell render (#3) | One tri-model design round; #3 rides the morning-#1 schema work; #1 needs the daemon registry substrate |
| **NEW GUI wave B (P1)**: queue dock (#4), context meter (#5), plan line-comments (#6), session-grant toggle (#7), stats strip (#8), MoA panel picker (#9) | Design after wave A; #4 joins the park-and-switch session |

Repo state preserved: no reference repo edited; frozen clones at
`/tmp/harness-src-{k3,glm,dsf}/` (disposable).
