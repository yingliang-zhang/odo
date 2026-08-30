# D9 — Learning Control Plane: Implementation Design

- Date: 2026-08-30. Anchor: control-plane-hardening-lock D9 roadmap (`docs/design/control-plane-hardening-lock.md:155`).
- Scope: waves 3–6 of the lock (terminal outcome → learning_episode → candidate → lint/security + frozen replay → shadow → canary → project_active → global_active human-only).
- Doctrine inherited: journal-first (evidence before action), fail-closed on ambiguity, additive journal shapes only (ADR-0002 immune), no new SQLite event types, daemon sole writer, ZERO LLM calls in gates.
- Grounding: every file:line below was read at HEAD.

---

## 0. Corrections to the brief / lock (read these first)

1. **"learner.go, autonomy.go, settle.go are C0-protected" is the wrong label.** Per the D1 lock (ruling ① K3), C0 is deliberately memory-prefix-only and was NOT widened to gate sources because widening it would mark every daemon diff C0 and pollute the autonomy ladder (`docs/design/control-plane-hardening-lock.md` D1, "classifyDiff guard (K3)"). Those files are **Tier-1 gate sources** (`internal/ipc/` prefix, `gatepolicy.go`): panel-attested, with human Accept (`actor == ""`) as the unconditional escape. This design routes them for **human-Accept anyway** — they are the judged control plane and must not review their own stage machinery — but the correct enforcement vocabulary is Tier-1 attestation + voluntary human routing, not C0.
2. **"Auto-rollback" cannot mean auto-editing memory.md.** D4 is explicit and re-affirmed by ADR-0003: deletion-class (retract/demote) memory changes NEVER auto-apply (`memory_flags.go:1-15` header; `server.go:6150-6160` diverts retract intents to `retractCands`; `cmd_rules_audit.go:186-237` human-only `odo rules retract`). Resolution: **rollback operates on the candidate layer** (stage demotion, instant, fold-derived, zero file writes); the memory.md line written at project_active is retracted through the existing human-resolved `retract_candidate` path. Two layers, no contradiction.
3. **`global_active` (human-only) vs D4 "user.md: no auto proposal, no auto apply, forever" tension.** A daemon-initiated global promotion would violate D4's strongest invariant. Resolution: `global_active` is a **folded receipt of a human's own edit** to `~/.odo/user.md` — detected through the existing user.md snapshot mechanism (`journalRuleSnapshots`, learner.go:133-…, layer `user`), never daemon-initiated, never a daemon proposal. The daemon's entire role is rendering the candidate in the GUI with a "promote manually" affordance.
4. **Harmful-tuple latency is real and must be answered, not hedged.** The tuple needs injections ≥ 10 (`rules_audit.go:94`); a promoted rule reaching 10 injections via organic traffic can take many epochs, during which it keeps harming. Mitigation inside the lock's constraints: the *measure* stage re-runs the LLM-free projection **every epoch** (it is a pure function, `rules_audit.go:442-452` — "Pure (no I/O)"), and the canary ride-rate is set so a bad candidate accumulates cohort injections ~5× faster than organic. No second trigger is invented: a second, faster trigger would be a new reactive failure mode.

---

## 1. Data model

### 1.1 What qualifies as a terminal outcome → learning_episode

An episode is one journaled terminal outcome the measure stage can score. Event shape follows the taxonomy convention (two flat types, discriminating payload keys; 11 event types at `internal/store/store.go:17-34`; rules-audit precedent `review_action{action:"memory_audit_flag", actor:"rules_audit"}`, `rules_audit.go:649-671`):

```jsonc
// review_action, actor:"learning_plane"
{
  "action": "learning_episode",
  "actor": "learning_plane",
  "episode_id": "3f9c…",          // sha16 of the canonical episode payload (self-hash, tamper-evident)
  "epoch": 42,                     // main-conversation epoch at emission (store.IncrementEpoch, server.go:5056)
  "outcome_kind": "diff_accepted", // see table
  "refs": {
    "diff_id": 812,               // diff_* / revise_* kinds
    "run_id": "r_…",
    "flag_seq": 1207,             // audit_flag kind (per-lane seq, memory_flags.go:39-44 key)
    "revert_epoch": 39,           // human_revert kind
    "origin_diff_id": 809,        // revise_converged kind (auto_revise_round.origin_diff_id)
  },
  "receipts": {"memory": "a1b2…"}, // sha16 of the injected memory block at decision time
                                   // (== user_message receipt[".odo/memory.md"], server.go:1488) — cohort join key
  "cost": {"input": 0, "output": 0, "cache_write": 0}, // folded from D3 loop_run_usage rows when a run exists; zeros otherwise
  "detail": {}                    // outcome-specific, additive-only
}
```

| outcome_kind | Fires on (existing journal evidence) | Excluded, and why |
|---|---|---|
| `diff_accepted` | moa_review accept + `handleDiffAction(accept)` resolution row (`autoland.go:609-632`, `server.go:3430+`), actor `auto_panel` or human | — |
| `diff_rejected` | `reject_independent` auto-reject (reject row + supersedeChain, settle.go:196-214) and human reject on a panel-judged diff | `reject_minority` (non-terminal, pending — D7); `panel_infra` (not a verdict) |
| `revise_converged` | majority-accept valve landing after ≥1 `auto_revise_round` (settle.go valve) | verify failures (implementation evidence, not direction evidence — D7 lock) |
| `ladder_suspended` | `memory_update{layer:"auto_land", cause:"ladder_suspended"}` (settle.go) | — |
| `audit_flag` | existing `review_action{action:"memory_audit_flag"}` rows (rules_audit.go:649-671) — episode wraps them for the measure fold; the flag rows themselves stay canonical | — |
| `human_revert` | `memory_update{layer:"apply", cause:"revert", actor:"human"}` (D4, cmd_retract shape) | — |

Verify failures and minority rejects are deliberately **not** episodes: D7 classifies them as non-directional evidence; scoring on them would let a flaky verify or one dissenting leg move stages — exactly what the lock forbids.

Emission is a small hook at each terminal point; all logic lives in one new file (§6 W3). Episode summarization by LLM, where wanted, is a **separate** async `review_action{action:"learning_episode_note", episode_id, note}` row — additive, droppable; no reader requires it (pinned by test, §7).

### 1.2 Candidate JSONL row (`.odo/learning/candidates.jsonl`)

Append-only, content-addressed. **What bytes hash:** the canonical JSON of the row with the `hash` field REMOVED, keys sorted, no insignificant whitespace — `hash = sha256(canonical)[:16]`. Canonicalization is a pure function pinned by golden tests (any re-serialization must reproduce the hash).

```jsonc
{
  "hash": "9d41…",                       // sha16 of canonical(payload)
  "created_seq": 1888,                   // journal seq at append time
  "created_epoch": 42,
  "kind": "rule_add",                    // rule_add | rule_retract | rule_demote
  "rules": [{
    "line": "- Prefer table-driven tests over loop asserts in Go. — cites: bug-fix-epoch-21; reaffirmed: 0",
    "intent": "add",                     // add | retract | demote
    "target": "memory.md",
    "supersedes_line_hash": null         // sha16 of the exact memory.md line this retracts/demotes, when known
  }],
  "provenance": {
    "artifact_hash": "e7b0…",            // sha16 of the episode-bundle digest (canonical list of episode_ids + flag rows cited)
    "episode_ids": ["3f9c…"],            // the learning_episode rows that are the evidence
    "flag_seq": 1207,                    // when generated from a rules-audit flag (D4 path)
    "source_seq": 1888,                  // learner run's journal anchor
    "scope": "project",                  // project only, forever, until a HUMAN promotes (§0.3)
    "generated_by": "learner",           // the only producer: the existing LLM learner path
    "supersedes": "41aa…"                // prior candidate hash for the same rule(s); null for first
  }
  // NOTE: no "status", no mutable "uses" — see below.
}
```

**Status and uses are NOT in the artifact.** The file is immutable by construction (append-only, hash-pinned); any mutable field would force rewrite, breaking content-addressing. Both fold from the journal: status from `learning_stage` rows (§1.3), `uses` from canary/post-apply injection receipts (§3.3) — the lock's "per-version provenance {…uses…}" is satisfied by the fold, journaled in the measure rows. The lock's intent (provenance visible per version) is met; the mechanism is journal-derived, consistent with "no new daemon state outside the journal + the candidates.jsonl artifact".

Generation path: the existing learner one-shot gains an **additive optional** `learning` output section (same contract style as M9's `procedures`, learner.go:189-208):

```jsonc
{"memory": [...], "procedures": [...],
 "learning": [{"rule": "...", "evidence": "<note name>", "cites": "<note|episode:3f9c…>",
               "kind": "add|retract|demote", "supersedes": "41aa…|"}]}
```

Daemon vets LLM-free (episode/flag refs must resolve; grammar lint §2.2; supersedes must name an existing hash), canonicalizes, appends to the jsonl, journals `review_action{action:"learning_stage", to:"proposed"}`. Rejections are journaled (`memory_update{layer:"learning", cause:"candidate_rejected", reason}`) — same fail-closed shape as `retract_proposal_rejected` (memory_flags.go:174-227 precedent).

### 1.3 Stage-state machine

All transitions are journal rows — **no new event type**: `review_action{action:"learning_stage", actor, candidate, from, to, gate, reason}`. GUI derives stage by fold. Terminal states never emit `learning_stage` again except `superseded` (emitted by the successor's promotion, pointing back).

| # | From → To | Fired by | Gate (deterministic, zero LLM) | Journal payload essentials |
|---|---|---|---|---|
| 0 | (none) → `proposed` | daemon fold after learner run + vet | vet + lint-precheck | `{to:"proposed", candidate:<hash>}` |
| 1 | `proposed` → `gated` | measure fold | **lint/security + frozen replay PASS** (§2) | `{from:"proposed", to:"gated", gate:{name:"frozen_replay", verdict:"pass", slice:{epoch_lo, seq_hi, slice_sha16}, metrics:{…}}}` |
| 1r | `proposed` → `rejected` | measure fold | lint/replay FAIL or vet fail | `{to:"rejected", reason, gate:{verdict:"fail", …}}` |
| 2 | `gated` → `shadow` | measure fold (automatic at next epoch) | gate row present and unexpired | `{from:"gated", to:"shadow"}` |
| 3 | `shadow` → `canary` | measure fold | **shadow tolerance** (§3.2): re-projection on the live window still passes | `{from:"shadow", to:"canary", gate:{name:"shadow_tolerance", …}}` |
| 3r | `shadow` → `expired` | measure fold | 0 would-injects for 6 epochs, or 12 epochs without passing tolerance | `{to:"expired", reason}` |
| 4 | `canary` → `project_active` | measure fold | **canary cohort no worse** (§3.4): min injections, harmful tuple absent, reject-rate ≤ baseline | `{from:"canary", to:"project_active", gate:{name:"canary_gate", metrics:{…}}}` + apply through existing memory gate |
| 5 | `project_active` → `global_active` | **HUMAN ONLY, forever** | human's own user.md edit, folded from user-layer snapshot rows (§0.3) | `{actor:"human", from:"project_active", to:"global_active", evidence:"user_snapshot_seq"}` |
| R | any non-terminal → `rolled_back` | measure fold | **harmful tuple on the candidate's attributed cohort** (§4.1) | `{from:"…", to:"rolled_back", reason:"harmful_tuple", flag_seq}` |
| F | any non-terminal → `frozen` | measure fold | candidate-level oscillation freeze (§4.3) | `{to:"frozen", reason:"oscillation_guard_candidate", …}` |
| S | any → `superseded` | successor's transition | successor cites `provenance.supersedes == this hash` | `{to:"superseded", by:"<successor hash>"}` |

`project_active` is the only transition that writes `.odo/memory.md`, and it does so through the **existing receipted path** (planMemoryApply → memory_autogate panel gate → applyResolvedBatch, `memory_autogate.go:146`, `server.go:6094+`) — never a side channel.

---

## 2. Frozen replay

### 2.1 What freezes

A **journal slice**, pinned by three numbers, recorded in every gate row:

- `epoch_lo`: main-conversation epoch boundary at the slice's start (fold markers carry explicit `first_seq`/`last_seq`, server.go:5072-5073 — the pinned schema).
- `seq_hi`: journal seq at candidate creation (`created_seq`).
- `slice_sha16`: digest over the canonical list of the slice's rows (per-row `sha16(type|seq|payload)`) — tamper-evidence and determinism pin.

Window rule: walk back epoch markers from `seq_hi` until the slice contains ≥ `replayMinOutcomes = 100` non-auto resolved outcomes (the rules-audit outcome join, `rules_audit.go:332-437`), hard cap 30 epochs. Smaller journals replay whole. The input to replay is therefore **bytes already in the journal** (events + memory snapshot rows, learner.go:133-…) — append-only, so re-running can never see different input. A future `odo journal rotate` (move-not-delete, cmd_journal.go:370-439) changes file location, not row content; the slice digest still verifies.

### 2.2 How a candidate is "projected" LLM-free — resolving the lock's ambiguity

The lock says "LLM-free projection of the candidate rule set against a FROZEN journal slice" without defining projection. **Resolution: projection = re-running the rules-audit measure with the candidate rule set substituted for the live rule set.** Reasoning chain:

- `ComputeRulesAudit` is *already exactly* an LLM-free projection of the live rule set over the journal: it folds injection receipts (user_message `receipt[".odo/memory.md"]`, server.go:1488) against outcome rows and produces per-rule counts + baseline (`rules_audit.go:239-243`, `445-595`).
- `aggregateRules` is pure (rules_audit.go:442-452) — the lock's authors deliberately made the scoring core unit-testable without I/O; frozen replay is its second caller.
- Anything stronger (simulating agent behavior change) requires a model by definition, which the lock forbids in gates. So projection must mean *measurement*, not execution.

Mechanically, for each user_message in the slice, the projection recomputes the injected block as `live rules at that seq` XOR `candidate rules` (add/retract/demote applied in candidate order), hashes it, joins outcomes whose receipt matches, and calls `aggregateRules`. Deterministic by construction: same slice bytes ⇒ same block hashes ⇒ same joins. For `retract`/`demote` candidates the substitution removes the matched line (matched by normalized rule text, `normalizeRule`, learner.go — same matching the oscillation guard uses).

### 2.3 Lint / security (pre-replay, same gate row)

- **Grammar**: every `rules[].line` must parse as a daemon-written rule line (`memoryLineRe`, learner.go:98-101 — `- <rule> — cites: <note>; reaffirmed: <n>`). Opaque-line output is refused: candidates must be machine-projectable.
- **Cites resolution**: `cites` must be an existing wiki note name or an `episode:<id>` reference that resolves (fail-closed, mirroring vetRetractIntent's "unknown ref" rejection, memory_flags.go:174-227).
- **Budget**: rendered memory.md delta keeps the file ≤ `memoryCap` 4KB (learner.go:44, enforced read AND apply) and the line count within the existing fold-window byte budget.
- **Security**: rule text must not reference Tier-0/Tier-1 gate paths, `user.md`, or `.odo-verify` (a rule is prompt content; a rule that instructs control-plane edits is a policy-evasion vector); no control characters; rule body length ≤ 500 bytes; no known injection phrases (`ignore previous`, `disregard`) — an allowlist-free denylist is intentionally weak, so the *structural* guards (grammar + cites resolution + gate-path deny) carry the weight.

### 2.4 Pass/fail criteria

The gate row carries `metrics` computed by the same integer cross-multiplication as the live audit (float-boundary-safe, rules_audit.go:554-560):

1. **No harmful tuple in the candidate set**: for every rule present in the candidate projection, NOT(injections ≥ 10 ∧ rejects ≥ 3 ∧ reject_conversations ≥ 3 ∧ reject-rate ≥ 2× baseline) (exact tuple, rules_audit.go:557-562).
2. **No regression vs the baseline projection** (same slice, live rule set):
   - Every rule the candidate **drops/retracts/demotes** must itself currently satisfy the harmful tuple on the slice. *You may only remove bad rules.* This is the crisp, mechanically-checkable form of "no regression" — it forbids silently deleting a rule with a good record.
   - Every rule the candidate **retains unchanged** must keep flag == non-harmful (they do by 1).
   - Every **new** rule has zero outcomes on the slice by definition; criterion 1 is vacuous for it, so the evidence burden shifts to `cites` resolution (§2.3) — a new rule with no resolvable evidence cannot pass.
3. **Budget bounds**: §2.3's cap checks pass.

Fail ⇒ `learning_stage{to:"rejected", reason}` with the failing metrics embedded. The frozen slice is recorded once at candidate creation; **the gate must replay against the recorded slice** — a mismatch (`recomputed slice_sha16 != candidate's recorded slice_sha16`) is a fail-closed `replay_slice_mismatch` rejection (§7 corruption pin).

---

## 3. Shadow / canary

### 3.1 The seam

Two seams, both already durable-decision points:

- **Diff-side (counterfactual bookkeeping)**: `autoLand` between the journaled unanimous verdict (`autoland.go:609-631`, moa_review row with `patch_sha16`) and the land call `handleDiffAction(accept)` (`autoland.go:632`). At this point the decision is durable and diff bytes are sha-attested but nothing is applied — the same property the lock's shadow needs. A W3 episode hook here emits `diff_accepted` episodes with the receipt of the memory block that was actually injected; the measure stage joins that against shadow candidates' would-inject hashes. **Shadow never changes the injected block.**
- **Injection-side (shadow probes)**: the layer fold that builds the injected block (`runMemoryLayers`/`slashContextBlock`; snapshot precedent `journalRuleSnapshots`, learner.go:133) additionally computes, per shadow candidate, `would_inject = candidate rules ∩ block conditions` and journals `memory_update{layer:"learning", cause:"shadow_probe", candidate, user_seq, would_inject, block_sha_would}`. Cost: one append per user_message per active shadow candidate, capped (≤ 3 concurrent shadow candidates, oldest expires).

### 3.2 Shadow (zero live effect)

Shadow's only measurable is **projection stability**: each epoch, the measure fold re-runs the frozen-replay projection against a *rolling* live window (last 10 epochs) and compares against the creation-time gate metrics. Tolerance (same integer machinery):

- harmful tuple still absent for every candidate rule;
- no candidate rule's reject-rate crosses 1.5× its gate-time rate *and* the harmful ratio (≥2× baseline);
- baseline drift: global reject-rate moved < 2× gate-time value (a regime change invalidates the gate's verdict — the candidate must re-gate, not ride through).

Pass ⇒ `shadow → canary` (transition 3). Stall: if the rolling window has < 10 outcomes for 6 consecutive epochs → `expired` (insufficient evidence; fail-closed, the lock's "within tolerance" is unanswerable without outcomes).

### 3.3 Canary (first live effect, bounded)

- **Cohort selection**: deterministic, journal-replayable — a user_message rides the candidate iff `sha16(candidate_hash | conversation_id | seq) % 100 < canaryRidePct` (default **20**). No in-memory state; the same formula recomputed in the fold gives every consumer the same answer.
- **Injection**: the candidate's rules are appended to the injected block as an extra layer, byte-marked (`<!-- learning canary <hash> -->`). memory.md is untouched — the candidate is a *fold-side injection filter* until promotion.
- **Attribution**: the user_message receipt map (server.go:1487-1491 shape) gains an additive key `"learning:<hash>": sha16(candidate-augmented block bytes)`. The rules-audit join (rules_audit.go:239-243) picks this up for free. Clean attribution is what makes cohort scoring and never-score-own-changes (§5) possible.
- **Disabling**: journaling `learning_stage{to:"rolled_back"|"rejected"}` removes the candidate from the fold's active set **at the next injection** — instant, no file write, restart-proof (state is the journal).

### 3.4 Metrics table — which existing journal fields feed each gate

| Gate | Metric | Journal source (verified) |
|---|---|---|
| frozen replay | per-rule injections/accepts/rejects/weak, baseline | user_message `receipt` map (`server.go:1488`), `review_action accept/reject/weak_reject`, `agent_done/agent_error` terminals; join in `rules_audit.go:332-437` |
| frozen replay | harmful tuple | `aggregateRules` integer legs (`rules_audit.go:557-562`) |
| frozen replay | budget | rendered-block bytes vs `memoryCap` (`learner.go:44`) |
| shadow tolerance | would-inject counts, drift | `memory_update{layer:"learning", cause:"shadow_probe"}` (new, additive) |
| canary gate | cohort injections ≥ 10 | receipt key `learning:<hash>` on user_message rows |
| canary gate | harmful tuple absent, reject-rate ≤ baseline | same join, cohort-restricted |
| canary gate | verify_failed rate on cohort diffs | `auto_land_blocked{reason:"verify_*"}` rows + `moa_review.verify_cmd/verify_tail` (`autoland.go:620-621`) |
| canary gate | verify duration | **additive** `verify_duration_ms` key on the moa_review row (W3; currently unjournaled — gap found and closed by this design) |
| canary gate | revise rounds | `auto_revise_round.round` (settle.go journal contract) |
| canary gate | panel dissent | `moa_review.reviews[].verdict` + model family (`settlementClass`, settle.go:196-214) |
| episodes | tokens/cost | `loop_event{kind:"loop_run_usage", input/output/cache_write/cost_usd}` (D3 ledger) |
| rollback trigger | harmful tuple on cohort | per-epoch projection over candidate-attributed outcomes (§4.1) |

---

## 4. Auto-rollback + oscillation freeze

### 4.1 Trigger

The **exact** rules-audit harmful tuple (`rules_audit.go:93-98, 557-562`), evaluated **per epoch** by the measure fold over the candidate's attributed outcomes (canary receipt key pre-promotion; post-promotion the rule's own snapshot cohorts — same cohorts the live audit scores):

`injections ≥ 10 ∧ rejects ≥ 3 ∧ rejects span ≥ 3 conversations ∧ reject-rate ≥ 2× baseline` (integer cross-multiplied form).

No additional triggers (no "2 fast rejects" tripwire): a second reactive trigger is a new failure mode, and the lock names exactly one. Latency is managed by cadence (per-epoch, not on-demand CLI) and canary ride-rate (§0.4).

### 4.2 Mechanics (two layers — resolves lock tension §0.2)

1. **Candidate layer (automatic, instant)**: `review_action{action:"learning_stage", from, to:"rolled_back", reason:"harmful_tuple", flag_seq, metrics}`. The fold stops including the candidate in injections immediately. If the candidate was in `canary`, this is the entire rollback.
2. **memory.md layer (human-resolved, D4 discipline)**: if the candidate had reached `project_active` (its rule was written into memory.md through the receipted path), the rollback fold additionally emits `memory_update{layer:"memory", cause:"retract_candidate", rule, flag_seq, candidate, epoch}` — the existing D4 receipt shape (`server.go:6399-6407`). The human applies via `apply_memory` or `odo rules retract` (`cmd_rules_audit.go:186-237`), which archives the retraction. **The daemon never deletes the memory.md line itself.**
3. **Supersedes chain**: the rolled-back hash becomes a chain node marked rolled_back (state folded from journal). Any successor candidate whose rules normalize-match a rule of a rolled-back candidate must cite `supersedes: <rolled-back hash>`; lint refuses a supersedes edge that *skips* an intervening rolled_back node within the freeze window (fail-closed on ambiguity).

### 4.3 Candidate-level freeze — yes, it needs its own window

The existing guard (retract→re-land within `oscillationWindowEpochs = 3`, memory_flags.go:99-101, 150-154) keys on `memory_apply` rows — it can only see rules that were actually applied and retracted in memory.md. Candidate churn is invisible to it: a candidate can be proposed→gated→rolled_back entirely pre-memory.md, repeatedly, every epoch, without tripping the prompt-level guard. And the flag-consumption dedup doesn't stop re-proposals either: a re-run audit journals fresh flag seqs.

**Design**: candidate-level freeze keyed on (normalized rule text, rolled_back epoch), window **`candidateFreezeWindowEpochs = 6`** — 2× the memory guard, because (a) candidate churn doesn't perturb the user-visible prompt, so a tighter window buys nothing, and (b) evidence for "this rule idea is bad" accumulates more slowly than prompt flip-flop is visible. Fold rule, fully deterministic from journal rows:

> A new candidate whose every rule normalizes to a rule-text that belongs to a candidate rolled_back at epoch R, with R < new candidate's epoch ≤ R + 6, is **auto-rejected at lint** with `memory_update{layer:"learning", cause:"candidate_rejected", reason:"oscillation_guard_candidate", rolled_back_epoch:R}`.

A new *evidence* event can end a freeze early only the honest way: the human retracts the old evidence (or the harmful flag is superseded by an `effective` verdict at ≥ 2× baseline, rules_audit.go:60) — the freeze fold reads those rows; nothing else unlocks it.

---

## 5. Never-score-own-changes

### 5.1 Evidence → measure → gate, three separated steps

- **Evidence rows** (producers, untouched): `moa_review`, `accept/reject`, `auto_revise_round`, `loop_event{kind:"loop_run_usage"}`, `memory_audit_flag`, `memory_update{cause:"snapshot"|"revert"}`. Panel and loop verdicts land here and nowhere else.
- **Measure** (deterministic fold): consumes evidence rows only, emits `learning_episode` rows and per-epoch `review_action{action:"learning_measure", candidate, epoch, metrics}` rows. Pure functions (`aggregateRules` reuse); re-running a fold window reproduces rows byte-identically (golden pin).
- **Gate** (state machine, §1.3): consumes **only** `learning_episode`/`learning_measure` rows. Structurally, the gate fold's event switch admits nothing else.

**Pin**: a test fixture containing a unanimous `moa_review` accept row and nothing else must produce zero stage transitions (panel verdicts are evidence, never stage-movers). Same for `loop_event` rows.

### 5.2 Cohort exclusions

- **Learning-scope candidates**: outcomes whose user_message receipt carries `learning:<hash>` join **only** candidate `<hash>`'s cohort and are **excluded from the live baseline and from every other rule's row**. Without this, a canary candidate would be scored against a baseline it itself is perturbing — the self-reinforcement loop. (The audit already isolates `auto_panel` rows the same way — M17 F5, rules_audit.go:408-411; this extends the same exclusion to the learning key.)
- **C0 diffs**: outcomes in conversations whose diff was C0-classified (`classifyDiff`, autonomy.go:208-233: protected `.odo/`|`wiki/` paths, >5 files, >300 lines, new top-level dir) are excluded from cohorts and baseline — these are dominated by human judgment and structural thresholds, not by rule quality. Mechanism: C0 classification must be journaled to be foldable — **additive** `c0:true` key on the auto-land row that already carries the classification context (verify at implementation; if classification is currently not journaled for some paths, W3 adds a `review_action{action:"autonomy_classify", diff_id, c0, reason}` emission at classify time — additive, one call site in autonomy.go, human-Accept routed per §6).
- Both exclusions are **asymmetric on purpose**: a candidate's own cohort counts fully *for the candidate*; it counts zero *for everyone else*.

---

## 6. Wave slicing

Waves mirror D1–D7's W1/W1b/W2 discipline: one dispatch per wave, journal-first, each independently landable through the auto-land pipeline. Gate-source touches (everything under `internal/ipc/` + root CLIs, per Tier-1 `gatepolicy.go`) get panel attestation; the files the brief calls out (`learner.go`, `autonomy.go`, `settle.go`, `server.go`) are routed for **human-Accept** (unconditional escape, `actor == ""`) — see §0.1 for the C0-vs-Tier-1 correction. New-file-first strategy: all logic in new files; call sites are 3-line journaling hooks, minimizing gate-source diff surface.

### W3 — Pure observability (zero behavior change)

- **New** `internal/ipc/learning.go`: episode structs, emission helpers, candidates.jsonl writer (O_APPEND + per-line hash), the read-side fold for the GUI.
- **New** `internal/ipc/learning_test.go`.
- **Hooks (Tier-1 gate sources, human-Accept routed)**: episode emission at the six terminal points — `handleDiffAction` resolution paths (server.go:3430+), autoland accept/reject branches (autoland.go:609-632), `settleDraft` suspension (settle.go), rules-audit flag sink (cmd_rules_audit.go:65-143), revert (memory_revert.go). Each hook = one `emitLearningEpisode(...)` call.
- **Additive journal fields**: `verify_duration_ms` on moa_review; `c0` classification marker (§5.2).
- **GUI**: MemoryPanel gains a read-only "Learning" section (fold over `learning_episode` + `learning_stage` + candidates.jsonl). Extends the existing flagged-rules + effect-metrics surface; no parallel panel.
- **Acceptance**: full ipc suite green with zero behavior deltas; GUI shows episodes; nothing reads episodes except the GUI (pinned).

### W4 — Candidate generation + gates, still zero behavior change

- **New** `internal/ipc/learning_lint.go` (grammar/budget/security lint), `internal/ipc/learning_replay.go` (slice freezing + projection; reuses `aggregateRules`).
- **Touch** `learner.go` (Tier-1, human-Accept): additive optional `learning` output section (§1.2), daemon vet, candidate append. No change to existing `memory` proposal handling.
- **Stage machine** rows `proposed → gated/rejected` journaled; shadow probe emission begins (§3.1) — pure journal rows, no live effect.
- **Acceptance**: golden replay tests (same slice digest ⇒ byte-identical metrics); a harmful-rule fixture fails the replay gate; lint refusals journaled.

### W5 — Canary + rollback + freeze (first live behavior change)

- **Touch** injection path (`runMemoryLayers`/`slashContextBlock`, server.go — Tier-1, human-Accept): canary ride filter + receipt key `learning:<hash>`.
- **Touch** `rules_audit.go` (Tier-1, human-Accept): the `learning:<hash>` exclusion (§5.2) — cohort isolation, additive read path.
- **Measure fold per epoch**: shadow tolerance, canary gate, rollback trigger, candidate freeze.
- **Acceptance**: cohort exclusion pin (§7); instant-disable pin (rolled_back candidate never injected after its stage row); freeze pin.

### W6 — Promotion + GUI completion

- **Touch** `server.go`/memory apply path (Tier-1, human-Accept): `canary → project_active` apply through the existing receipted path (§1.3) — no new writer.
- **global_active**: folded human-edit receipt + MemoryPanel "promote manually" affordance (renders candidate; human edits user.md; fold detects via user-layer snapshot, §0.3).
- Final hardening: jsonl tamper latch (§7), docs, ADR-0003 amendment note (candidate layer rides the existing invariants; no new writer of any memory layer).

---

## 7. Failure modes visible in this design — and what pins each

| # | Failure mode | Where it could loop/stall/corrupt/self-reinforce | Pin |
|---|---|---|---|
| 1 | **Re-propose loop**: rolled-back rule re-proposed every epoch by the learner (fresh flag seqs defeat flag dedup) | candidate-level oscillation freeze, §4.3 (window 6) | test: rollback at epoch R ⇒ candidate at R+1..R+6 rejected `oscillation_guard_candidate`; R+7 allowed |
| 2 | **Self-reinforcing scoring**: canary outcomes pollute the baseline the candidate is compared against | cohort isolation, §5.2 | test: fixture where counting canary outcomes in the baseline flips the gate verdict — gate must stay stable |
| 3 | **Stage stall forever**: shadow candidate never gets enough outcomes to pass tolerance | `expired` after 6 outcome-less epochs / 12 epochs total, §3.2 | test: empty-window fixtures expire with journaled reason |
| 4 | **Silent artifact corruption**: candidates.jsonl edited out-of-band (hand edit, merge, sync conflict) | boot fold verifies every line's hash; ANY mismatch ⇒ latch the learning plane fail-closed (gateDrift precedent, D1 lock), GUI warning, `memory_update{layer:"learning", cause:"candidate_artifact_corrupt", line, expected, actual}` | test: tampered line ⇒ latch; append-only writer refuses non-append mode |
| 5 | **Replay nondeterminism**: float drift or map-iteration order in projection | integer cross-multiplication only (rules_audit.go:554-560 discipline); canonical JSON (sorted keys); deterministic fold order (seq-ascending, ADR-0003 inv 4) | golden test: same slice digest ⇒ byte-identical `learning_measure` payload, run twice |
| 6 | **Wrong-slice replay**: gate replays against a different window than the candidate was created on | gate recomputes slice digest and compares to the candidate's recorded `slice_sha16`; mismatch ⇒ `replay_slice_mismatch` fail-closed, §2.4 | test |
| 7 | **Panel self-review circularity**: a moa_review row directly moves a stage | structural separation, §5.1 | test: unanimous accept fixture ⇒ zero transitions |
| 8 | **LLM summary dependency**: pipeline quietly requires episode notes | notes are separate additive rows; no reader references them | test: delete all `learning_episode_note` rows ⇒ every fold output identical |
| 9 | **Cost runaway**: probe + measure rows inflate the journal / fold windows | probes capped (≤3 active shadow candidates); measure is one row per candidate per epoch; episodes are one row per terminal outcome; distill render tombstones multi-KB synthesized rows (M17 F1 precedent) | fold-window byte budget test |
| 10 | **C0 misclassification leakage**: unjournaled C0 classifications silently re-enter cohorts | W3 journals classification (§5.2); the fold treats *unknown* classification as NOT excluded only when no auto-land row exists at all (fail-soft for legacy rows, fail-closed going forward: every post-W3 auto-land row must carry the key) | test: post-W3 row missing `c0` ⇒ measure fold journals a `learning_gap` warning row |
| 11 | **Canary attribution drift**: ride-filter formula changes between daemon versions mid-cohort | the fold reads the formula version from the candidate's gate row (`ride_pct` pinned at canary entry); changing it requires a new candidate (supersedes) | test: mixed-version journal folds coherently; formula constant pinned by test |
| 12 | **Rollback race**: harmful tuple fires while the human is mid-`apply_memory` on the same rule | stage row and the human receipt are independent appends; fold order (seq) decides; a post-rollback human apply of the same rule text trips the memory-level oscillation guard (3-epoch window) — the two guards compose | test: interleaved fixtures |

### Residual risks, stated

- The harmful tuple's 10-injection floor means a subtly harmful *retained* rule (present in both live and candidate sets) can survive the frozen replay gate if the frozen slice lacks its bad outcomes. The shadow tolerance + per-epoch re-projection catch it later; the memory-level audit remains the final backstop. Accepted: earlier detection would require a second trigger (rejected, §4.1).
- `global_active` detection depends on the human editing `~/.odo/user.md` in a recognizable way (snapshot rows). If the human edits without the daemon running, the next snapshot still catches it — the fold is newest-snapshot-based, so detection is eventual, not instant. Accepted.
- candidates.jsonl is per-project (`.odo/learning/`), committed or not per team choice; the artifact-hash latch makes divergence loud, not silent.
