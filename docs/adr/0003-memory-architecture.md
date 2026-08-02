# ADR-0003 — Memory Architecture

Status: Accepted (2026-08-02). Complements ADR-0001 (trust posture) and
ADR-0002 (journal schema). Ratified by dual independent reviews (GLM-5.2 +
Kimi-K3) which converged on the same design; divergences listed at the end.

## Context

Odo M0–M2 built the write half of memory (journal + M1 distill → wiki notes)
but nothing reads memory back into agent prompts. The reference system the
user runs daily (Hermes) has a five-layer stack whose failure modes are
known: cross-project MEMORY.md becomes a junk drawer; Hindsight's
every-3-turns extraction is noisy; Basic Memory demands manual upkeep;
experiment ledgers risk LLM-fabricated metrics. Odo adopts a scoped,
journal-anchored architecture instead.

## Layers

| Layer | Path | Scope | Writer | Injected | Cap |
|---|---|---|---|---|---|
| journal | `.odo/journal.db` | project | daemon | never | — |
| epoch notes | `wiki/<ws>-epoch-N.md` | workstream | distiller | selected, ≤12 KB newest-first (M3) | — |
| topic pages | `wiki/topics/*.md` (M5) | project | curator | via index | — |
| `index.md` | `wiki/index.md` (M5) | project | curator | always (M5+) | ≤2 KB |
| `memory.md` | `.odo/memory.md` (M4) | project | distiller, human | always | ≤4 KB |
| `user.md` | `~/.odo/user.md` (M3 reads, M4 learns) | global | learner, human | always | ≤4 KB |
| `ledger.md` | `.odo/ledger.md` (M6) | project | daemon only | never (pulled) | — |
| `memory-archive.md` | `.odo/memory-archive.md` (M4) | project | distiller | never | append-only |

`memory.md` (M4) and the archive live under `.odo/` — gitignored, outside
any agent worktree. The daemon rejects accepted diffs that touch them.

## Invariants

1. **Agents write to no memory layer, ever.** OMP/Pi runs are ephemeral and
   untrusted; their output enters memory only through the journal (observed
   by the distiller), never by direct write or proposal. An agent with a
   memory write path is an unreviewed injection channel into every future
   prompt.
2. **Everything derived is rebuildable from the journal.** Wiki notes, topic
   pages, index.md are derived artifacts: if curation goes wrong, delete and
   re-run the curator from the journal. No fact may live solely in a derived
   layer unless it is user-authored (the user is its own source).
3. **No silent truncation, no silent deletion.** Overflow demotes
   (memory.md → `memory-archive.md`, append-only); contradictions retract
   with a record; the journal logs every move.
4. **No LLM in the metric data path.** Ledger rows are written by the daemon
   from journal events. An LLM may *select* candidate metric lines but must
   quote them verbatim with a source pointer; the daemon verifies
   `quote ∈ referenced payload` and rejects failures with an error event.
   "Verified from logs" is mechanical, not aspirational.
5. **Injection receipt (M4).** Every `user_message` journals the content
   hashes of the injected layers (user.md, memory.md, recalled notes). Every
   later audit starts from this; without it every other failure is invisible.
6. **Injection order = cache-friendly stable prefix:**
   `user.md → memory.md → wiki block → attachments → message`
   (M3 partial: user.md → wiki → message). Churny content goes last.
7. **Distill is the only write cadence.** No every-N-turns extraction
   (Hindsight's noise source). Learning happens at epoch boundaries.

## Routing contract (rules vs records)

- **Rules** (behavior-shaping, imperative): → `memory.md` (project) if every
  run must obey; → `user.md` (global) if true across all projects.
- **Records** (information-bearing, narrative): → wiki epoch notes / topic
  pages. A rule and its originating record may coexist, linked — but the
  same statement is never duplicated verbatim across injected layers.
- **Numbers**: → `ledger.md` (verbatim + source pointer only).
- **Everything**: → journal (the substrate, not a routing choice).

Discriminator test for memory.md vs topic page: does the fact change what
the agent *does* on every run (memory.md), or answer a question *some* runs
will have (topic page)?

## Promotion & demotion

- **Promotion** (one-way up): journal → (distill) → epoch note → (curator)
  → topic page → memory.md → user.md. The user.md promotion candidate is a
  principle observed in **2+ projects'** memory.md files — a concrete
  cross-project recurrence signal.
- **user.md evidence rule:** a principle enters user.md only from (a) an
  explicit user statement ("always", "never", "I prefer") or (b) a recurring
  REJECT-comment / steering theme. Single ambiguous signals stay project-level.
- **Demotion:** entries carry a last-reaffirmed epoch; overflow evicts
  least-recently-reaffirmed to `memory-archive.md`. Contradiction with a new
  epoch triggers retraction-with-record, surfaced to the user.

## Phases

- **M3** (frozen): user.md injection (read-only) + wiki recall ≤12 KB +
  read-only wiki browser (user.md pinned row) + visibility pack.
- **M4 — Learning:** user.md auto-write (evidence-constrained) + project
  memory.md (batch review → auto when accept-rate > 90% for 3 epochs) +
  injection receipt + memory-changed chip + archive. Wiki stays
  per-workstream; project-wide recall arrives here.
- **M5 — Curation:** curator pass rewrites topic pages from the FULL set of
  epoch notes (never incrementally from the previous topic page —
  generation-2 rule, prevents confabulation drift) + `index.md` ≤2 KB
  always-injected + mandatory `(epoch-N)` citations per bullet (uncited
  bullets flagged in the browser) + pin affordance ("remember: X" verbatim
  hoover). Wiki editing UI stays deferred (curator owns topic pages; humans
  own memory.md and pins).
- **M6 — Precision + Ledger:** keyword/topic-selected recall, payload
  extended to `{path, matched_terms}` (answers "why was this recalled?") +
  pull-based recall via a local CLI (`odo wiki read <page>` — agents are
  coding CLIs with shell access; no MCP plumbing) + `ledger.md` daemon rows
  with substring verification + distiller contradiction reports.
- **Rejected/deferred:** local embeddings/vector stores (violates
  inspectability, near-zero win at this scale; revisit only if composed
  index+pull demonstrably fails at scale); Hindsight-cadence extraction;
  per-entry accept/reject gates (theater — a batch-accepted queue is worse
  than no gate).

## Detection metrics (failure → first signal)

1. **Injection rot** (stale junk in always-injected files quietly
   mis-steering runs): track REJECT rate + steering-correction rate per
   epoch against injected-token count. Journal already has the events.
2. **Curator confabulation** (topic pages drifting from their sources):
   uncited-bullet count, keyword overlap across topic pages, contradiction
   checks in the curator pass itself.
3. **Gate theater** (memory.md review queue rubber-stamped or abandoned):
   accept rate > 90% for 3 consecutive epochs → switch that file to
   auto-write; review rate collapse → batch harder or auto-write
   REJECT-derived constraints first.

## Model divergences (arbitrated)

| Point | GLM-5.2 | Kimi-K3 | Resolution |
|---|---|---|---|
| memory.md write | batch review at epoch boundary | auto-write + journaled diff | Phase in: batch review first, auto when accept-rate metric shows theater (3 epochs > 90%) |
| Recall precision | pull-based first | index-first, then composed index+pull | K3's path: M5 index → M6 pull via plain CLI (coding agents already have shells) |
| Ledger storage | SQL view over journal (no editable file) | `ledger.md` file, daemon-written | File form (browsable/greppable), but daemon-only writer + substring verification keeps GLM's un-editable-by-LLM property |
| Embeddings | defer until pull fails | cut outright | Deferred-and-cut: documented as rejected; revisit trigger recorded above |
