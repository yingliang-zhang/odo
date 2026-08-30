# D9 Learning Control Plane — Implementation Design (waves W3–W6)

- Date: 2026-08-30. Anchor: odo working tree (post-D8, post-P3). All seam
  references below were verified against this tree.
- Source lock: docs/design/control-plane-hardening-lock.md §D9 (verbatim
  roadmap), docs/design/self-improving-first-principles-2026-08-15.md
  (MVP loop + bounds), D4 ruling ④ (memory write contract), D7 settle table,
  D1 structural gate policy (gate_manifest.json), D3 usage ledger.
- Doctrine carried forward: journal-first (evidence rows before actions),
  fail-closed on ambiguity, additive journal shapes only, one measure
  convention reused everywhere (rules_audit's attribution model), zero LLM
  calls in any gate.

---

## §0 Corrections and resolved ambiguities (read first)

Five claims in the lock/brief are wrong or ambiguous. Corrections C1–C3 are
backed by code; R1–R6 are lock ambiguities resolved with a recommendation.

**C1 — Brief error: "MemoryPanel already shows flagged rules + effect
metrics."** It does not. `gui/src/components/MemoryPanel.tsx:2` imports only
`applyMemory, memoryProposals, readMemory, readPins, resolveHealConflict`; it
renders Proposals + Current files. Flag rows surface as ledger.md sections
(`ipc.AppendRulesAuditLedger`, cmd_rules_audit.go:99) rendered raw by
`LedgerPanel.tsx` (the Ledger tab, contrib.ts:74 registry entry). D9's GUI
wave therefore EXTENDS MemoryPanel with its first-ever flag/candidate surface
(no parallel surface created — a sub-tab, not a new panel).

**C2 — Brief error: "learner.go, autonomy.go, settle.go are C0-protected."**
C0 is memory-prefix-only BY LOCK: D1's classifyDiff guard ("autonomy C0
remains memory-prefix-only… gate sources get their own gates, not an
autonomy-class carve-out"), implemented at autonomy.go:220-233
(`isMemoryPath`). Those files are **Tier-1 gate source** via the
`internal/ipc/` prefix (gate_manifest.json `protected_prefixes`), so diffs
touching them land only behind a journaled panel verdict on the exact patch
bytes (the Tier-1 evidence gate); Tier-0 (gatepolicy.go, gate_manifest.json)
is human-only outright. The dispatch consequence ("expect protected-path
human Accept at the gate") is the same; the mechanism named in the brief is
not. Every D9 wave except the pure-CLI W3 touches `internal/ipc/` →
**every wave lands behind panel attestation + human Accept routing; Tier-0
files are untouched by D9 entirely.**

**C3 — Lock overreach risk: "auto-rollback on rules_audit's harmful tuple."**
rules_audit measures *memory-rule injection cohorts*, not candidates. A
candidate can never inherit a memory rule's flag — that would be one
subsystem retracting another on borrowed evidence. Resolution: the **harmful
tuple as a PREDICATE** — the threshold set and integer cross-multiplication
math (rules_audit.go:93-98, :553-560) — is generalized and re-applied to the
candidate's *own* diff cohort (§4). The tuple is reused, never the flag row.

**R1 — What IS a candidate? Resolved: a closed-grammar, conservative-only
pipeline decision rule.** The lock says "projection of the candidate rule set
against a frozen journal slice" — only true if candidate effects are
deterministic functions of journaled rows. Free-text memory.md rules are
prompt-shaped (LLM-mediated, unreplayable). So D9 candidates are rules over
the **auto-land pipeline's decision points**: `{when: predicate over journaled
diff/risk/verify/verdict fields, then: one action from a closed enum}`. The
action enum is `{route_human, block, advisory}` — all strictly
more-conservative than live behavior. **Loosening is grammatically
impossible** (no action can permit more auto-landing than today); numeric
gate mutation (e.g. settleMaxReviseRounds) is excluded on purpose — a
runtime-mutable gate constant makes gate behavior depend on learned state,
the circularity D1 exists to prevent (the judged rewriting the judge). A
proven "loosen" case routes to the human as a code change (§3, codification).
This makes frozen replay EXACT rather than approximate, and makes the whole
control plane monotone-safe: learning can only tighten.

**R2 — Do candidates ever write memory.md? Resolved: never.** Stages below
project_active never touch memory.md. project_active is *not* a memory.md
apply — the candidate remains a separate, journaled overlay consulted at the
pipeline seams; only `global_active` (human-only) materializes a rule, via
the existing human apply/codification paths. This keeps D4's ruling intact
(deletion-class in memory.md stays human-committed; a candidate rollback is
a stage flip, never a file deletion) and makes rollback trivially safe at
every stage. memory.md rules keep the entire existing D4 lifecycle untouched.

**R3 — Lock: provenance carries `{uses, cost}` in the candidate row.**
Conflict with "immutable, append-only": mutable counters cannot live in an
append-only row. Resolution: the candidates.jsonl row carries
*source-provenance only* (where the rule came from); `uses` and `cost` are
**fold-derived** from episode/evidence rows that reference the artifact hash
and are reported by `odo learning audit` — never stored. Immutable artifact +
derived counters, exactly like the journal-derive-everything doctrine.

**R4 — Lock: "shadow metrics within tolerance vs live (same frozen slice)."**
Shadow counterfactuals are computed on *live incoming* diffs after
promotion, not on the frozen slice (the frozen slice is only the
candidate→shadow replay input). "Same slice" resolves to: the shadow arm and
the live arm are compared over **the same live window of diffs** — per-diff
paired comparison, never different populations. See §3.

**R5 — global_active for a pipeline rule.** "Global" cannot mean a
cross-project runtime config file (a second policy surface beside the D1
manifest; also user.md layers are untouched by constraint). Resolution:
**global_active = codification** — the human fires `odo learning promote
<hash>`, the daemon emits the candidate as a source diff (a new threshold
constant, a routing clause, gated through the normal auto-land pipeline),
and because the target is Tier-1/Tier-0 gate source the landing requires the
panel + human Accept by construction. The stage row cites the landed diff.
The highest promotion of a deterministic rule is being written down as code.

**R6 — LLM in the loop.** LLM appears in exactly two places: proposal
generation (the existing learner one-shot, extended with a candidates
contract) and optional episode summarization (`learning_summarize: on`,
default off; failure/absence degrades to the deterministic digest alone —
droppable without breaking the pipeline). Every gate, replay, rollback, and
freeze is LLM-free.

---

## 1. Data model

### 1.1 New journal event type + new artifact

One new discriminated event type, the M19 `loop_event` precedent:

```go
// internal/store/store.go — additive, beside EventLoopEvent
EventLearningEvent = "learning_event"
```

Every consumer of `ListEvents`/`ListProjectEventsPage` switches on known
types and ignores unknown ones by construction, so a new type is invisible to
every existing fold (same argument the lock accepted for loop_event). All
learning rows discriminate on `kind`, never on a new top-level type. The
stage machine's project-wide state rows journal on **main's active
conversation** (`RulesAuditMainWorkstream` precedent, rules_audit.go:645);
per-diff shadow rows journal on the diff's own conversation (where every
other review_action for that diff lives).

New artifact: `.odo/learning/candidates.jsonl` (append-only, one JSON object
per line, content-addressed). No other new daemon state: stage state, cohort
membership, metrics, freeze state are all folds over journal rows.

### 1.2 learning_episode — the join row

An episode is **one terminal outcome, attributed once**. The attribution
itself (send→terminal→diff→outcome→cohort, rules_audit.go:230-430) is the
expensive part; journaling the join once is receipt discipline — downstream
gate folds read episodes, they never re-derive attribution. Episodes are
written by the **measure pass** (idempotent, NovelFlags convention), which
runs (a) from `odo learning audit` and (b) at the distill epoch boundary
inside distillCore (server.go — one call beside the existing
`collectAuditFlagContext` call at server.go:4937-4941). Non-terminal signals
(verify blocks, dissent, revise rounds) are NOT re-journaled — gate
evaluators read the existing `auto_land_blocked` / `moa_review` /
`auto_revise_round` rows directly; episodes carry only what does not already
exist: the join.

Qualifying terminal outcomes (task list, resolved):

| Outcome | Source row (existing) | Episode `outcome` | Grade |
|---|---|---|---|
| Human accepted diff | `review_action{action:"accept", actor≠auto_panel}` | `human_accept` | scoring |
| Human rejected diff | `review_action{action:"reject", actor≠auto_panel}` | `human_reject` | scoring |
| Weak reject (panel reject, no human action) | `moa_review{consensus:"reject"}` unresolved | `weak_reject` | scoring (0.5 weight, rules_audit convention) |
| Auto-landed / auto-rejected | `accept/reject{actor:auto_panel}` | `auto_accept`/`auto_reject` | evidence-only — never scores a candidate (C0/C3 cohort hygiene) |
| Revise-ladder convergence | accept with ≥1 prior `auto_revise_round{origin_diff_id}` | rides the accept row; `revise_rounds` field | scoring |
| Ladder suspension | `memory_update{layer:"auto_land", cause:"ladder_suspended"}` | `ladder_suspended` | evidence-only (non-terminal for the diff) |
| rules_audit flag | `review_action{action:"memory_audit_flag"}` | `rule_flag` (cites flag seq) | evidence-only — memory-plane signal, never a candidate stage-mover |
| Human memory revert | `memory_update{layer:"apply", cause:"revert", actor:"human"}` | `memory_reverted` | scoring-grade for replay harm attribution (a revert is a late reject, autonomy.go revert-heuristic precedent) |

Excluded from episode journaling (not terminal): `panel_minority_reject`
suspensions, `panel_infra`, verify blocks (diff stays pending; they feed
metrics as raw rows). `false_stop`/`no_text` runs never produce a diff; the
retry's real outcome is the episode.

```go
// internal/ipc/learning.go
type learningEpisode struct {
    Kind         string `json:"kind"`          // "episode"
    EpisodeID    string `json:"episode_id"`    // "diff-<id>" | "flag-<seq>" | "revert-<seq>"
    Outcome      string `json:"outcome"`       // table above
    DiffID       int64  `json:"diff_id,omitempty"`
    Conversation int64  `json:"conversation"`
    ResolveSeq   int    `json:"resolve_seq"`   // the terminal row's per-lane seq
    Cohort       episodeCohort `json:"cohort"`
    DiffClass    string `json:"diff_class"`    // classifyDiff output, C1..C3/unclassified
    ReviseRounds int    `json:"revise_rounds,omitempty"`
    Excluded     bool   `json:"excluded"`      // never-score-own-changes predicate (§5)
    ExclusionReason string `json:"exclusion_reason,omitempty"`
    Usage        *episodeUsage `json:"usage,omitempty"` // D3 extractor, fail-soft
}

type episodeCohort struct {
    MemorySHA16    string `json:"memory_sha16,omitempty"`    // newest .odo/memory.md receipt in window (rules_audit join)
    CandidateSHA16 string `json:"candidate_sha16,omitempty"` // the CANDIDATE cohort key (canary rides) — §3
}

type episodeUsage struct { // adapter.SessionUsage, same fields as loop_run_usage (D3)
    Available      bool    `json:"usage_available"`
    InputTokens    int     `json:"input_tokens"`
    OutputTokens   int     `json:"output_tokens"`
    CacheRead      int     `json:"cache_read_tokens"`  // journaled, never budgeted
    CacheWrite     int     `json:"cache_write_tokens"`
    CostUSD        float64 `json:"cost_usd"`
}
```

Idempotence: dedup key `(episode_id, outcome, resolve_seq, cohort)` — a
re-derived identical join adds no row (NovelFlags convention,
rules_audit.go:620-640). Journal example:

```json
{"kind":"episode","episode_id":"diff-142","outcome":"human_reject",
 "diff_id":142,"conversation":3,"resolve_seq":87,
 "cohort":{"memory_sha16":"e41d2a77b90c3f55","candidate_sha16":""},
 "diff_class":"C3","revise_rounds":1,"excluded":false,
 "usage":{"usage_available":true,"input_tokens":41200,"output_tokens":1900,
          "cache_read_tokens":80211,"cache_write_tokens":0,"cost_usd":0.061}}
```

### 1.3 candidates.jsonl — the immutable candidate

Content addressing: `artifact_hash = sha16(canonicalJSON({rule, scope,
supersedes}))` — rule CONTENT only, never provenance (learner.go:52 sha16
convention; canonical JSON = keys sorted, no whitespace). The same rule text
re-proposed after a rollback produces the SAME hash — which is exactly what
the freeze keys on. The JSONL row is the provenance ledger; the hash is the
identity used by the stage machine, the freeze, and every evidence row.

```go
// internal/ipc/learning.go
type candidateRule struct {   // the whole grammar — closed, lint-checkable
    Kind string     `json:"kind"`  // "route_human" | "block" | "advisory"
    When ruleWhen    `json:"when"`
    Then ruleThen    `json:"then"`  // Then.Action == Kind (single source)
    Note string     `json:"note"`   // display-only, never evaluated
}

type ruleWhen struct {         // exact-match set predicates over JOURNALED fields
    DiffClass       []string `json:"diff_class,omitempty"`    // C1|C2|C3 (C0 never arms the pipeline)
    RiskClass       []string `json:"risk_class,omitempty"`     // risk receipt classes (risk.go)
    VerifyFailCount *int     `json:"verify_fail_count,omitempty"` // prior verify_failed/verify_no_evidence blocks on the chain
    ReviseRound     *int     `json:"revise_round_gte,omitempty"`  // current ladder round >= n
    PathPrefix      []string `json:"path_prefix,omitempty"`
    RejectFamily    []string `json:"reject_family,omitempty"`   // modelspec.Family of prior reject legs
    PatchStat       *ruleStat `json:"patch_stat,omitempty"`    // files/lines bounds
}
type ruleStat struct{ FilesLTE, LinesLTE int `json:"files_lte,omitempty"` } // both <= only

type ruleThen struct {
    Action string `json:"action"` // "route_human" | "block" | "advisory"
}

type candidateRow struct {     // one candidates.jsonl line — append-only
    ArtifactHash string       `json:"artifact_hash"`
    Kind        string        `json:"kind"`      // "rule" (one kind today; additive)
    Rule        candidateRule `json:"rule"`
    Scope       string        `json:"scope"`     // "project" — the only scope
    Origin      string        `json:"origin"`    // "learner" | "codify"
    SourceSeq   int           `json:"source_seq"`   // the memory_propose row's seq
    SourceConv  int64         `json:"source_conv"`
    ProposalIndex int         `json:"proposal_index"`
    Supersedes  string        `json:"supersedes,omitempty"` // prior artifact_hash this replaces ("" first)
    CreatedAt   string        `json:"created_at"`
}
```

Line example:

```json
{"artifact_hash":"9f2c41a8b0d3e77c","kind":"rule",
 "rule":{"kind":"route_human","when":{"diff_class":["C3"],"risk_class":["supply_chain"],"verify_fail_count":1},
         "then":{"action":"route_human"},"note":"supply-chain-adjacent C3 with a prior verify failure goes to the human"},
 "scope":"project","origin":"learner","source_seq":912,"source_conv":3,
 "proposal_index":0,"supersedes":"","created_at":"2026-08-30T09:00:00Z"}
```

Append discipline: crash mid-write leaves a partial final line — the loader
treats a non-JSON or newline-less tail as a quarantined stub (§7), never
parses it. `uses`/`cost` are NOT in the row (R3).

### 1.4 Stage-state machine

States: `candidate → shadow → canary → project_active → global_active`, plus
terminals `rejected` (lint/replay fail), `rolled_back` (auto-rollback or
staleness), `frozen` (oscillation), `quarantined` (artifact integrity), and
`superseded` (a newer hash of the same lineage promoted past this one).

| Transition | Fired by | Trigger (all deterministic unless marked HUMAN) | Journal row |
|---|---|---|---|
| candidate→shadow | gate evaluator (distill boundary + boot) | lint PASS ∧ frozen replay PASS | `learning_event{kind:"stage"}` |
| candidate→rejected | gate evaluator | lint or replay FAIL (reason rides) | stage row, `reason:"lint_fail"\|"replay_fail"` |
| shadow→canary | gate evaluator | paired shadow/live tolerance over ≥ `learningMinCanaryN` diffs, canary slot FREE (exclusivity) | stage row |
| shadow→rolled_back | gate evaluator | stale window elapsed without min N, or regression signal | stage row, `reason:"stale"\|"regression"` |
| canary→project_active | gate evaluator | cohort no-worse predicates (§4.3) ∧ harmful tuple absent | stage row |
| canary→rolled_back | rollback evaluator | generalized harmful tuple (§4.1) or regression | `kind:"rollback"` |
| project_active→rolled_back | rollback evaluator | harmful tuple on the project cohort | `kind:"rollback"` |
| any→frozen | gate evaluator | 2nd rollback of the same artifact_hash lineage | `kind:"frozen"` |
| project_active→global_active | **HUMAN ONLY** (`odo learning promote`) | emits codification diff; row cites landed diff | stage row, `actor:"human"` |
| frozen→candidate | **HUMAN ONLY** (`odo learning unfreeze`) | explicit | stage row, `actor:"human"` |

Stage row shape (project-wide state, main lane):

```json
{"kind":"stage","artifact_hash":"9f2c41a8b0d3e77c","from":"candidate","to":"shadow",
 "actor":"daemon","reason":"replay_pass",
 "gate":{"frozen_max_id":51044,"frozen_rows":51044,"frozen_sha16":"77aa01c3d5b2e884",
         "lint":"pass","replay":{"prevented_harm":2,"caused_harm":0,"friction":1,"decisions_changed":3}},
 "evidence":[912]}
```

Restart-proofness: the stage machine is a pure fold over `learning_event`
rows (newest transition per hash wins, supersedes lineage resolved first) —
no in-memory state, the boot fold re-derives it (the ladder-suspension
doctrine, settle.go:70-76). Idempotence: the evaluator skips a transition
whose `(artifact_hash, to, reason, gate tuple)` already exists
(`userScopeHeldReported` convention, memory_autogate.go:219).

### 1.5 Proposal path (the only LLM entry)

`MemoryProposal.Target` gains additive value `"candidates"` (protocol.go:208
enum extension — additive key value, no rename). The learner prompt
(`learnerPrompt`, learner.go:474) gains a candidates contract behind pref
`learning_candidates: on` (default OFF — the `skills_distill` opt-in
precedent, learner.go:532): input = the deterministic episode digest (tallies
per diff class: reject rates, verify-fail rates, dissent, revise rounds —
numbers only), output = `candidates:[{kind,when,then,note}]`. Daemon vet,
LLM-free: grammar check (every field against the closed enums, both-sides
path semantics via `git.PatchPaths` doctrine), security lint (§2.3), the
prompt never offers the contract when the pref is off — the vet then ignores
the array wholesale (`proceduresOn` convention, learner.go:395-397). Panel
gates candidates exactly like any proposal (`panelAccepts`,
memory_autogate.go:51); project scope → auto-apply permitted post-panel
(ruling ④ — candidates are project scope). The apply core routes
`target=="candidates"` to the candidate sink: append candidates.jsonl +
`learning_event{kind:"candidate", artifact_hash, source_seq, proposal_index}`
— never a memory.md write (R2).

---

## 2. Frozen replay design

### 2.1 What freezes

The frozen slice = **all project journal rows with global id ≤ the
evaluation head**, pinned by three fields on the gate row (§1.4 example):
`frozen_max_id`, `frozen_rows`, `frozen_sha16`. The slice hash is computed
STREAMING — sha16 over the concatenated `id:conversation_id:seq:type:
sha16(payload_json)` of every row, paged via `ListProjectEventsPage`
(events.go:115-143, 512/page — the boot-replayer K3 hygiene, never
materialized). Neither per-lane seq ranges nor a content snapshot: seqs are
not comparable across lanes (events.go:80-90 doctrine) and payloads are too
heavy to copy; the id-bounded slice + streaming hash lets any later re-run
verify the same input by re-walking to `frozen_max_id` and comparing
`frozen_sha16`.

### 2.2 What "projection" means — LLM-free by construction

Replay re-folds the frozen slice through the pipeline's **deterministic
decision functions**, with the candidate active vs. baseline, and compares
the two decision sequences. The replayable decision points are exactly the
ones whose inputs are journaled:

- `classifyDiff` (autonomy.go:213) over the diff's journaled patch stats +
  `git.PatchPaths` — pure over the patch bytes.
- risk classification (`risk.go` receipt keys journaled on every
  review_action row since W5) — pure over patch bytes.
- verify gate outcome — journaled (`auto_land_blocked{reason:
  verify_failed|verify_no_evidence|verify_unconfigured}`, autoland.go:1037,
  :1049, :1001) — replay READS the journaled outcome, never re-runs .odo-verify
  (re-running would execute code — not LLM-free in the safety sense, and
  non-deterministic against a moved HEAD).
- `settlementClass` (settle.go:196) over journaled `moa_review{reviews[],
  consensus_verdict}` legs — pure.
- ladder state (rounds, no-progress comparators from `auto_revise_round`
  rows) — pure.

NOT replayable, therefore outside candidate grammar by design: anything that
changes panel prompt assembly, model selection, or review weighting (a
changed panel is a different judge — the codex lesson, first-principles §C;
also D8's "never in consensus math"). `advisory` actions journal a note for
the human surface; they change no decision, so replay projects them as
no-ops.

Per diff in the slice, replay computes: live decision D₀ (from the journal —
what actually happened), candidate decision D₁ (D₀ with the candidate's
`when` matched against the diff's journaled features and its `then` applied
at the matched seam). Because the action enum is conservative-only (R1),
D₁ ∈ {D₀, route_human, block, advisory}. Replay then classifies every
decision change against the diff's LATER terminal outcome in the slice:

- **prevented_harm**: candidate would block/route a diff that was
  human-rejected, weak-rejected, auto-rejected, or human-reverted
  (`memory_reverted` episodes) — the candidate would have saved the round.
- **caused_harm**: candidate would block/route a diff that was
  human-ACCEPTED with no later revert — pure friction with no rescue.
- **friction**: candidate would block/route a diff that landed
  (`auto_accept`) — spend saved/lost, outcome-neutral by the journal's
  evidence (an auto-accept with zero later human signal is not provable
  harm — counted, not gated).

### 2.3 Pass/fail criteria (candidate → shadow)

1. **lint/security PASS** (deterministic): grammar closed-enum check;
   `when.path_prefix` matched against protected prefixes both-sides;
   prompt-injection patterns in `note` (the risk.go vocabulary reused);
   action conservative-only (no `then` can widen auto-landing — a compiler
   of the grammar, unit-pinned); rule count per candidate = 1 (composability
   comes from multiple candidates, never one composite rule).
2. **harmful tuple ABSENT on the candidate cohort within the slice** —
   the counterfactual attribution (C3): diffs the candidate would have
   blocked, projected against the tuple predicate with ≥`learningMinCohortN`
   (10, rulesFlagMinInjections convention) — vacuously absent below min N.
3. **no caused_harm**: `caused_harm == 0`. One prevented-harm-negative case
   fails the candidate outright — risk first (the `harmful wins on overlap`
   convention, rules_audit.go:60).
4. **signal, not noise**: `prevented_harm ≥ 1` AND `friction ≤ 3 ×
   prevented_harm`. A zero-win candidate is all cost; reject at the gate,
   journal `reason:"replay_fail", detail:"no prevented harm"`.
5. **monotonicity pin**: the replayed decision sequence must contain ZERO
   loosened decisions — belt-and-suspenders assertion over the lint
   guarantee, journaled as a replay field (`loosened:0` required).
6. **budget bounds**: replay wall-time cap (30s; page-bounded, abort →
   fail-visible row, no promotion); candidate count in shadow ≤ 3; canary ≤ 1
   (exclusivity, §3).
7. **determinism**: replay is a pure fold — running it twice on the same
   `frozen_sha16` must produce byte-equal gate tuples (pinned test; also the
   determinism proof the lock's "zero model calls" clause needs).

Regression vs baseline projection: because the grammar is conservative-only,
"no regression" reduces to criteria 3+4 (harm and noise) — landing-rate
drops are permitted outcomes of a conservative rule, never a replay failure.

### 2.4 Engine placement

`internal/ipc/learning_replay.go` — a new Tier-1 file (the `internal/ipc/`
prefix makes the learning plane's own source gate source BY CONSTRUCTION: a
candidate can never land a change to its own evaluator — D1's boundary doing
double duty). It reuses the replay paging and pairing idioms of
memory_replay.go (streaming fold, propose→apply-style pairing of
`auto_revise_round`→terminal) but shares no code with the memory-intent
replayer (different predicate; the locked ordering rule there is
memory-specific, memory_replay.go:21-31).

CLI: `odo learning replay <hash>` re-runs on the recorded freeze and prints
the gate tuple (verification surface for the human; `--refreeze` re-evaluates
on the current head and journals the result — the only re-freeze path).

---

## 3. Shadow / canary mechanics

### 3.1 Shadow — counterfactual recording, zero live effect

Seam: one hook at each replayable decision point in the pipeline:

```go
// autoland.go, after the settlementClass switch resolves (≈:523) and at the
// verify-gate branch (≈:461-470); settle.go at the ladder decision
// (settleRevise entry) — each site gains exactly one call:
s.learningShadow(ctx, d, "settlement", liveDecision, decisionCtx)
```

`learningShadow` (learning.go) is pure-read + journal: it evaluates every
shadow-stage candidate's `when` against the SAME journaled features replay
uses, journals `learning_event{kind:"shadow_decision"}` on the diff's lane,
and returns without touching the live path. Shadow row:

```json
{"kind":"shadow_decision","artifact_hash":"9f2c41a8b0d3e77c","diff_id":150,
 "decision_point":"settlement","live":"revise","candidate":"route_human","changed":true}
```

Gate (shadow→canary): paired comparison over the same live window — for each
diff that armed the pipeline, the shadow arm's projected decision vs the
live arm's actual decision, scored exactly like replay §2.2
(prevented_harm/caused_harm/friction) on later-resolved outcomes. Promotion
requires `uses ≥ learningMinCanaryN` (10), `caused_harm == 0`, `friction ≤
3 × prevented_harm`, and the canary slot free. The comparison is per-diff
paired (R4) — never aggregate-rate-vs-aggregate-rate, which a routing-heavy
candidate would game.

**Canary exclusivity**: at most ONE candidate at canary at a time, at most
one lineage at project_active per decision point — two simultaneous canaries
confound every cohort. The stage fold enforces it fail-closed: a promotion
attempt while a slot is occupied journals `reason:"canary_slot_busy"` and
leaves the candidate at shadow.

### 3.2 Canary — a fraction of diffs ride the candidate, for real

Cohort definition: every diff that arms the auto-land pipeline
(`maybeAutoLand` entry, autoland.go:271) is deterministically assigned:
`sha16(diff goal + diff id) % 100 < learningCanaryPercent` (default 10).
Assignment journals `learning_event{kind:"canary_assignment", diff_id,
artifact_hash}` BEFORE the pipeline runs (evidence before action). The
candidate acts at the SAME seams the shadow hook sits at; a canary action
journals the existing blocked/verdict rows with additive keys
`learning_candidate: <hash>`, `learning_action: "route_human"|"block"` — new
blocked reasons are additive (`auto_land_blocked{reason:"learning_block"}`
follows D7's additive-reason precedent; `route_human` resolves as the
minority-reject posture: pending + transcript advisory, NO auto-reject).

Exclusions from the canary cohort (never-score-own-changes, §5): gate-source
diffs (Tier-0/Tier-1), memory-path diffs (`.odo/`, `wiki/`), learning-plane
diffs (`.odo/learning/**`, `internal/ipc/learning*.go`), C0/unclassified
diffs produced by candidate-acting runs. A candidate never rides the diff
that lands it or touches its own plane.

How reverted: demote the stage (`kind:"rollback"` row) — the canary
assignment fold stops selecting new diffs, in-flight diffs finish under the
rules they armed with (their cohort attribution is by assignment row, so
post-demotion outcomes still attribute correctly), and the receipts stand for
measurement. No diff is ever mid-flight reverted — the pipeline's own
decision, once journaled, is history (append-only doctrine).

### 3.3 Metrics table — existing journal fields per gate

| Metric | Feeds | Source (existing rows, exact fields) |
|---|---|---|
| verify_failed rate | shadow/canary tolerance; rollback tuple | `review_action{action:"auto_land_blocked", reason:"verify_failed"\|"verify_no_evidence", diff_id, patch_sha16}` (autoland.go:1037/:1049) per cohort vs same-window non-candidate baseline |
| revise rounds | tolerance; rollback tuple | `auto_revise_round{round, diff_id, origin_diff_id}` count per chain; `memory_update{layer:"auto_land", cause:"ladder_suspended"}` as the non-convergence terminal |
| panel dissent | tolerance; rollback tuple | `moa_review{reviews[].verdict, consensus_verdict, actor:"auto_panel"}` — dissent = non-accept legs/total; `auto_land_blocked{reason:"panel_minority_reject", repanel_count}` |
| verify duration | tolerance (additive) | **new additive fields**: `auto_land_started{stage:"verify"}` gains `t0_ms`; the first verify-consuming row after it (`auto_land_blocked` verify-reason or `moa_review`) gains `verify_ms` — measured at the seam, one subtraction |
| tokens / cost | budget bounds; rollback tuple | episode `usage` block (§1.2) via `adapter.SessionUsage` (the D3 extractor, loop_journal.go:44-51 shape); loop-provenance runs already carry `loop_run_usage` — the episode fold prefers the existing row, else stamps its own |
| run duration | tolerance | `created_at` delta: arming `user_message`/repair-prompt row → `agent_done` |
| reject rate (the tuple core) | canary→project_active; rollback | episode rows: cohort `human_reject` + 0.5·`weak_reject` over cohort outcomes, cross-multiplied integers vs baseline (rules_audit.go:553-560 math, reused verbatim) |

Baseline for every rate: the same window, same exclusions, non-candidate
diffs only (mirror the §5 exclusion set into the baseline — the population the
candidate acts on, nothing else).

---

## 4. Auto-rollback + oscillation freeze

### 4.1 Rollback trigger tuple (deterministic, C3 resolution)

The generalized harmful tuple — rules_audit's thresholds and integer math
applied to the candidate's own cohort at every gate-evaluation tick (distill
boundary; plus a boot pass):

- `uses ≥ 10` (cohort-assigned diffs with a resolved outcome),
- `harmful events ≥ 3` where a harmful event = `human_reject` (weight 1) or
  `weak_reject` (0.5) of a canary-ridden diff, or a `memory_reverted`
  episode mirroring a landed candidate-ridden diff,
- spread across `≥ 3` conversations (correlated-noise guard),
- bad-outcome rate `≥ 2×` baseline via cross-multiplied integers
  (`(2R+W)·baseN ≥ 2·(2BR+BW)·n` — the rules_audit leg verbatim).

OR the structural legs: cohort verify_failed rate ≥ 2× baseline (same minima),
or mean revise rounds ≥ baseline + 1 with n ≥ 10. Any leg fires rollback.
Below min n, no leg can fire (fail-closed against small-sample retraction —
the `effective additionally requires ≥1 accept` vacuity guard generalized).

### 4.2 What rollback does mechanically

1. Journals `learning_event{kind:"rollback", artifact_hash, from, to:
   "rolled_back", actor:"daemon", reason:"harmful_tuple"|"regression",
   tuple:{uses, harmful, conversations, baseline_outcomes, baseline_harmful},
   evidence:[episode seqs]}` on main.
2. Demotes the stage (canary→rolled_back or project_active→rolled_back):
   the assignment fold stops selecting; the active-set composition at the
   seams drops the hash. **No file ever changes** (R2) — rollback at any
   stage is a journal flip.
3. Increments the lineage's rollback count → freeze check (§4.3).
4. Ledger row (appendLedger, inv 4) + a `journalRunAdvisory`-shaped
   transcript advisory so the rollback is visible where the user reads
   (round-2 parity rule: no code path may leave a rollback ledger-only).

### 4.3 Oscillation freeze — own window, interplay with D4

The D4 memory guard (memory_flags.go:99, `oscillationWindowEpochs = 3`) keys
on distill epochs and `memory_apply` rows — a clock candidates do not have.
Candidates get their OWN freeze, keyed on rollback events, and **stricter**:
**the SECOND rollback of the same artifact-hash lineage (supersedes chain
root) freezes the lineage forever** — `learning_event{kind:"frozen",
 artifact_hash, rollbacks:2, reason:"oscillation_freeze"}`. Rationale: a
candidate rollback is a *measured harm event* with min-n evidence behind it,
not the heuristic flip-flop the 3-epoch window tolerates; two measured harm
events on the same rule content is signal, not noise. There is no
auto-unfreeze; `odo learning unfreeze <hash>` is the human path (journals
`actor:"human"`). The frozen set is injected into the learner prompt as a
marked DATA block (the D4 frozen-marker convention, memory_flags.go:205-207)
and the vet rejects any re-proposal whose content hash matches a frozen
lineage — the `oscillation_guard` rejection shape. The two guards never
interact: the memory freeze gates `memory_apply` retraction cycles, the
candidate freeze gates stage re-promotion; a rule that crossed both planes
would face both independently (journaled separately, both fold-derived).

Supersedes-chain flip-flop is covered by the same lineage key: a v2 that
reverts v1's content, followed by a v3 restoring it, produces identical
content hashes at the lineage root — the freeze catches it by hash identity,
no epoch arithmetic needed.

---

## 5. Never-score-own-changes

### 5.1 Cohort exclusions (the scoring population)

An episode is `excluded: true` — counting toward NO candidate cohort and NO
gate baseline — when its diff touches: Tier-0 files, Tier-1 prefixes
(`isGateSourcePath`, gatepolicy.go — structural, not a list), memory paths
(`isMemoryPath`: `.odo/`, `wiki/`), or the learning plane (`.odo/learning/`,
`internal/ipc/learning*.go`, `internal/ipc/learner.go`,
`internal/ipc/memory_flags.go`). This is ONE predicate, `excludedFromScoring`,
shared by the episode writer, the canary assigner, and the baseline folds —
one function, no per-gate variants. The exclusions mirror into the baseline
(the same population the candidate can act on), so a candidate that routes
gate-source diffs to humans can never look "safer" by comparing against a
baseline that still contains them.

### 5.2 Verdicts are evidence, never stage-movers

Three separated steps, structurally enforced:

1. **Evidence**: panel verdicts on diffs, loop-audit verdicts, moa rows —
   land as they do today (their own event types), and as
   `learning_event{kind:"evidence", source_seq, source_conv, class}` rows
   when the gate fold needs them referenced (e.g., loop verdicts on
   candidate-ridden diffs). A verdict row NEVER names a stage transition.
2. **Measure**: gate evaluators fold episodes + evidence into cohort metrics
   (deterministic, min-n guarded).
3. **Gate**: the threshold predicates of §2.3/§3.1/§4.1 fire transitions —
   the ONLY code that writes `kind:"stage"`/`kind:"rollback"` rows (one
   function, `transitionCandidate`).

Pins: (a) a test injecting a maximally favorable single evidence row (one
unanimous accept, one loop-clean verdict) into a below-min-n cohort asserts
NO stage moves; (b) `transitionCandidate` is the single writer of stage rows —
a code-level pin test (grep-tier, like the `planUserApply unreachable`
assert) that no other call site journals `kind:"stage"`; (c) panel verdicts
on excluded diffs never enter any cohort (fixture with a gate-source diff
accepted unanimously → metrics unchanged).

---

## 6. Wave slicing (one auto-land dispatch per wave)

Gate-touching parts: EVERY wave except W3's CLI touches `internal/ipc/` =
Tier-1 → panel verdict on exact patch bytes + human Accept routing (the
D1-reclaimed `server_test.go:1119` posture). Tier-0 (gatepolicy.go,
gate_manifest.json) untouched throughout. `.odo-verify` lines added where
the repo convention requires (adoption-lock lesson: every quality gate lives
in `.odo-verify`, never only in the brief).

**W3 — pure observability (zero behavior change).**
- New: `internal/ipc/learning.go` (episode fold + idempotent sink +
  `ComputeLearningReport`), `cmd_learning_audit.go` (`odo learning audit
  [--json]`, the cmd_rules_audit.go shape), ledger.md sections
  (AppendRulesAuditLedger precedent), additive `verify_ms`/`t0_ms` fields
  (autoland.go:453 breadcrumb + first verify-consuming row).
- Learner prompt, seams, stage machine: UNTOUCHED. The daemon journals
  nothing new; the CLI is the only writer (WAL + busy_timeout coexists with
  the live daemon — the rules-audit open precedent).
- GUI: MemoryPanel gains a read-only "learning" sub-tab (episodes + metrics;
  extends the existing focus-tab shape, MemoryPanel.tsx:40) — flagged rules
  surface there too, closing the C1 gap.
- Tests: attribution joins on fixtures (send→terminal→diff→outcome with
  cohorts), idempotent re-audit, exclusion predicate table, additive-field
  pins (old rows parse unchanged).

**W4 — candidate artifact + frozen replay + stage machine to shadow.**
- New: `internal/ipc/learning_replay.go` (slice freeze + streaming hash +
  projection), candidates.jsonl writer/loader (quarantine-on-tail), grammar
  + lint, `odo learning replay <hash>`.
- Edits (Tier-1, human-Accept routed): learner.go (candidates contract,
  pref-gated), protocol.go (Target enum value + `LearningProposal` shape),
  memory_autogate.go/server.go apply-core routing for target "candidates",
  server.go distillCore (measure pass + gate evaluator call, beside
  server.go:4937), boot fold in NewServer (stage reconciliation +
  candidate-artifact integrity, the memory-replayer boot precedent,
  memory_replay.go:99-102).
- Shadow hooks at the seams (autoland.go settlement + verify branches,
  settle.go ladder entry) journal shadow_decision rows ONLY — still zero
  live behavior change; the hooks are read-only calls.
- Tests: replay determinism (double-run byte-equal), freeze hash stability,
  lint conservative-only pin, grammar closed-enum pin, replay pass/fail
  fixtures (prevented/caused/friction), stage idempotence, boot
  reconciliation with a truncated JSONL tail.

**W5 — canary actuation + rollback + freeze (the behavior-touching wave).**
- Edits (Tier-1, human-Accept routed): the seam hooks gain the canary branch
  (assignment + action application, additive blocked reasons), rollback
  evaluator at the distill gate tick + boot, freeze on 2nd rollback, frozen
  set injection into the learner prompt (learner.go, D4 marker convention).
- Additive keys on blocked/moa rows (`learning_candidate`, `learning_action`).
- Tests: canary assignment determinism + exclusions, exclusivity (slot busy),
  harmful-tuple rollback fixtures (each leg), freeze after second rollback,
  no mid-flight revert (assignment-row attribution), rollback advisory +
  ledger rows, baseline-mirroring pin (gate-source diffs move no metric).

**W6 — project_active + global_active codification + full GUI.**
- project_active promotion (all-arms assignment, same gates), the R5
  codification path: `odo learning promote <hash>` (human-only CLI, the
  cmd_memory.go shape) emits the candidate as a source diff routed through
  the normal pipeline; stage row cites the landed diff; `odo learning
  unfreeze`; GUI actions in the MemoryPanel learning sub-tab (promote +
  unfreeze buttons, human-actor rows); `.odo-verify` gate lines; wiki topic
  `learning-control-plane.md` + README section.
- Tests: codification diff emission (Tier-1 target ⇒ panel+human by
  construction, pinned), global_active row cites diff, full-stage
  round-trip fixture (candidate→shadow→canary→project_active→global_active
  with all journal rows asserted), GUI vitest/e2e for the sub-tab.

Dispatch order W3 → W4 → W5 → W6, one dispatch each, watcher until land;
any panel dissent follows the D7 settle table + revise ladder verbatim.

---

## 7. Failure modes and pins

| # | Failure mode | Where it bites | Pin / test |
|---|---|---|---|
| F1 | Promote→rollback→re-promote loop (the oscillation the lock names) | stage machine | Freeze on 2nd rollback of a lineage (§4.3); learner-prompt frozen marker + vet rejection; test: two rollbacks then a re-proposal → `frozen`, no third canary ever |
| F2 | Candidate stalls at shadow/canary forever (low traffic, min-n never met) | gate evaluators | Staleness window (default 14d) without min n → `rolled_back{reason:"stale"}`; test: aged cohort with n=3 demotes, journals, freezes nothing |
| F3 | candidates.jsonl diverges from journal (crash mid-append, hash without row, row without stage) | artifact + journal dual home | Boot reconciliation fold (W4): every stage row's hash must resolve to a complete JSONL row; partial tail → quarantined + heal row (the memory-replayer posture); test: truncated tail, orphan stage row |
| F4 | Silent self-reinforcement: candidate routes diffs to human → cohort shrinks → rates skew in own favor | rollback/tolerance gates | (a) paired per-diff comparison, never aggregate-vs-aggregate; (b) exclusions mirrored into baseline; (c) min-n fail-closed; test: candidate routing 100%→human leaves gates inert, never flags regression |
| F5 | Two canaries confound each other's cohort | canary | Exclusivity (one canary, one project_active per decision point); test: second promotion journals `canary_slot_busy`, stays at shadow |
| F6 | Candidate grammar creep: a future wave adds a loosening action | lint + replay | Conservative-only lint is a closed compiler over the enum; replay asserts `loosened == 0`; two pins (grammar + replay) must both move to add any action |
| F7 | Stage rows on the wrong lane make the project-wide fold miss them | stage fold | Stage/rollback/frozen rows journal on main ONLY (RulesAuditMainWorkstream convention); fold reads via ListProjectEventsPage (global id order); test: rows on a foreign lane are ignored by the fold |
| F8 | Replay non-determinism (map iteration order, float math) undermines "zero model calls" | replay engine | Integer-only cross-multiplied comparisons (rules_audit convention, no float boundaries); sorted iteration everywhere; double-run byte-equal pin (§2.3.7) |
| F9 | Episode re-derivation floods the journal per audit tick | measure pass | Idempotence key (§1.2); test: three consecutive audits over an unmoved journal append zero rows |
| F10 | Verify duration fields break old-row parsing | additive fields | `t0_ms`/`verify_ms` omitted on absent — every existing fold unmarshals into structs that ignore unknown keys; pin: old fixture rows parse and fold unchanged |
| F11 | The learner proposes candidates citing evidence that doesn't exist (invented refs) | proposal vet | Same discipline as D4 flag citations: `when` predicates may only name classes that exist in the digest the prompt carried; out-of-digest predicates drop with a journaled `learning_event{kind:"proposal_rejected", reason}` (the retract_proposal_rejected shape); test |
| F12 | LLM summarization becomes load-bearing | episode digest | `learning_summarize` default off; on-failure/absent → deterministic digest alone; test: pipeline end-to-end with the pref off and the one-shot erroring |

Two residual risks stated plainly (no mitigation exists inside this design,
by doctrine): a candidate can only act conservatively, so the failure class
left is *excess friction* — the caps (shadow ≤ 3, canary 1, friction ≤ 3×
prevented-harm, staleness demotion) bound it, and the human sees every
promotion in the MemoryPanel learning tab. And cross-project learning is
deliberately absent: every artifact and fold is project-local; "global"
exists only as the human codification act (R5).
