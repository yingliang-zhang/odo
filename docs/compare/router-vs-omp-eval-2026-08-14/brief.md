# Frozen Brief — Tri-Model Evaluation: Odo direct-to-router vs OMP-centric

You are analyst **__LEG_ID__**, one of three independent analysts evaluating an
architecture-boundary question for Odo. Your output is consolidated with two
other models' analyses by an orchestrator. Ground every claim in files you
actually read (cite `path:Symbol`); mark unverified cells "not verified".

## HARD TIME DISCIPLINE (learned from tonight's failures)

- Budget total: ~35 min wall clock. Gather ≤20 min, then WRITE the report no
  matter what. An incomplete report with honest gaps beats no report.
- Do NOT spawn subagents. Do NOT run builds/tests of any repo. `omp --help`
  and greps/reads are fine.
- The final assistant message must START with "## A. Recommendation" and
  contain the complete A–F deliverable.

## The question

Odo is a personal Research Coding OS (Tauri 2 WebView + React + single Go
daemon at `~/Projects/odo`). It already touches models in TWO ways:

1. **OMP adapter** (`internal/adapter/omp.go`): spawns headless OMP CLI agent
   processes for worktree editing runs; 5-verb contract
   (`adapter.go:Adapter` — Start/Send/Events/Cancel/Close); M7 streaming via
   OMP `--mode json` JSONL tailing with byte-offset cursor.
2. **Direct router client** (`internal/moa/client.go`): a hand-rolled
   Anthropic-Messages client over the Sudo gateway
   (`https://coding.sudoai.cc/anthropic`) for "thinking tasks (review, audit,
   design, research)" — including `QueryWithTools`, a READ-ONLY tool loop
   (16 rounds cap, daemon-side scoped executor in `internal/ipc/fstools.go`,
   every executed call journaled). It encodes hard-won quirks: output-budget
   escalation ×2 to per-model cap, 900s timeout floor + budget/120s headroom,
   thinking-block verbatim replay (kimi signature 400), stop_reason
   whitelist, usage capture, per-model modelspec budgets.

The user asks: **does it pay for Odo to (further) adopt direct-router model
calls — e.g. for context-management actions (distill/compaction summaries,
mid-run context repair) and MoA consolidate actions — versus basing
everything on OMP as the core runtime?**

## Ground truth to verify first (anchors)

- Call sites: direct moa usage concentrates in `internal/ipc/server.go` (16
  refs) + `internal/ipc/fstools.go` (4 refs); `adapter.Start` in
  `internal/ipc/server.go` + `internal/ipc/settle.go` (verify re-run path).
- Odo invariants (README.md §Design invariants + §The shape of Odo): one Go
  durable authority; append-only journal; every sent prompt carries per-layer
  sha16 receipts + total_prompt_bytes + dropped_seqs; memory/skills/
  orchestrator pillars; models "external and swappable"; `odo autonomy audit`
  evidence loop. Roadmap: Design-MoA (3 blind proposals → consolidator →
  DESIGN LOCK → single implementer), A1 earned-autonomy ratchet, fix-INT
  waves in flight (working tree has UNCOMMITTED W1 edits — read files as-is;
  moa/client.go's 900s base is one of them).
- Adapter realities the legs should assess honestly: OMP as a black box —
  Odo cannot verify what OMP actually sent the model (receipts cover Odo's
  injected layers only, not OMP-internal assembly); compaction inside OMP is
  invisible to the journal; tonight's observed failure modes: print-mode
  recap-only exits (report recoverable only from session JSONL), an
  exact-UUID resume that exited instantly producing nothing, and a 900s
  timeout mid-gathering. Counterpoint reality: OMP provides the managed
  agent loop — edit tools, shell, retries, `--thinking` effort, provider
  routing (`--model` routes), session persistence — that Odo would otherwise
  reimplement; its streaming JSONL powers Odo's M7 preview bubble;
  `~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh` shows the
  operational machinery (timeouts, exact-UUID resume, hard-cap concurrency).
- Prior audits (READ them — shared context, not contamination):
  `docs/compare/harness-tri-model-audit-2026-08-13.md` (esp. §5 #2
  model-visible⟺logged assertion, #5 structured verdict output "depends on
  OMP exposing a flag", #9 compaction ownership) and
  `docs/compare/harness-gui-tri-model-audit-2026-08-13.md` (how the three
  harnesses split direct-LLM vs agent-loop surfaces themselves).

## Options to evaluate (you may refine/add)

- **A. Formalize the status-quo boundary**: OMP = write-capable agent runs;
  direct moa = thinking tasks. Write the boundary rule into ADR; fill the
  gaps (below) without changing topology.
- **B. Expand direct-router deliberately**: add daemon-side direct calls for
  (i) MoA consolidate (Design-MoA synthesizer), (ii) context-management
  actions (distill/compaction summaries, context repair), (iii) read-only
  tool-loop runners for design/audit legs (exists — productize), each with
  EXACT journaled requests/responses making "model-visible ⟺ logged"
  a daemon-enforced invariant on those paths.
- **C. OMP-core everything**: retire moa direct; all completions via OMP.
- **D. Stay OMP-topology but invest in OMP flags upstream** (structured
  output schema, exact-prompt echo/verifiable receipts, steering depth)
  before expanding either way.
- **E. Full direct write-runner**: own the entire agent loop (edit/shell
  tools in-daemon) — the codex/grok/dsh shape — at rebuild cost.

## Evaluation axes (all required)

Receipts exactness (can the daemon ASSERT visible⟺logged?); context
ownership (compaction points journaled, repair possible, mid-run steering
depth); consolidation cost/latency/flexibility (spawn-agent vs one HTTP
call); harness reimplementation scope (edit/shell/verify/policy tools,
streaming protocol, effort flags); streaming UX cost of leaving OMP's
`--mode json`; session durability/resume (OMP sessions vs none for direct);
parse/verdict reliability for MoA (free-form vs schema-constrained);
credential/security surface (SUDO key in-process vs subprocess);
maintenance/upstream-drift (OMP improves for free; quirk-tail already paid
on the moa side); fit to roadmap items (Design-MoA, A1 ratchet, GUI Wave A
background-task registry) and to "single researcher, lightweight" principle;
failure-mode comparison (black-box JSONL forensics vs journaled exact
requests).

## Deliverable (required structure)

```
## A. Recommendation  (one option as primary; name the runner-up and the trigger that would flip it)
## B. Option-by-axis matrix  (A–E × axes above; terse cells)
## C. The boundary proposal  (EXACT list: which model-touching action → which mechanism, incl. today's calls)
## D. Phased plan  (if expansion recommended: waves with cost S/M/L and journal/ADR consequences)
## E. Risks & failure modes  (per option top-2, honest)
## F. What would change my mind  (falsifiable conditions per your recommendation)
```

Keep prose tight; tables over walls of text.

## Constraints

- READ-ONLY everywhere. Do NOT modify `~/Projects/odo` (another session has
  uncommitted work there). Do NOT call live model APIs.
- Never print secret values (API keys, tokens); cite env var NAMES only.
- Cite `path:Symbol` for every load-bearing claim; "not verified" > guess.
