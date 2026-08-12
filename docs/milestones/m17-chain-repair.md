# M17 — Chain repair: the five accountability fixes

Closes every memory/skills chain regression surfaced by the 2026-08-12
accountability audit (3-axis × 3-model). All pain numbers below were measured
against the live project journal; the two deferred pain classes (P0-4
fstools/skill-scan confinement, P0-5 fold-whitelist) ride the next batch,
which already touches those files.

> **F-numbering map** (audit → code comments): audit F1 = code `M17 F1`
> (render filter); audit F2 = code `M17 F2` (contradiction guard); audit
> F2b (unretract CLI) appears in code comments as `M17 F3`; audit F3/F4
> (curate age leg, ghost citations, retraction-filter input) appear as
> `M17 F4`; audit F5 = code `M17 F5` (skills-audit actor filter). Code
> comments were not renumbered; this paragraph is the index.

## Pain

1. **P0-1 auto-distill dead — 25 consecutive skips, 0 fires.** The window
   crossed the 256 KiB prompt cap inside a single run (thinking/tool payloads
   dominate the byte count), every evaluation chose
   `window_exceeds_prompt_budget`, and the skip never re-armed.
2. **P0-2 contradiction guard falsely retracted 6 of 7 epoch notes.**
   Journal seqs 5144–5149: a post-reset note's scope disclaimer
   ("seq 1–4907 was omitted … is not covered") candidated on a signal token,
   and the ≥1-overlap rule then retracted any older note sharing **a single
   content-free salient token** — six notes, six one-word coincidences. (Note
   on mechanism: "not" is in recall's `stopWords`, so it never joined either
   side's overlap set; its only role was gating the candidate.) No
   `unretract` emitter existed anywhere to repair the damage.
3. **P0-3 auto-curate constructively unreachable** — firing required ≥ 4
   distill markers since the last passing curate, so a never-curated project
   could never start — **and 12 hand-written topic pages carried 13 ghost
   epoch citations** (8 epochs absent on disk AND absent from the journal's
   distill markers, i.e. epochs that never existed, including already-deleted
   functions like `CreateWorktreeOnBranch`), silently, on every recall.
4. **P1-11 skills-audit self-inflation** — `accept{actor:"auto_panel"}` rows
   (live proof: seq 6668, diff 17) counted as human approvals in both
   per-skill rows and the skill-free baseline, so a flagged skill's
   rejection-rate denominator could be inflated by the system's own
   pipeline. Every panel-ladder design leg called this the ladder's
   load-bearing prerequisite.

## What ships

### F1 — distill render is the single source of truth (P0-1)

`distillRender` / `distillRenderSize` (internal/ipc/server.go, ~3203/3281;
unit tests in learner_test.go, auto-fold coverage in auto_test.go) own the
"what the model actually receives" transform for every auto-distill
consumer:

- `agent_thinking` → `[thinking omitted — N bytes]` tombstone;
- `agent_tool_call` → `[args omitted — N bytes; tool: <name>]` tombstone
  (write/edit calls journal full file contents as args — verbatim they
  re-create the over-cap shape in miniature, and capEvents' newest-first
  fold would evict user messages to keep file contents. The tool NAME is
  kept: which side of the codebase a run touched is fold signal);
- `agent_tool_result` → `[result omitted — N bytes; tool: <name>]`;
- `review_action` / `memory_update` → one-line action/verdict / layer/cause
  forms (a deliberate granularity loss: per-model review rationales and
  record details no longer reach the fold input — the note earns verdicts,
  not transcripts);
- advisory `agent_text` (/panel, /vision) → excluded entirely (mirrors
  eligibility);
- everything else → verbatim.

`measureWindow` and `capEvents` both consume `distillRenderSize`, so
eligibility, urgency, coverage honesty, and the marker's `window_bytes`
all speak the same unit: the RENDER bytes. For verbatim kinds the size is
a stable over-estimate (`len(type)+len(payload)+64` — exactness traded for
never materializing a multi-KB payload just to count it); for tombstoned
one-liners the render is measured exactly. An over-cap window no longer
hard-skips: it folds its renderable tail with the same declared-omission
form the manual path always used (`TestAutoOverCapFoldDeclaresOmission`).
Retired reason: `window_exceeds_prompt_budget`; the `blocked_reason`
disclosure field went with it (protocol.go). Unit contract:
`TestDistillRenderFilter` (includes the agent_tool_call case),
`TestDistillPromptOmission`.

### F2 — contradiction guard: overlap ≥ 2 non-signal tokens (P0-2)

Signal tokens ("not", "no", …) are now a candidate GATE ONLY — they never
join the salient-overlap set, and a flag requires ≥ 2 shared non-signal
salient tokens. The production regression is pinned by
`TestContradictionGuardM17Production`; the conflict/no-conflict/no-double-
decline fixtures hold. **Deliberate tradeoff (false negatives):** a
genuine single-keyword contradiction (sharing exactly 1 non-signal token,
e.g. "Auth switched from JWT" vs "uses JWT") no longer auto-retracts — the
asymmetry favors the fix (a missed retraction leaves a stale note; a false
one kills a live note), and the repair path below is the mitigation.

### F2b — `odo unretract <note-basename>` (P0-2 repair path; code labels: M17 F3)

The recall derivation has consumed `memory_update{layer:"note",
cause:"unretract"}` since M6; NOTHING ever emitted one. The new CLI is the
emitter: it opens the project journal through `store.Open` (WAL coexists
with a live daemon), resolves the note's workstream to its ACTIVE
conversation (the same conversation recall derives from), validates
`<workstream>-epoch-<N>`, and journals `<name> unretracted by user …`.
Idempotent; the note file itself is never touched (epoch notes are
append-only). Fixtures: `TestUnretractCLIRoundtrip`,
`TestUnretractCLIValidation`. GUI follow-up: the wiki-browser's retracted
badge is additive-only — it keeps showing "⚠ retracted" after an
unretract until restart (recall/curate/age paths heal immediately;
documented for the ops pass).

### F3 — never-curated age leg + failure backoff (P0-3a; code labels: M17 F4)

With no prior curate marker, the age source is the OLDEST UNRETRACTED
epoch note's mtime (`oldestUnretractedNoteMtime`, curator.go) — epoch
notes are the curation input (source of truth; topic pages are derived
artifacts), so their age measures curation staleness and the
M12-unreachable-by-construction first curate can fire. Known fragility:
mtimes reset on `git checkout`/`cp`/CI clones — fail-safe direction (no
spurious curates); a journal-marker-time age source is a sturdier
follow-up. A curate failure (any trigger) suppresses auto retries for
24 h, derived from the journal like the auto-distill ladder; a passing
curate resets it. `TestAutoCurateAgeTrigger` pins all three legs: fresh
notes never fire (drained deterministically via `curateWG` — no sleep
window), backdated notes fire as `auto_age`, an existing stale marker
still fires.

Rider shipped with the same pass (was undocumented in the first review
round — K3 blocker): **retracted notes never feed the curator**
(`curateCore` filters the project-wide enumeration through the ACTIVE
conversation's retraction set; an all-retracted project errors with an
unretract hint). Fixture: `TestCurateSkipsRetractedNotes` (prompt-copy
seam proves the retracted content never reaches the curator; the marker's
`notes_read` lists only shown notes). Known seam: the retraction set is
conversation-scoped, so a note retracted in a SIBLING workstream's
conversation is invisible here (single-workstream projects — the
production case — are unaffected; `siblingRetractionGate` in
recall_cross.go is the reuse candidate for a multi-workstream follow-up).

### F4 — ghost-citation repair (P0-3b; code labels: M17 F4)

`checkTopicCitations` (curator.go) classifies dead citation tokens: a
`(epoch-N)` token whose epoch has NO note file on disk AND no distill
marker for ANY workstream is a GHOST — it names an epoch that never
existed. Repair semantics per bullet line:

- zero ghosts or any REAL dead citation on the line → the gate stays
  fail-closed (the whole curate aborts before any write when ≥1 real dead
  citation exists);
- ghosts only, no live citation on the line → the line's prose is
  unprovenanced confabulation by construction → the whole line is
  stripped;
- ghosts + a live citation on the line → the line's fact is provenanced
  and stays; only the phantom tokens are scrubbed in-line (review-fix:
  the first cut stripped such lines whole, silently destroying live
  facts).

Every stripped token is journaled in the pass marker's
`stripped_citations`, and (review-fix) in the gate_failed marker as well —
the abort path no longer loses the "what would have been stripped" record.
Fixtures: `TestCheckTopicCitations` (incl. the live+ghost line),
`TestCheckTopicCitationsAllGhostEmptiesTopic`, plus the existing
dead-citation gate end-to-end tests.

### F5 — skills-audit actor hygiene (P1-11)

`odo skills audit` labels human-visible auto-land resolutions
(`actor:"auto_panel"`, accept/reject) as `auto_accept`/`auto_reject`:
excluded from per-skill rows AND from the skill-free baseline, reported as
the separate `auto_accepts`/`auto_rejects` totals — mirroring
`ComputeAutonomy`'s streak exclusion. Nuance: auto-land's own
`moa_review{consensus:reject}` rows keep the M15 weak-signal semantics
(weak_reject counts in rows/baseline) — a tri-model consensus is a real
signal, and counting it is the conservative direction for flagging.
Fixture: `TestSkillsAuditAutoActorExcluded`.

## Teardown robustness

`Server.curateWG` (new) drains detached auto-curate evaluations at
`Wait()`/test teardown — the M12 pattern for auto-distill timers,
extended to F3/F4's fail-open goroutine. Fixes a teardown race
("journal into a closed store" / TempDir cleanup failures) the detached
evaluation introduced; also a production graceful-shutdown drain.

## Verification

- `go build ./...` and `go vet ./...` clean; gofmt clean on all touched
  files.
- Full suite `go test ./internal/ipc/ -count=1` green twice back-to-back
  (~246 s each; includes the learner/curator/contradiction rigs) —
  `TestAutoCurateAgeTrigger` previously flaked ~50 % under suite load:
  the fixture asserted the pre-F3 contract under a −1 ns threshold seam
  and its no-fire window passed vacuously; rewritten to the F3 contract
  with a `curateWG`-drained negative leg.
- 3-way blind review (K3/GLM/DSF) round: 3× REJECT → fixes applied
  (tool_call tombstone, live+ghost line semantics, doc-truth alignment,
  retracted-curator-input test, stripped-tokens-on-abort marker).

## Not done (deliberately)

- **Production data repairs** (post-release ops against the live
  journal): `odo unretract main-epoch-{2..7}` for seqs 5144–5149, then the
  first real auto-curate regenerates topic pages with ghosts scrubbed.
- **P0-4 / P0-5** (fstools denylist gaps, `scanSkills` symlink
  confinement, `moa_fs_deny` append-vs-substitute, fold-whitelist
  `actor:auto_panel`): bundled with the settlement-ladder milestone.
- P1 table (accept TOCTOU, protected-path case-fold, MoA thinking
  journal, chained-auto-land base-stale, REVIEW_READ_TIMEOUT,
  memory.md/pins.md materialization): sequenced after this batch.
- Follow-ups seeded above: sibling-workstream retraction visibility for
  curator/age leg; journal-time age source; wiki-browser unretract badge.

## How to run

```
odo unretract main-epoch-3    # reverse one false-positive retraction (journal record, idempotent)
odo skills audit --json | jq '{auto_accepts, baseline}'
```

The distiller needs no invocation: the next idle/urgent/startup trigger
on any conversation with ≥ 6 events and ≥ 16 KiB of rendered window fires
on its own.
