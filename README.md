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
│  │ memory/      │ (per-project markdown wiki)      │
│  │ index.md     │                                  │
│  │ *.md         │                                  │
│  └──────────────┘                                  │
└──────────────────────────────────────────────────┘
        │
        ▼
  OMP / Pi / Claude Code (headless CLI agents)
```

### Design invariants

1. **One durable Go authority owns all state.** Tauri's Rust layer is a thin
   shell; it never holds durable truth.

2. **Schema-first wire types.** All daemon↔GUI IPC types are generated from a
   single schema source. No hand-synced Go/TS type definitions.

3. **Append-only SQLite journal.** Typed queries, no ORM. Every state
   transition is an append to the journal, not an in-place update.

### What Odo does NOT have (deliberately)

- No cryptographic attestation (deferred to M1+)
- No sandbox containment (agents run in user's environment)
- No frozen contract pipelines
- No multi-model MoA as a built-in runtime feature (M2+)
- No grill/adversarial review system

## Repository and session design

### Authority vs context boundary

| Layer | Authority | Stores |
|---|---|---|
| SQLite journal | Odo (authority) | runs, events, conversations, diffs |
| Markdown wiki | Odo (context) | architecture, decisions, conventions |
| Global prefs | Odo (context) | `~/.odo/prefs.md` — user preferences, model defaults |

### Session continuity

When a Hermes session compresses or switches, the project repo is the
authority for resuming work:

1. Read `docs/milestones/` for the current milestone spec
2. Read `memory/decisions/` for accepted decisions
3. Read `memory/log.md` tail for recent progress
4. Run `git log --oneline -5` for current HEAD

If it's not in the log, it didn't happen.
