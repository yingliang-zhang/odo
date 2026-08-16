# Odo

Odo (ὁδός) — a personal Research Coding OS.

Named after the Greek root for "way, road, path" — the source of *method*,
*episode*, and *odometer*. Odo is built for a single researcher who relies on
AI coding agents. It provides visible, continuous, lightweight project
development.

## Why Odo exists

Four pain points, each with zero lines of workaround:

1. **Memory fills up** — no single growing memory.md; per-project structured wiki
2. **Context loss on session switch** — conversations are durable journal events
3. **Can't see agent progress** — structured tool events stream into the chat
4. **Too heavy** — Tauri 2 WebView, not Electron; single Go daemon

## Status

M0–M15 complete. 208 commits, ~44K lines (≈29.6K Go incl. tests + ≈14.0K TS/CSS/Rust).

| Milestone | What it delivers | Tests |
|---|---|---|
| M0 Bootstrap | Go daemon, SQLite journal, OMP adapter, worktree plumbing, Tauri shell | 10 Go |
| M1 Visible Loop | Send message → agent runs → diff lands → Accept/Reject → git apply | 14 Go |
| M2 Diff Review | Split diff viewer, settings panel, MoA fan-out, model/adapter selector | 14 Go + GUI |
| M3 Memory Layers | Recall chip on user message, per-workstream wiki, pending-diff counts | 17 Go + AX |
| M4 Distiller + Learner | Epoch distill → wiki note, learner proposes memory rules | 17 Go + AX |
| M5 Curation | Topic pages + index.md rewrite, pin memory, curator pass | 17 Go + 32 E2E |
| M6 Precision + Ledger | Contradiction detection, note retraction, verified metrics ledger | 14 Go + 19 E2E |
| M7 Live Streaming | Block-level streaming via OMP `--mode json`, preview bubble, adaptive poll | 7 adapter + 1 E2E + integration |
| M8 Skills | Skills panel (CRUD), path traversal security, scope selector, BOM/EOF parser | 12 Go + 11 E2E |
| M9 Skill Distillation | Skill distillation + three-tier gating (auto-discard / human-gate / auto-accept) + MoA review | 20 Go + 6 E2E |
| M10 Auto-Distill | Settings UI, idle gate, auto-curate chain, `auto_distill: on_idle` | 44 E2E (full suite) |
| M11 Multi-Project | Sidebar project list, per-project daemon, folder picker | GUI |
| Sidebar Redesign | 48px icon rail, 4 sections, toast viewport, collapse (⌘B) | computer-use E2E |
| GUI Belt A–D | Abort, scroll, textarea, shortcuts, markdown, search, palette, split diff, theme, empty state, a11y | 58 E2E |
| Hardening | 8 tri-model review items (path guard, retraction dedup, CSS var, palette trap) | 3 Go |
| GUI Audit | P0+P1 fixes: CSS tokens, model picker (datalist+chips), diff comments, focus ring, panel resize, tablist, verdict badges | 43 E2E (tri-model MoA reviewed) |
| PR1 CSS Polish | systemBlue accent, SF Pro font stack, flat agent bubble + hairline border, asymmetric user bubble tail, macOS focus ring, motion tokens, reduced-motion guard, frosted vibrancy | 43 E2E |
| PR2 Settings Inspector | Left 160px category sidebar (General/Models/Knowledge) + right detail panel, all fields preserved | 43 E2E |
| PR3 TopBar Declutter | Distill (labeled) + ⋯ overflow menu (Curate/Pin/Wiki/Ledger) + Settings (gear icon) | 43 E2E |
| Diff Line Numbers | Hunk header parsing for old/new line numbers, split-view comment buttons, file:line comment refs | 43 E2E |
| P2 A11y | aria-busy spinners, inline delete confirm (replaces native window.confirm) | 43 E2E |
| Clipboard Paste Fix | save_attachment daemon command for clipboard image paste (webview → base64 → daemon → real path) | 43 E2E |
| M12–M14 Memory + Context Polish | Verbatim replay w/ actionable fold chips, CJK recall, durable todo (journal-backed), cross-workstream matched recall, auto-distill chain, per-layer sha16 receipts + `total_prompt_bytes` | Go + 47 E2E |
| M15 Outcome Loops | `odo skills audit` (receipt×outcome join, flag-only), `odo autonomy audit` (per-class streaks, rung-0 instrumentation, `auto_apply` pref parse), M11 comment truth, OMP memory probe (docs-only) | 15 Go + 47 E2E |
| M16 Auto-Land | Pref-gated (`auto_apply: main`) unanimous-panel landing: protected-path / supply-chain / new-topdir / test-weakening mechanical gates, mandatory `.odo-verify` re-run, 87K-token cost breaker, one shared adversarial prompt for manual+auto review (batch B), `consensusVerdict` unanimity (fail-open fix), journaled `auto_land_blocked` + `actor:"auto_panel"` audit (streak-excluded). Batch B: verify-evidence + visual-class gates, per-leg `base_url`/`thinking_md` journal honesty, autonomy audit settle header | 7 Go + skip-gated live harness |
| fix-INT W1–W7 | Integration audit triage: accept TOCTOU + base_stale_at_land (W1), fold allowlist + cap-drop journaling + send-closure assertion (W2), moa_fs_deny replace→merge ADR-0004 (W4), Guardian risk taxonomy + structured review receipts (W5), goal-queue park-and-switch ADR-0005 — durable per-conversation FIFO (cap 8), 3-site auto-dequeue, journal-derived recovery (W6), OMP output-schema flag check (W7) | Go + 10 W6 tests |

### Planned

- **A1 earned-autonomy ratchet** — rung-0 instrumentation shipped (M15); main-path auto-land shipped pref-gated with unanimous-panel review (M16). Still open: branch-rung landing (`odo/auto-<ws>` batch flow), data-gated on real streak + override-rate numbers from `odo autonomy audit`
- **Tool-bearing skills** — skill directories (`SKILL.md` + `references/` + `scripts/`) scanned and receipted by directory content hash; agent-authorable scripts stay behind the human apply gate (B-strategy-2)
- **Design-MoA for nontrivial tasks** — 3 blind design proposals → consolidator → journaled DESIGN LOCK (human amend/veto) → single implementer → existing MoA review; mechanical fixes stay single-model (B-strategy-2)
- **Auto-register semantics** — projects must not auto-register on connect (real pain observed: probe/scratch dirs got registered during a test run); explicit consent or git-root gate
- **Cross-examiner** — one-shot mid-discussion second opinion at decision points (DEFER until a concrete decision-point pain is demonstrated; `/panel` already covers manual second opinions)
- **Per-run diff lock** — move `ExtractDiff` out of `s.mu` to avoid blocking concurrent conversations during git subprocess calls (DEFER until accept latency is observed blocking another conversation)
- **Split-view comments in split mode** — 💬 comment button exists in both inline and split views; if split-view commenting becomes a daily need, add comment affordance to split view (currently done)

### WONTFIX (removed from roadmap)

- ~~Experiment ledger~~ — redundant for a single-user app; git history + memory/log.md + ledger.md already capture experiment outcomes
- ~~P2 polish (contrast audit, palette fuzzy search, combobox wiring)~~ — contrast passes WCAG AA at normal sizes; palette uses substring filter (sufficient); combobox ARIA already present in CommandPalette
- ~~Vision auto-screenshot finishing layer~~ — premature; core `/vision` route ships with K3 image blocks; the finishing layer needs a demonstrated pain point

### Features

- **Conversation-centric**: every run journals typed events (`user_message`, `agent_text`, `agent_tool_call`, `agent_tool_result`, `agent_done`, `agent_error`, `review_action`, `memory_update`) to an append-only SQLite store
- **Journal tooling** (read-only CLIs, no LLM): `odo journal` folded/range/tail/search replay; `odo recall audit` recall-miss telemetry; `odo skills audit` skill receipt×outcome join + flags; `odo autonomy audit` per-class accept streaks; `odo rules audit` memory-rule receipt×outcome join + harmful/effective flags (journaled + ledger.md sinks; self-improving Wave 1 measure step)
- **Live streaming**: OMP `--mode json` JSONL stream tailed with byte-offset cursor; preview bubble shows in-flight block with pulsing caret; adaptive poll (350ms running / 1500ms idle)
- **Memory architecture**: 6-layer (journal → epoch notes → topic pages → memory.md → user.md → ledger.md), one-way promotion, contradiction detection + retraction
- **Diff review**: unified + split view, Accept applies the diff to the project repo and commits, Reject discards the worktree
- **Sidebar**: 48px collapsed icon rail, 4 sections (Workstreams, Capture, Knowledge, System), ⌘B toggle
- **Command palette** (⌘K): distill, curate, pin, open wiki, settings, switch workstream
- **Chat search** (⌘F): in-conversation text search with jump-to-match
- **Markdown rendering**: agent text renders as markdown with syntax-highlighted code blocks
- **Skills**: global (`~/.odo/skills/`) and project-local (`.odo/skills/`) markdown skills, keyword-matched for prompt injection, full CRUD via GUI
- **Skill distillation**: learner proposes skills from conversation patterns; three-tier gating (auto-discard / human-gate / auto-accept) with MoA review
- **MoA review**: run a diff through N parallel models, results journal as one review_action event
- **Diff comments**: inline 💬 per code line, "Send comments" routes feedback to agent via `send_message`
- **Theme**: dark/light, persisted to localStorage
- **Keyboard shortcuts**: ⌘↵ send, ⌘B sidebar, ⌘F search, ⌘K palette, ⌘, settings, Esc stop/clear

## The shape of Odo

The **journal is the bedrock**: an append-only SQLite store where every typed
event, every injected prompt layer (sha16 receipt + byte counts), and every
verdict lives. Anything derived — memory, skills, autonomy — is rebuildable
from it, and anything injected is attributable ("what exactly did run N see?").

Three pillars compound on top of that single audit trail:

| Pillar | Holds | Today |
|---|---|---|
| **Memory** | context — epoch notes, topics, memory.md/user.md, durable todo, replay | full lifecycle: auto-distill/fold/curate, CJK recall, receipted injection, miss telemetry (`odo recall audit`) |
| **Skills** | procedure — how tasks get done | CRUD + keyword-matched injection + distillation gate + outcome telemetry (`odo skills audit`, flag-only) |
| **Orchestrator** | verdicts → autonomy | journaled accept loop + MoA review; earned-autonomy instrumentation at rung 0 (`odo autonomy audit`) |

The flywheel: receipts × outcomes → sharper injections → cleaner accepts →
earned autonomy → more evidence. Models are external and swappable; the
closed evidence loop is the moat.

## License

Apache License 2.0.

## Development principles

### 1. Every commit traces to a pain point

Before writing any code, state which pain point it addresses. If it's
infrastructure, state the chain: "This store schema supports pain point #2
because epoch data needs to survive crashes." If you can't trace to a pain
point in 3 hops, don't build it.

### 2. Close the loop before hardening it

The thinnest complete loop that closes a pain ships before any hardening of
that loop. A working demo with no attestation beats a rigorously-attested
diff generator that discards its diffs.

### 3. Invisible work is presumptive scope creep

If the user cannot see or feel a subsystem within one milestone of starting
it, it needs explicit re-justification against a pain point.

### 4. Review scales with blast radius

Journal/data-model changes and anything touching the accept→apply path →
independent fresh-context review. Service glue, UI → single review. Spikes →
none.

### 5. Docs are prompts

This repo is built and maintained by AI agents that read the README before
writing code. Naming, status lines, and ADR titles are steering inputs —
review them as such.

### 6. Implementation does not review its own output

A model that writes code cannot reliably review it. Independence = fresh
context, not necessarily different vendor. Author-in-panel is acceptable when
the panel contains a different model (coverage-union makes findings additive).

### 7. Milestone spec gate

No code before a committed `docs/milestones/mN-<slug>.md`:

```
Pain:      <user-observable problem, one sentence>
Demo:      <the exact actions the user will run, and what they must see>
Not built: <the explicit out-of-scope list>
```

A milestone closes only when the user physically runs the Demo and the Pain is
visibly relieved. The orchestrator can never mark its own milestone complete.

## Architecture

```
┌──────────────────────────────────────────────────┐
│  Tauri 2 shell (React + Vite in native WebView)  │
│  Sidebar (48px rail / 4 sections / ⌘B)            │
│  Chat surface (run groups, preview bubble, search) │
│  Diff viewer (unified + split) + Markdown renderer  │
│  Command palette (⌘K) + Settings (⌘,)              │
├──────────────────────────────────────────────────┤
│  Unix socket IPC (typed JSON, preview + streaming) │
├──────────────────────────────────────────────────┤
│  Go daemon                                         │
│  ┌──────────┐ ┌──────────┐ ┌────────────────────┐ │
│  │ SQLite   │ │ Adapter  │ │ Memory Distiller   │ │
│  │ Journal  │ │ (OMP)    │ │ (epoch → wiki)      │ │
│  │ (events) │ │ --mode   │ │ Curator (M5)        │ │
│  │          │ │  json    │ │ Learner (M4)        │ │
│  │          │ │ ↗stream  │ │ Ledger (M6)         │ │
│  │          │ │          │ │ Skill Gate (M9)     │ │
│  └──────────┘ └──────────┘ └────────────────────┘ │
│       │            │                               │
│       │            ▼                               │
│       │     ┌──────────────┐                       │
│       │     │ Worktree     │ (invisible plumbing)  │
│       │     │ (per action) │                       │
│       │     └──────────────┘                       │
│       ▼                                            │
│  ┌──────────────┐                                  │
│  │ wiki/        │ (per-workstream epoch notes)     │
│  │ topics/ (M5) │                                  │
│  │ index.md (M5)│                                  │
│  └──────────────┘                                  │
└──────────────────────────────────────────────────┘
        │
        ▼
  OMP (headless CLI agents)
  --mode json → JSONL event stream (M7)
```

### Design invariants

1. **One durable Go authority owns all state.** Tauri's Rust layer is a thin
   shell; it never holds durable truth.

2. **Hand-synced IPC types.** Daemon↔GUI IPC types are defined in Go
   (`internal/ipc/protocol.go`) and TypeScript (`gui/src/types.ts`) and kept
   in sync by hand. The Tauri Rust layer forwards JSON verbatim. No codegen.

3. **Append-only SQLite journal.** Typed queries, no ORM. Every state
   transition is an append to the journal, not an in-place update.

4. **Streaming is non-blocking.** The adapter tails `output.txt` with a
   byte-offset cursor per `Events()` call; the daemon strips the transient
   preview before journaling. No background goroutine, no WebSocket.

## Build and run

```bash
# Go daemon + tests
go build ./... && go vet ./... && go test ./... -count=1

# GUI (TypeScript + Vite)
cd gui && PATH=~/.hermes/node/bin:$PATH npx tsc --noEmit && npm run build

# Tauri Rust shell
cd gui/src-tauri && PATH=~/.cargo/bin:$PATH cargo check

# Run the app (starts daemon + Tauri dev server)
cd gui && PATH=~/.hermes/node/bin:$PATH npm run tauri:dev

# Real OMP streaming integration test (needs API keys + network)
go test -tags=integration -v -timeout=120s ./internal/adapter/ -run TestRealOMPStreaming
```

### What Odo does NOT have (deliberately)

- No cryptographic attestation (deferred to M1+)
- No sandbox containment (agents run in user's environment)
- No frozen contract pipelines
- No grill/adversarial review system

## Repository and session design

### Authority vs context boundary

| Layer | Authority | Stores |
|---|---|---|
| SQLite journal | Odo (authority) | runs, events, conversations, diffs |
| Markdown wiki | Odo (context) | architecture, decisions, conventions |
| Global prefs | Odo (context) | `~/.odo/prefs.md` — user preferences, model defaults |

## Memory architecture

Memory is layered, scoped, and journal-anchored (full rationale:
[ADR-0003](docs/adr/0003-memory-architecture.md)):

| Layer | Path | Injected? | Holds |
|---|---|---|---|
| journal | `.odo/journal.sqlite` | never (substrate) | everything, full fidelity |
| epoch notes | `wiki/<ws>-epoch-N.md` | selected ≤12 KB | records — narratives, dated decisions |
| topic pages + `index.md` | `wiki/` (M5) | index ≤2 KB always | curated project knowledge |
| `memory.md` | `.odo/memory.md` (M4) | always ≤4 KB | project rules every run obeys |
| `user.md` | `~/.odo/user.md` | always ≤4 KB | global durable principles |
| `ledger.md` | `.odo/ledger.md` (M6) | never (pulled) | verbatim metrics, daemon-written |

Core rules: agents (OMP) never write any memory layer — the distiller
harvests behavioral signals from the journal instead. Rules live in
`memory.md`/`user.md`; records live in the wiki; numbers live in the ledger
with verbatim quotes the daemon verifies mechanically — no LLM in the
metric path. Everything derived is rebuildable from the journal; nothing is
silently truncated or deleted (overflow demotes to an append-only archive).
Every sent prompt carries per-layer sha16 receipts plus `total_prompt_bytes`
and `dropped_seqs`, with cap boundaries rendered as actionable markers
(`odo journal range A B`) — the audit trail for "what did run N see?" is
complete, and its omissions are themselves journaled.
Promotion flows one way up: journal → epoch note → topic page → memory.md →
user.md (a rule seen in 2+ projects earns global promotion).

### Session continuity

When a Hermes session compresses or switches, the project repo is the
authority for resuming work:

1. Read `docs/milestones/` for the current milestone spec
2. Read `docs/adr/` for accepted architecture decisions
3. Read `memory/log.md` tail for recent progress
4. Run `git log --oneline -5` for current HEAD

If it's not in the log, it didn't happen.
