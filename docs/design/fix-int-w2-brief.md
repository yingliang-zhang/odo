# Frozen Brief — Tri-Model Reconstruct-then-Design: fix-INT Wave 2 (journal/memory semantics)

## 1. Context

Odo (`~/Projects/odo`): Go daemon = single durable authority over an append-only
SQLite journal; memory = 6 layers with per-layer sha16 receipts on every sent
prompt; distill folds journaled events into wiki epoch notes; review bookkeeping
was massively extended by M18 (settlement ladder: rows like `auto_revise_round`,
`auto_land_blocked`, `moa_review`, accept/reject with `actor:"auto_panel"`).

This wave fixes journal/memory semantics — four items. Their lineage: three are
M17-audit P1 leftovers ("fold-whitelist `actor:auto_panel`", "cap-drop
journaling", "#14 memory.md/pins.md materialization"); the fourth comes from a
2026-08-13 comparative harness audit (deepseek-harness's "model-visible ⟺
logged" runtime invariant). **The original audit finding texts no longer exist
on disk — the items are named-only in `docs/milestones/m17-chain-repair.md:202`.**
Therefore the FIRST phase of your work is reconstruction: from the code anchors
below, state precisely what the defect is. If your reconstruction differs from
the sketch I give, use YOURS (label it a correction, cite the evidence). Then
design. Do not design against my sketch if the code disagrees.

HEAD for reference: `86b2351` (Wave 1 landed). Working tree must stay
untouched — read-only.

## 2. Items + anchors (verify, then design)

**Item 1 — fold whitelist `actor:auto_panel`.**
Anchors: `internal/ipc/server.go` `distillRender` (~3265): review_action and
memory_update render as one-liners with NO actor distinction;
`isAdvisoryAgentText` (~3334) excludes only /panel /vision agent_text from the
fold input. M18 invented `actor:"auto_panel"` bookkeeping — those rows now flow
into epoch-note prompts as if they were user-level signal. Sketch of the
defect: panel-internal bookkeeping pollutes/distorts epoch notes (the note
earns verdicts, not transcripts — but which auto_panel rows earn a place?).
Question includes: should `actor:"auto_panel"` rows enter the fold whitelist
at all, per-action (auto_revise_round vs accept vs auto_land_blocked)? And does
the distill-marker typing interplay (`foldBoundary`, recall.go:30, keys on
action:"distill") need actor isolation?

**Item 2 — cap-drop journaling.**
Anchors: `capEvents` (~server.go:3240) writes omission markers into PROMPTS
only; replay `dropped_seqs` is recorded in the per-prompt receipt
(server.go:663 `rp["dropped_seqs"]`; also ~1873, ~2080). Sketch: a drop is
receipt-only — once the prompt is gone, the journal alone cannot prove what was
omitted from which run (the "model saw X" claim is re-derivable only if the
derivation inputs survive). Verify whether omission facts are in fact
journal-reconstructable today (receipts ON the user_message payload ARE
journaled... or aren't they? — check precisely which receipt lives where), then
design the minimal journaling that closes the gap honestly (new payload fields
vs new event kind vs nothing-if-already-covered).

**Item 3 — memory.md / pins.md materialization.**
Anchors: server.go ~409–460 (send path reads `.odo/memory.md` + `.odo/pins.md`
fresh from disk every prompt; receipts hash them, ~762); contrast
`docs/milestones/m12-memory.md:73` (todo state: snapshot per merge + journal
scan — no derived file). Sketch: rule content lives only in mutable files; the
journal receipts the hash but never the bytes, so "which rules were active at
seq N" is unreconstructable after a hand edit. Design a materialization
strategy (snapshot-on-change-detect vs snapshot-at-distill/apply boundaries),
weighing journal size against reconstructability.

**Item 4 — "model-visible ⟺ logged" runtime assertion.**
Anchors: the prompt assembly + receipt block (server.go ~600–770:
memoryLayers → receipt map with sha16 per layer, total_prompt_bytes,
dropped_seqs). deepseek-harness asserts at runtime that anything reaching a
model request is reconstructable from the log. Odo has receipts but no
assertion — a layer silently injected without a receipt entry would go
unnoticed. Design the daemon-side pre-send assertion: what exactly is checked
(every injected block has a matching receipt; receipt bytes == injected bytes),
fail-closed vs warn+journal (decide, with reasoning — this is a contract
decision), and where it sits so ALL model-call paths (run send, review legs,
distill, panel) either pass through it or are explicitly exempt.

## 3. Constraints

- Append-only journal is holy. New event kinds / payload keys are contract
  changes — allowed but must be flagged and proven consumer-safe
  (check cmd_*, gui render switches, autonomy/ledger readers for each new key).
- Live journal compat: today's ~/.odo journal must not break a build that ships
  this. No migrations that rewrite history.
- Receipts discipline: any new injected content needs a sha16 receipt; any new
  omission needs accounting. Extension must be additive.
- Minimal diffs, existing test harness style. No new dependencies.
- My Wave-2 scope covers these four items ONLY — do not design fold-boundary
  redesign, distill redesign, or GUI work.

## 4. Output format (per item)

1. **Reconstructed defect** — verified statement (or CORRECTED, with evidence).
2. **Options** — ≥2 with trade-offs.
3. **Recommendation** — exact symbols/files; new events/keys flagged
   `[CONTRACT]`; fail-closed/open rationale.
4. **Test plan** — exact function names + assertions.
5. **Risks.**
Final section: consolidated diff sketch (file-by-file).

## FINAL-MESSAGE CONTRACT (flash variant)

Tools calls are expected and many. The FINAL assistant message must contain the
COMPLETE four-item deliverable, and must start with "# Wave-2 Design Proposal".
Do not stop at evidence notes.
