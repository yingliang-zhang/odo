# ADR-0006 — Model-Routing Boundary

Status: Accepted (2026-08-15). Records the routing verdict from
`docs/compare/router-vs-omp-eval-2026-08-14.md` (tri-model 3/3
convergence, verdict B).

## Context

Odo's daemon procures LLM completions through two transports:

1. **OMP (the coding agent CLI)**: a full agent loop with tool access,
   file editing, and multi-turn reasoning. Used for coding tasks.
2. **Direct moa.Query**: a single HTTP POST to the sudo gateway. No
   tool loop (or `QueryWithTools` for a bounded read-only loop). Used
   for "thinking tasks" — one-shot completions that never touch files.

The router-vs-OMP evaluation (3/3 convergence) established which tasks
belong on which transport, and the boundary that keeps the daemon's
memory write authority intact.

## Rule

**Thinking tasks route through direct moa.Query; coding tasks route
through OMP. The write path stays OMP forever.**

| Task | Transport | Prefs flag | Default | Why |
|---|---|---|---|---|
| Coding (diff-producing) | OMP agent loop | — | OMP | Agent needs tool access + file editing |
| Distill (epoch fold) | moa.Query | `distill_via` | `omp` | One-shot completion; no file edits |
| Learner (rule proposals) | moa.Query | `learner_via` | `omp` | One-shot completion; no file edits |
| Curator (topic pages) | moa.Query | `curator_via` | `omp` | One-shot JSON; no file edits |
| Design-MoA (blind legs + consolidator) | moa.Query / QueryWithTools | `design_via` | off | Read-only tool loop + synthesis; no file edits |
| Panel review | moa.Query | — | moa | Always direct (adversarial review needs independence from OMP) |

## Invariants

1. **Daemon owns every memory write.** Regardless of transport, model
   output is inert text until daemon-side parsers and gates pass
   (learner proposals wait for human `apply_memory`; curator JSON passes
   shape, topic-validity, and citation-liveness gates). No agent process
   ever writes a memory layer (ADR-0003 inv1, unmodified).

2. **Default is OMP for distill/learner/curator.** The `*_via` prefs flags
   default to `omp` — opt-in to moa. Unknown values log and fall back to
   OMP. A typo must never silently reroute the memory pipeline.

3. **Every moa-route request body is journaled.** `request_sha16` and
   `request_bytes` on the fold marker (distill), curate marker (curator),
   learner marker (learner), and design_lock event (Design-MoA). Wire-exact
   receipts (R-W1.5).

4. **Truncation fails closed.** A truncated moa response never commits a
   partial fold/note. The daemon logs the failure and skips the epoch.

5. **The panel is always direct moa.** Panel legs never route through
   OMP — the adversarial review must be independent from the coding
   agent it reviews. This is not behind a prefs flag.

## Amendment to ADR-0003

> **Invariant 7 (amended, 2026-08-15):** Distill remains the only
> *cadence* for LM-influenced memory writes. R-W2/W3/W4 change how the
> daemon *procures* those completions (direct moa.Query behind prefs
> flags), not who owns the write. The transport swap neither creates a
> model write path nor moves the cadence.

## Future

- When telemetry confirms the moa route is stable (no truncation spikes,
  no receipt mismatches), the `*_via` defaults flip from `omp` to `moa`.
- The panel transport is not negotiable — it stays direct moa.
- An OMP `--output-schema` flag (if shipped) would enable structured
  verdict output for panel legs; until then, schema-in-prompt + strict
  daemon-side validator is the fallback.
