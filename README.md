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

Pre-v0.1. Auto-development in progress.

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
│  Chat surface + tool ticker + stuck indicator     │
│  Diff viewer + model/adapter selector              │
├──────────────────────────────────────────────────┤
│  Unix socket IPC (typed JSON)                      │
├──────────────────────────────────────────────────┤
│  Go daemon                                         │
│  ┌──────────┐ ┌──────────┐ ┌────────────────────┐ │
│  │ SQLite   │ │ Adapter  │ │ Memory Distiller   │ │
│  │ Journal  │ │ Runner   │ │ (epoch → wiki)      │ │
│  │ (events) │ │ (OMP/Pi) │ │                    │ │
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
  OMP / Pi / Claude Code (headless CLI agents)
```

### Design invariants

1. **One durable Go authority owns all state.** Tauri's Rust layer is a thin
   shell; it never holds durable truth.

2. **Hand-synced IPC types.** Daemon↔GUI IPC types are defined in Go
   (`internal/ipc/protocol.go`) and TypeScript (`gui/src/types.ts`) and kept
   in sync by hand. The Tauri Rust layer forwards JSON verbatim. No codegen.

3. **Append-only SQLite journal.** Typed queries, no ORM. Every state
   transition is an append to the journal, not an in-place update.

### What Odo does NOT have (deliberately)

- No cryptographic attestation (deferred to M1+)
- No sandbox containment (agents run in user's environment)
- No frozen contract pipelines
- No multi-model MoA review — **shipped in M2** (`review_diff` IPC + DiffViewer panel)
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
| journal | `.odo/journal.db` | never (substrate) | everything, full fidelity |
| epoch notes | `wiki/<ws>-epoch-N.md` | selected ≤12 KB | records — narratives, dated decisions |
| topic pages + `index.md` | `wiki/` (M5) | index ≤2 KB always | curated project knowledge |
| `memory.md` | `.odo/memory.md` (M4) | always ≤4 KB | project rules every run obeys |
| `user.md` | `~/.odo/user.md` | always ≤4 KB | global durable principles |
| `ledger.md` | `.odo/ledger.md` (M6) | never (pulled) | verbatim metrics, daemon-written |

Core rules: agents (OMP/Pi) never write any memory layer — the distiller
harvests behavioral signals from the journal instead. Rules live in
`memory.md`/`user.md`; records live in the wiki; numbers live in the ledger
with verbatim quotes the daemon verifies mechanically — no LLM in the
metric path. Everything derived is rebuildable from the journal; nothing is
silently truncated or deleted (overflow demotes to an append-only archive).
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
