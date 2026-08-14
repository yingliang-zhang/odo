# Tri-Model Evaluation (2026-08-14): Odo direct-to-router vs OMP-centric

> Third tri-model run of the 2026-08-13/14 harness session. Question: should
> Odo (further) adopt direct-router model calls — for context management and
> MoA consolidations — or stay OMP-centric as its core runtime?
> Consolidated from a blind-sealed tri-model MoA run. Raw legs + frozen
> brief: `./router-vs-omp-eval-2026-08-14/`.
>
> **Related:** `harness-tri-model-audit-2026-08-13.md` (architecture),
> `harness-gui-tri-model-audit-2026-08-13.md` (GUI/interaction),
> `moa-client-vs-harness-audit-2026-08-14.md` (client quality — inserts a
> resilience wave ahead of this plan's distill migration).

## 0. Provenance

| Leg | Model | Output | Notes |
|---|---|---|---|
| 1 | kimi-k3 (--thinking max, 900s) | 17.8K | Print-mode recap-only exit; full report recovered from session JSONL by section-header marker |
| 2 | glm-5.2 (--thinking max, 900s) | 21.7K | Complete; contributed the tool-free migration gate + fix-INT collision warning |
| 3 | deepseek-v4-flash (--thinking max, 900s) | 15.9K | Complete; contributed the "4th model call" framing + ADR-0003 wording note |

Orchestrator mechanical verification (not model judgment):
**distill is tool-free** — `distillPrompt` (server.go:3587) contains zero
read/glob/explore directives; events render inline (user/agent text
verbatim, tool calls/results as byte-count tombstones); `runOneShot`
targets a throwaway tmpDir with nothing to explore. `learnerPrompt` /
`curatorPrompt` are pure functions of daemon-held data. GLM's migration
gate passes. Additional fact: `runOneShot`'s own comment records the
precedent — *"review migrated to moa.Query in D5."*

## 1. Verdict (3/3 convergent)

**B — expand direct-router deliberately; the write path stays on OMP
forever.** Runner-ups: A×2 (formalize the status quo), D×1 (invest in OMP
upstream flags — all three agree D is a parallel companion, never a
prerequisite: the peer harnesses' posture is no-external-PRs, leverage
unproven).

**C (OMP-core everything) and E (full direct write-runner) rejected 3/3:**
C regresses every measured axis (N-leg MoA = N subprocess spawns; receipts
regress to black-box; the paid quirk tail is retired). E rebuilds a
codex/grok/dsh-scale harness (codex session management alone = 7,010
lines, verified) and violates "single researcher, lightweight."

## 2. The reframe (3/3-confirmed discovery)

The current boundary is NOT "OMP = write, moa = think." Three pure
thinking tasks — **distill** (`runDistillAgent` → `runOneShot`,
server.go:3491/3502), **learner** (learner.go:545), **curator**
(curator.go:573) — pay full OMP agent-spawn cost (tmpdir + wrapper +
session dir + 200ms polling + 10-min timeout) for a single completion,
using none of OMP's loop. Meanwhile review/panel/vision/skill-gate already
proved the direct path works. This task is finishing a migration the
codebase already started, not opening a new front.

Three decisive arguments:

1. **Receipts asymmetry.** On the moa path the daemon constructs the
   entire wire body (`messageRequest`) — model-visible ⟺ logged is
   byte-enumerable. On the OMP path the daemon hands over a prompt file;
   OMP's internal assembly escapes the (shipped, fail-closed)
   `assertPromptReceipts` closure, which the exemption ledger
   (server.go ~1026-1040) concedes.
2. **The observed failure class targets exactly these one-shots.**
   Print-mode recap-only exits (recovery only via session JSONL),
   exact-UUID resume exiting instantly, 900s mid-gather kills — all
   observed live on 2026-08-13/14. The memory pipeline (Odo's moat) rides
   OMP's weakest mode. A learner failure degrades silently
   (learner.go:~527).
3. **Everything B needs is already paid for.** Budget-escalation ledger,
   900s timeout floor, thinking-block verbatim replay, stop-reason
   whitelist, scoped read-only executor, fence-tolerant parsers — all in
   `internal/moa`, `internal/ipc/fstools.go`, `internal/modelspec`.

## 3. Boundary rule (-> ADR-0005)

> **Side effects in a worktree -> OMP. Reads returning text to the daemon
> -> moa. Every moa call journals a verifiable exact-request receipt.**
> Derived test: inputs held byte-complete by the daemon + cannot change
> files => direct.

| Action | Today | Ruling |
|---|---|---|
| chat run / A2 continuation / auto-revise round | OMP | **OMP** (agent loop value: tools, streaming, sessions) |
| review_diff / auto-land panel / /panel / /vision / skill gate | moa direct | direct (unchanged) |
| **distill / learner / curator** | OMP one-shots | **migrate to `moa.Query`** (gate passed; parsers/vet untouched) |
| Design-MoA consolidator | — | **build on `moa.Query`** + journaled `design_lock` event; the missing "4th model call" per the `consensusVerdict` comment (DSF) |
| Design-MoA blind proposal legs | — | **build on `moa.QueryWithTools`** (productize the E1 read-only loop; shared fs-executor package) |
| Design-MoA implementer | — | OMP worktree -> existing MoA review |
| context-summary verbs | distill-side | summaries direct; **compaction trigger/execution stays OMP** (upstream backlog; journal `compaction/*` when emitted) |
| run-level repair steering | — | **OMP `adapter.Send`** (DSF arbitration: summary generation direct; steering a live run stays adapter-owned) |
| ledger metrics / contradiction / audits | daemon mechanical | **never route through models** (deterministic-metrics moat) |

## 4. Phased plan (wave-order dissent reconciled)

K3 ordered distill-first (quick win); GLM ordered distill-last (fix-INT
collision risk + gate); DSF ordered consolidator-first. Consolidated:

| Wave | Content | Cost | Constraints |
|---|---|---|---|
| R-W0 | ADR-0005 boundary rule + upstream asks (exact-prompt echo, `--output-schema`, compaction events) — D carried in parallel, never gating | S (doc) | user approval of this evaluation |
| R-W1 | **moa resilience shore-up** (from the client audit: bounded retry + typed errors + usage surface) | S | before any migration — inserted by `moa-client-vs-harness-audit-2026-08-14.md` |
| R-W1.5 | receipts closure fill: panel/review payloads gain `request_sha16` + `request_bytes` (exact moa requests journaled) | S | **after fix-INT W1/W2 land** (GLM's collision caution wins; K3's distill-first recorded as dissent) |
| R-W2 | distill -> `moa.Query` behind prefs dark-launch flag `distill_via: omp`; timeout derived from `requestTimeout`; explicit policy for worst-case 1446s vs current 10-min `distillTimeout` | S | R-W1 landed; modelspec entry for the distill model (client audit precondition) |
| R-W3 | learner + curator -> `moa.Query`; ADR-0003 invariant-7 wording amendment (daemon's own cadence != agents writing memory — DSF) | S | R-W2 telemetry clean |
| R-W4 | Design-MoA: consolidator (`moa.Query`, `design_lock` event, truncations strict) + blind legs (`QueryWithTools`, round-cap decoupled, per-round context accounting, fs-executor root param) | M | R-W1–R-W3 |
| R-W5 | Deferred: mid-run context-repair summaries (no demonstrated pain — README principle 3); adopt OMP flags if upstream ships them | — | conditional |

Rollback per wave = one flag / one function (call sites already isolated
behind `runDistillAgent` / `runLearner` / `handleCurate`).

## 5. Risks (B, honest)

1. **Single-gateway concentration widens** — but already near-total (chat
   runs route the same sudo provider); W2 ships a dark-launch flag.
2. **Quirk tail grows** — new models on direct paths may introduce new
   quirks; modelspec + the escalation ledger keep them falsifiable and
   bounded.
3. **Timeout semantics shift** — moa worst case (900 + 65536/120 = 1446s)
   exceeds the 10-min `distillTimeout`; needs an explicit deadline-policy
   decision in R-W2 (K3 unique risk).

## 6. Falsifiable flip conditions (3/3 merged)

- OMP ships echo + schema + compaction events -> flip to A+D, moa retreats
  to /panel//vision.
- Gateway SLO/protocol breaks twice in a quarter -> W2/W3 roll back behind
  the flag.
- Journaled `output_tokens`/`escalations` show direct materially worse
  wall-clock/cost than the OMP shape it replaced -> data decides (K3's
  falsifiable-ledger discipline).
- Design-MoA proposal legs empirically need to write scratch artifacts
  (beyond the 16-round read-only loop) -> legs move back to OMP; the
  consolidator stands.

## 7. Honesty notes

- K3's leg was recovered from session JSONL after a print-mode recap-only
  exit (same failure class the recommendation retires for Odo's own
  one-shots — the day's most pointed self-demonstration).
- DSF's text carries V4-flash formatting noise; its structural findings
  (boundary rule, waves, risks) match K3/GLM; precise citations taken from
  K3/GLM.
- Legs' line numbers drift (the working tree was being hot-edited by the
  fix-INT session); consolidation cites symbols; the tool-free gate was
  verified against the live tree (server.go:3491/3502/3587,
  learner.go:545, curator.go:573).
