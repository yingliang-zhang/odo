# DESIGN — D9 Learning Control Plane (waves 3–6 implementation)

- Date: 2026-08-30. Anchor: odo HEAD post-D8 (D1–D8 landed, adoption-lock P1–P3 landed).
- Source lock: `docs/design/control-plane-hardening-lock.md` §D9 (lines 151–154).
- Doctrine: journal-first, fail-closed on ambiguity, additive journal shapes only (ADR-0002), zero LLM in gates, human-only `global_active` + `user.md` forever.
- Scope note: this design covers **rule-set candidates** (`scope: "project:memory"`). Policy candidates (`project:policy` — panel/autonomy tuning) are named where the seams exist but are NOT built in waves 3–6; they need a separate lock.

---

## 0. Verification results — three claims in the brief/lock are wrong or stale

**0.1 WRONG (brief): "MemoryPanel already shows flagged rules + effect metrics per self-improving wave-4 design."**
Evidence: grep over `gui/src` for `memory_audit_flag|rules_audit|RulesAudit|harmful|flagged` hits only a slash-command comment in `ChatSurface.tsx:509`. `MemoryPanel.tsx` (678 lines) renders exactly: batch proposals, current files, stranded heal ops, distill-cap chip. The self-improving doc's §F wave 4 (`docs/design/self-improving-first-principles-2026-08-15.md`) was never dispatched. Flags today surface only via `odo rules audit` stdout and a `ledger.md` section (`AppendRulesAuditLedger`, rules_audit.go:683).
Consequence: flag display is in scope here (D9-W3a), not an existing surface to extend.

**0.2 STALE (brief): "learner.go, autonomy.go, settle.go are C0-protected."**
Evidence: since D1, autonomy class C0 is **memory-prefix-only** — `classifyDiff`'s D1 guard (autonomy.go:232–243, comment "C0 is MEMORY-PREFIX-ONLY") and `isMemoryPath` = `.odo/` + `wiki/` prefixes (server.go:5596). The named files are **Tier-1 gate source** via the `internal/ipc/` structural prefix (gatepolicy.go:38–43): they route through the full auto-land panel and land on unanimous attestation or human Accept (wiki/topics/auto-land-pipeline.md). The self-improving doc's "C0 protected" list (§F) predates D1's structural tiers.
Resolution: waves touching stage-actuation decision logic route **human-Accept-preferred** (project rule layered on the lock); structurally they are Tier-1 (3-leg unanimous panel attestation acceptable). Tier-0 files (`gatepolicy.go`, `gate_manifest.json`) are NOT touched by any wave — no re-pin needed.

**0.3 MISREAD (brief): "memory_replay.go — the projection machinery frozen replay will reuse."**
Evidence: `memory_replay.go` is a crash-recovery receipt replayer (newest-receipt-per-layer doctrine, heal/merge/conflict). It is not a projection engine. Frozen replay reuses: the rules-audit join (`rulesScanConversation`/`rulesConvOutcomes`/`aggregateRules`, rules_audit.go:233–560), the cohort identity machinery (injection receipts `memoryLayers` server.go:1459–1517 + `journalRuleSnapshots` learner.go:135–197), the pure apply projection (`planMemoryApply` learner.go:690–801), and paged journal reads (`ListProjectEventsPage`, store/events.go:99). `memory_replay.go` proper is reused in exactly one place: the rollback apply rides its marker-first receipt family (§4).

**0.4 LOCK TEXT TENSION (must rule):** D9 says "auto-rollback on rules_audit's harmful tuple"; D4 (lock + memory_flags.go header) says "deletion-class memory changes stay human-committed, forever."
Ruling (recommendation): **statistics-driven rollback auto-applies; model-judgment deletion stays human.** The D4 prohibition targets LLM-authored retraction proposals — an LLM's judgment about what to delete. A rollback deletes nothing by judgment: the trigger is the deterministic harmful tuple computed from human verdicts (rules_audit.go:94–97), and the restore target is a content set that was already injected and validated as the live block. Fail direction: leaving a statistically-harmful rule in production while waiting for a human click is the dangerous side; returning to the exact previously-live bytes is the reversible side. Rollback is bounded to the harmful candidate's own add-set (never opaque/human lines, never other candidates' rules) and journaled marker-first with the full evidence tuple.

**0.5 LOCK TEXT INCONSISTENCY (must rule):** provenance `{uses, cost}` on immutable, content-addressed rows is self-contradictory — a uses counter cannot mutate an immutable row.
Ruling: `uses` and `cost` are **creation-time** provenance (0 and spend-to-creation). Running counters are fold-derived from journal rows (`learning_cohort` assignments, outcome joins), never stored. The lock's intent — every version carries auditable provenance — is preserved.

---

## 1. Data model

### 1.1 `learning_episode` — the epoch's terminal-outcome projection

One row per lane per distill, journaled as `review_action{action:"learning_episode"}` at the `distillCore` tail (seam: after `journalDistillLedger`, server.go:5137), computed as a **pure fold over the just-pinned window** `[first_seq, last_seq]` (the marker's own window, server.go:5066–5081 — the fold consumes the same pin, never re-derives it).

Qualifying terminal outcomes (attribution model = rules audit's send → terminal → diff → outcome, rules_audit.go:1–60):

| Outcome | Journal source | Notes |
|---|---|---|
| accepted diff | `review_action{action:"accept"}`, split human vs `auto_panel` | weak/auto splits per M17 F5 |
| rejected diff | `review_action{action:"reject"}` incl. D7 auto `reject_independent` | actor-stamped |
| weak reject | `moa_review{consensus:"reject"}` with no later human action on the diff | the weak-map rule inside `rulesConvOutcomes`, rules_audit.go:330–452, verbatim |
| revise-ladder convergence | ladder rows: landed-after-rounds, `ladder_suspended`, `revise_no_progress` | `SettleTallies` shape (autonomy.go:117–125) |
| terminal verify block | `auto_land_blocked{reason:"verify_failed"}` | + `verify_ms` after W3a |
| rules-audit flag | `memory_audit_flag` rows in window | seqs |
| human revert | `memory_update{layer:"apply", cause:"revert"}`; diff-revert heuristic pairs | autonomy.go:65–70 heuristic |
| errored run / tainted run | `agent_error` terminals; `run_verdict` rows (`false_stop`, `no_text`) | runverdict.go |

NON-outcomes (recorded as context counts, never as verdict evidence): `panel_infra` blocks (D7: infra is not a verdict), diff-less terminals (attribution boundaries only), stage transitions themselves.

Payload example:

```json
{
  "action": "learning_episode", "epoch": 17, "workstream": "main",
  "window": {"first_seq": 402, "last_seq": 481},
  "outcomes": {
    "accepted": 3, "rejected": 1, "weak_rejected": 0,
    "auto_accepted": 2, "auto_rejected": 1,
    "verify_failed": 1, "panel_mixed": 0, "panel_minority_reject": 1,
    "revise_rounds_spawned": 2, "revise_landed": 1, "ladder_suspended": 0,
    "agent_errors": 0, "false_stops": 0, "no_texts": 1, "human_reverts": 0
  },
  "cohorts": [{"sha16": "ab12…", "outcomes": 4, "accepts": 3, "rejects": 1}],
  "flags_emitted": [977],
  "usage": {"available": true, "input": 81230, "output": 9402, "cache_write": 1200, "cost_usd": 0.182},
  "verify_ms_total": 41200, "distill_ms": 98821
}
```

Determinism pin: same journal ⇒ byte-identical episode row (recomputed in tests; catches `time.Now()` leaks — the row carries no timestamps beyond the journal's own).

### 1.2 Candidate row — `.odo/learning/candidates.jsonl`

Append-only, one JSON object per line, content-addressed. `artifact_hash` = full sha256 over the canonical (sorted-key, compact) serialization of `{version, scope, base_sha16, base_source_seq, delta, content}` — **hash covers the artifact's truth fields only** (provenance excluded: same delta on the same base = same artifact, so re-creation is idempotent and JsonL append dedupes by hash on write, O(n) scan, n stays tiny).

```json
{
  "artifact_hash": "9f2c…(64 hex)",
  "version": 1,
  "scope": "project:memory",
  "base_sha16": "ab12cd34ef56ab78",
  "base_source_seq": 411,
  "delta": {
    "add": [{"rule": "Always run go vet before claiming done", "evidence": "main-epoch-16"}],
    "retract": []
  },
  "content": "- Prefer compact output — cites: main-epoch-2; reaffirmed: 9\n- Always run go vet before claiming done — cites: main-epoch-16; reaffirmed: 17\n",
  "provenance": {
    "created_by": "learner_batch",
    "source_seq": [455],
    "propose_epoch": 17,
    "panel_receipt_seq": 449,
    "uses": 0,
    "cost": {"usage_available": false},
    "supersedes": null
  },
  "created_at": "2026-08-30T01:12:44Z",
  "created_seq": 460
}
```

- `content` = the FULL projected injected block bytes under the delta (what the prompt would carry), not a patch. Self-contained, replay-trivial, and directly comparable to snapshot bytes (`journalRuleSnapshots` pins the same render surface).
- `delta.add` carries learner-vetted fields only: `{rule, evidence}` (+ `flag_seq` when flag-driven). No reaffirms (reaffirm is not a rule-set change); retractions list normalized verbatim rule texts.
- Retract-carrying deltas are created from **human-resolved** retract candidates (`retract_candidate` rows, server.go:6400) via `odo learning from-retracts` or the GUI — never directly from LLM output.
- Creation sources: `"learner_batch"` (accepted memory.md adds — post-panel), `"human"` (CLI/GUI), nothing else.

### 1.3 Stage-state machine

GUI folds these rows into a stage table; candidates.jsonl supplies content by hash. All transitions journaled; the fold is the only state.

| From | To | Trigger | Actor | Journal rows (all additive) |
|---|---|---|---|---|
| — | `candidate` | accepted learner adds / human create | daemon | `review_action{action:"learning_candidate", artifact_hash, …base/provenance}` + jsonl append |
| candidate | `shadow` | lint + security + frozen replay ALL pass (§2), at creation, synchronous | daemon | `review_action{action:"learning_gate", artifact_hash, gate:"lint"|"security"|"replay", verdict:"pass", report_seq}` per gate + `review_action{action:"learning_stage", artifact_hash, from:"candidate", to:"shadow", cause:"gates_passed"}` |
| candidate | `dropped` | any gate fail | daemon | same rows with `verdict:"fail", detail` (per-line evidence); terminal |
| shadow | `canary` | checkpoint: ≥3 main-lane epochs aged AND replay re-PASS on grown slice AND no harmful tuple on candidate rules AND no canary slot occupied (§3) | daemon | `memory_update{layer:"learning", cause:"shadow_checkpoint", artifact_hash, epoch, metrics}` (each checkpoint) then `learning_stage{…, to:"canary", cause:"shadow_passed"}` |
| shadow | `dropped` | re-fail / harmful appears | daemon | `learning_stage{…, cause:"shadow_failed", evidence_seqs}` |
| shadow/canary | `dropped` | `odo learning drop <hash>` / GUI | human | `learning_stage{…, cause:"dropped_by_human", actor:"human"}` |
| canary | `project_active` | cohort stats no-worse (§3.4) AND delta is additive-only | daemon | `learning_stage{…, cause:"canary_passed"}` + `review_action{action:"memory_apply", …, recovery, actor:"learning_promote"}` marker-first write of `content` into memory.md |
| canary | `held_for_human` | cohort stats pass BUT delta carries retractions (D4 deletion rule) | daemon | `learning_stage{…, cause:"canary_passed", held:true}` |
| held_for_human | `project_active` | `apply_memory` / `odo rules retract` / `odo learning apply <hash>` | human | `learning_stage{…, actor:"human"}` + `memory_apply` marker |
| project_active | `rolled_back` | harmful tuple on candidate's own rules at a re-measure checkpoint (§4) | daemon | `review_action{action:"learning_rollback", artifact_hash, harmful_flag_seqs, restored_sha16, retracted:[…]}` + `memory_apply` marker |
| project_active | `global_active` | `odo learning promote --global <hash>` ONLY | human | `learning_stage{…, to:"global_active", actor:"human"}` — stages the rule line for the human to add to `~/.odo/user.md` by hand; NEVER writes user.md (D4 ruling ④ absolute) |
| any non-terminal | `frozen` | oscillation freeze (§4.3) | daemon | `review_action{action:"learning_frozen", artifact_hash, reason, window}` |

`dropped`, `rolled_back`, `frozen`, `global_active` are terminal. `rolled_back` candidates may be superseded by a NEW candidate (different content ⇒ different hash); the frozen rule TEXTS are what the freeze governs (§4.3), not the artifact.

---

## 2. Frozen replay

### 2.1 What freezes

**Reference freeze, not a byte copy.** The journal is append-only (per-conversation seq allocated `MAX(seq)+1` under the single-connection store, store/events.go:13–56; no UPDATE paths exist; `odo journal rotate` takes the daemon offline first). Immutability below a bound is structural, so a freeze is a **manifest**, journaled as `review_action{action:"learning_freeze", …}`:

```json
{
  "action": "learning_freeze", "artifact_hash": "9f2c…",
  "bounds": {"17": {"tail_seq": 250, "head_seq": 481}, "19": {"tail_seq": 33, "head_seq": 96}},
  "input_sha256": "c41a…",
  "slice_events": 1261
}
```

- Per active conversation: `head_seq` = newest distill marker seq at freeze time; `tail_seq` = the marker seq K=8 epochs back (lane head if younger). Replay reads events with `tail_seq < seq ≤ head_seq` per lane; rows arriving later never enter the slice.
- `input_sha256` = sha256 over the canonical join of the bounds + the sha of every cohort snapshot consulted (falsifiability: the replay's inputs are pinned and re-checkable).
- Consumed inputs missing (snapshot row absent, patch file pruned, journal rotated away): verdict `unverifiable` = FAIL, fail-closed, journaled with the missing key. Never interpolate.
- Cost: replay reads pages via `ListProjectEventsPage` (512-row pages, store/events.go:99); the slice is bounded by construction. Full-project scans happen once per candidate creation + once per shadow checkpoint (per main-lane epoch) — acceptable; an incremental fold is a later optimization, not a correctness dependency.

### 2.2 What "projection" means (and what it does not)

For every send/`run_prompt` receipt in the slice carrying `.odo/memory.md` block hash `h`:

1. Live block = `snapshot(h).content` (pinned bytes, learner.go:158–197).
2. Candidate block = `render(planMemoryApply(snapshotContent, deltaAdds, [], 0))` for additive candidates; for retract-carrying candidates the retraction-with-record path of the same pure function. `planMemoryApply` (learner.go:690–801) is total, pure, and already the write-path planner — one projection rule for replay and landing, zero second convention.
3. Candidate cohort hash = `sha16(candidateBlock)`; the attribution join (rulesConvOutcomes → aggregateRules, rules_audit.go:330–560) re-runs unchanged against the counterfactual cohort map.

**Epistemic honesty (locked):** the replay cannot know how an LLM would have behaved under a different prompt. It is a **hygiene + known-harm recall gate**, not a behavior predictor. Predictive evidence exists only from canary (§3). The gates therefore test everything that IS deterministic: cohort-joinable harm, format/cap invariants, budget bounds, and provenance links.

### 2.3 Pass/fail (candidate → shadow). ALL must pass; zero LLM anywhere.

| Gate | Predicate | Fail mode it catches |
|---|---|---|
| lint | `content ≤ memoryCap` (4KB, learner.go:44); every non-opaque line matches `memoryLineRe`; no dup normalized rule vs base or in-batch; every retract target exists in base (normalized); evidence note exists under `wiki/` (deterministic file check, `readWithinDir`-contained) | malformed render, phantom retraction, fabricated evidence cite |
| security | no rule line matches secret-shaped patterns (risk.go family: `*_KEY/*_TOKEN/*_SECRET` assignments, `~/.ssh/id_*`, userinfo-bearing URLs, `../` escapes); per-line reject with the pattern name journaled | rule-as-exfil-channel, planted path escape |
| replay a | no candidate-added rule's counterfactual row meets the harmful tuple | re-landing a statistically harmful rule |
| replay b | normalized add-text ∩ {texts retracted-after-harmful-flag in slice history} = ∅ | harmful text re-proposed verbatim |
| replay c | rotation projection is EMPTY — candidate fits without evicting any existing rule | silent third-party eviction outside the delta's evidence |
| replay d | injected-block byte growth vs live ≤ +512B AND final ≤ memoryCap | prompt-budget blowout (every send pays) |
| replay e | whole replay executed twice → byte-identical report | nondeterminism (`time.Now`, map-order leaks) |
| provenance | every `source_seq` resolves to a journaled row of the claimed kind (memory_propose/flag/apply) | ghost candidate with fabricated lineage |

Gate reports journal as `learning_gate` rows (per gate, with per-line/per-check evidence); the stage row cites `report_seq`.

---

## 3. Shadow & canary mechanics

### 3.1 Shadow (rules scope, waves 3–6)

The candidate is inert — never injected. On each main-lane distill the checkpoint re-runs the §2.3 replay against the slice **grown by the newly completed epoch windows** (bounds advanced, new `input_sha256`, journaled as a `shadow_checkpoint` row with the metrics map). Shadow's teeth:

- catches a candidate whose rule set becomes harm-adjacent as new human rejects accumulate on overlapping live rules;
- re-verifies cap/hygiene against the grown base (memory.md moved while the candidate aged);
- enforces aging: promotion only after ≥3 main-lane epochs of exposure-free observation.

Shadow → canary additionally requires the **canary slot free**: at most ONE candidate in `canary` per project (cohort purity — two concurrent canaries would make attributed outcomes non-separable). A shadow-passed candidate that can't take the slot stays `shadow`; the checkpoint journals `cause:"shadow_queued"` (visible, never silently stuck).

**(Named, not built — policy scope `project:policy`):** the counterfactual-decision seams for future panel/autonomy candidates are: every `journalAutoLandBlocked`/`consensusVerdict` decision point in `autoLand` (autoland.go:301–668), `settlementClass` (settle.go:196), the drain-tail `maybeAutoLand` fires (server.go:3119–3128), and ladder decisions (settle.go:773–1025). A policy shadow would journal `learning_shadow{policy, diff_id, live_decision, candidate_decision}` — evidence rows only. Not waves 3–6.

### 3.2 Canary cohort

- **Assignment**: deterministic interleave, zero RNG state. At send time the daemon folds the lane's run-start anchors; the run is canary iff `ordinal % M == 0` where `M = round(1/f)`, `f` = prefs `learning_canary_fraction` (default 0.25, hard ceiling 0.5, 0 = canary disabled project-wide). Assignment journaled BEFORE the run as `review_action{action:"learning_cohort", artifact_hash, conv_seq, run:"send"}`.
- **Continuation inheritance**: steer-continuation runs anchor on `run_prompt` rows (server.go:2235–2249); the cohort binds to the run CHAIN via the first send's assignment (continuations never re-roll — a stage flip mid-chain cannot swap a running cohort's block hash).
- **Injection seam**: `memoryLayers` (server.go:1487–1489) — when the fold says this run rides candidate H, `ml.project` is the candidate's `content` instead of `readProjectMemory(s.projectRoot)`, so the receipt `.odo/memory.md → sha16(candidateBlock)` (server.go:1488) cohorts the run with zero new receipt keys. The candidate block bytes are pinned into the journal as `memory_update{layer:"learning_canary", cause:"snapshot", source:"learning/<hash>", sha, content}` at promotion time — the exact `journalRuleSnapshots` pattern (learner.go:135–197), so receipt-hash → rule-set resolution replays identically for canary cohorts.
- **Revert = stop injecting.** memory.md is never touched before `project_active`, so a demote (`learning_stage` row) takes effect on the next send; in-flight chains finish on their bound cohort and stay attributable. Nothing to un-apply, nothing to un-write.
- **Audit isolation**: the live `odo rules audit` keeps current semantics EXCEPT outcomes whose resolving cohort hash resolves to a `learning_canary` snapshot. Those are excluded from live rule rows AND the live baseline, reported as a separate `canary_outcomes` line (the `auto_accepts/auto_rejects` line precedent, rules_audit.go:145–157). Experiment traffic never grades live rules; live traffic never grades the candidate.

### 3.3 Metrics table (gate feeds → existing journal fields)

| Metric | Source rows | Status |
|---|---|---|
| human outcomes per cohort | `review_action` accept/reject (non-auto) + weak-reject rule | exists (rules audit join) |
| verify_failed rate | `auto_land_blocked{reason:"verify_failed"}` per run's cohort | exists |
| revise rounds | `review_action{action:"auto_revise_round"}` per run-chain + ladder rows | exists |
| panel dissent | D7 `settlementClass` results, per-family reject tallies on `moa_review`/blocked rows | exists |
| verify duration | NEW additive `verify_ms` on `moa_review` + all `auto_land_blocked` verify rows | **W3a add** (gap today) |
| tokens / cost | loop runs: `loop_run_usage` (D3); regular runs: NEW `memory_update{layer:"run_usage"}` per drain tail via `adapter.SessionUsage` (omp.go:1041, fail-soft) | **W3a add** (gap today) |
| run integrity | `run_verdict` rows, `agent_error` terminals per cohort | exists |

### 3.4 Promotion predicate (canary → project_active). Deterministic, journaled as a `learning_measure` row first (§5.3):

- canary cohort has ≥ 10 resolved human outcomes (threshold mirrors `rulesFlagMinInjections`, one constant family),
- live cohort over the same window has ≥ 10 (paired contrast mandatory — a canary with no live contrast never promotes),
- canary reject-rate ≤ live reject-rate × 1.25,
- canary harmful tuple absent; canary `agent_error`/taint rate ≤ live + 5pp,
- window bounds journaled (from cohort-start seq to checkpoint marker).

Additive-only deltas promote automatically (`actor:"learning_promote"`, marker-first `memory_apply`). Retract-carrying deltas flip to `held_for_human` — the human resolves through the existing apply path. D4 is untouched in substance regardless of cohort strength.

---

## 4. Auto-rollback + oscillation freeze

### 4.1 Trigger (deterministic; the exact rules-audit harmful tuple)

At each main-lane distill checkpoint (W5), the daemon recomputes the audit (scoped fold, the §2 machinery) and tests: any rule in a `project_active` candidate's `delta.add` whose current row meets harmful = injections ≥ 10 AND human rejects ≥ 3 AND ≥ 3 reject conversations AND reject-rate ≥ 2× baseline (the rules_audit.go:94–97 constants, cited by name). Novel-detection reuses the evidence-tuple idempotence (`NovelFlags`, rules_audit.go:620) so a re-check never double-fires.

### 4.2 Mechanics

1. Restore set = the harmful candidate's `delta.add` texts, retracted from CURRENT memory.md content via `planMemoryApply`'s retraction-with-record path (archive gets the retraction record; a text already gone or re-added identically by a later candidate is journaled per-text honestly).
2. Write marker-first: `review_action{action:"memory_apply", …, actor:"learning_rollback", recovery:{memory,archive blocks}}` BEFORE file writes — the apply rides the marker-first doctrine, so a crash mid-apply is repaired by the EXISTING boot replayer (`memory_replay.go` — the one place it genuinely serves D9: rollback receipts are its candidate family, newest-receipt wins, zero new replay code).
3. Journal `learning_rollback{artifact_hash, harmful_flag_seqs, restored_sha16, retracted:[…]}` after the writes commit.
4. Advisory row into the transcript (`journalRunAdvisory` precedent) so the rollback is visible where the user reads.

Boundary pins: never touches opaque lines, user.md, pins, skills, or other candidates' adds; never fabricates a restore target (text not found ⇒ journaled per-text `not_present`, still succeeds for the rest).

### 4.3 Oscillation freeze — candidate window and the D4 guard's interplay

- **Existing lane guard** (memory_flags.go:93–146): retract→re-land ≤3 lane epochs ⇒ frozen, drives the learner prompt `[frozen]` marker and the retract-intent vet. Scope: lane-level, propose/apply pairs. A rollback marker has no paired `memory_propose` row for its epoch, so the existing fold skips it (fail-soft by design, memory_flags.go:122–126) — **the existing guard cannot see rollbacks.**
- **New candidate freeze**: rollback (or harmful-drop) of rule text T ⇒ T frozen for 3 MAIN-lane epochs (project-scoped cadence — candidates are project artifacts, main is the audit sink, `RulesAuditMainWorkstream`). Enforced at THREE points: learner vet (the `afc.frozen` set gains the candidate fold — prompt shows the marker, vet rejects), candidate lint (replay b + explicit freeze join ⇒ `gates_failed`), and stage-interrupt (a `held_for_human` candidate carrying frozen text stalls, journaled).
- Same 3 — one convention, deliberate. Window keys differ (lane epochs vs main-lane epochs) because the scopes differ; both constants cite `oscillationWindowEpochs`.
- Freeze is evidence-joined on **normalized text** (`normalizeRule`, learner.go:273) — honest limitation: paraphrase re-entry evades it (advisory fuzzy near-match MAY be journaled later; never a gate. Keep one identity rule)

---

## 5. Never-score-own-changes

### 5.1 C0-diff exclusion

Learning-gate metrics (candidate cohorts, promotion baselines) exclude outcomes whose diff classifies C0 (`classifyDiff`, autonomy.go:232 — memory prefixes / >5 files / >300 lines / new top dir): un-steerable, human-heavy work adds noise the candidate can't be responsible for. Classification over a frozen slice: patch file missing ⇒ class `unknown` ⇒ EXCLUDED with an honest count (fail-closed; the `unreadable_diffs` count precedent, autonomy.go:99-110). `.odo/learning/` paths are C0 by construction (`.odo/` prefix, server.go:5596) — learning artifacts self-scope-exclude for free.

### 5.2 Learning-scope exclusion

- Canary-experiment outcomes resolve only to their own candidate's gate metrics — excluded from live rule rows AND live baseline (§3.2, `canary_outcomes` line).
- Auto/panel/loop verdicts remain non-ground-truth everywhere: `auto_panel` outcomes excluded since M17 F5; panel rows and loop audit rows are EVIDENCE in episodes, never inputs to stage transitions.

### 5.3 Evidence → measure → gate (three separated steps, structural)

1. **Evidence**: raw journal rows (verdicts, receipts, outcomes) — nothing reads these to move a stage except the measure fold.
2. **Measure**: pure functions `fold(events) → metrics`, output journaled as `memory_update{layer:"learning", cause:"measure", artifact_hash, window, metrics}`. Deterministic (replay-e pin covers).
3. **Gate**: transition predicates consume ONLY journaled measure rows + thresholds — the Go signatures take the measure struct, never `[]store.Event`, making the shortcut evidence→gate unrepresentable in code (review pin: grep gate functions for `store.Event` params = ∅).

---

## 6. Wave slicing (one dispatch per wave; every wave through diff → verify → panel → attestation)

### D9-W3a — Pure observability (ZERO behavior change)

- `internal/ipc/learning_episode.go`: episode fold + journaling at `distillCore` tail; `unownedFoldGrowth` whitelist + "learning_episode" (+ later `learning_*` actions) — server.go:5262-5276 whitelist edit, additive, fail-closed direction kept.
- `internal/ipc/runverdict.go`-adjacent: `run_usage` memory_update rows at drain tail for ALL runs (SessionUsage wrapper, fail-soft).
- autoland.go: `verify_ms` added to verify-row payloads (additive key).
- `internal/ipc/learning_store.go`: `.odo/learning/` dir, candidates.jsonl append-only writer (hash-dedupe), reader, boot-time dir bootstrap. **Nothing consumes candidates yet.**
- `internal/ipc/learning_status.go` + protocol: `learning_status` IPC (daemon-folded flags + stage table + effect metrics — single fold, GUI renders; no dual-fold parity problem).
- `cmd_learning.go` (`odo learning status`); MemoryPanel gains a third sub-tab **Learning**: flagged rules + per-rule effect rows (the never-landed self-improving wave 4) + empty stage table; `api.ts`/`types.ts` additions.
- Gate posture: all `internal/ipc` files Tier-1 (panel attestation expected); gui/ normal pipeline. No Tier-0 touch.
- Tests: episode determinism (fixture → fixed bytes); attribution-whitelist pins (episode-only window reads as nothing-new; episode row mid-fold read as attributed); jsonl hash dedupe/append-only; IPC shape; vitest for the tab.

### D9-W4 — Candidate lifecycle core (behavior change behind pref)

- `internal/ipc/learning_candidate.go` (creation from accepted adds; retract-intent path UNCHANGED), `learning_lint.go`, `learning_replay.go` (freeze manifest + projection + gates), `learning_stages.go` (pure stage fold), `learning_measure.go`.
- Hook seams: `autoApplyProposals` (memory_autogate.go:34–61) — with prefs `learning_stages: on` (DEFAULT on; this wave IS the feature; `off` = legacy direct apply, kill switch posture mirroring `AutoApply`), accepted memory.md adds become a candidate with gate run instead of an immediate apply. `handleApplyMemory` keeps meaning as the human escape: human apply = jump candidate to `project_active`. Shadow checkpoints at main-lane distill tail; shadow→canary promotion; canary injection seam (memoryLayers snapshot sibling + cohort assignment rows + continuation binding); canary metrics + automatic additive promotion at §3.4; live-audit canary exclusion (`canary_outcomes` line) in rules_audit.go.
- Gate posture: memory_autogate.go/server.go/learner.go/rules_audit.go edits — human-Accept-preferred (Tier-1 structural minimum). `.odo-verify` untouched; no Tier-0.
- Tests: creation hash stability + dedupe; each lint/security/replay predicate with fail fixtures (rotation-eviction reject, +512B budget, harmful re-add join, determinism double-run); stage fold table transitions; cohort interleave (ordinal mod M) + continuation inheritance; canary promotion predicate boundary (9 vs 10 outcomes, exactly-1.25× boundary integer cross-multiplied, the rules_audit.go:553–556 float-trap precedent); legacy pref-off parity (bit-identical apply path).

### D9-W5 — Rollback + freeze + never-score

- `internal/ipc/learning_rollback.go` (trigger, restore planning, marker-first apply, freeze fold), C0/canary exclusions in `learning_measure.go`, three-step separation enforcement.
- GUI: Learning tab gains stage transitions feed + rollback/frozen display + human drop/promote buttons (IPC: `learning_action` — human-only commands, journaled with actor).
- Tests: rollback happy path (bytes + archive record + replay-engine recovery of a mid-write crash via the marker), boundary (not_present text, opaque preservation), window freeze at 3 main epochs with boundary fixture (exactly at 3 = frozen, 4 = free — integer edge pin), C0 exclusion with synthetic patch classes, evidence→gate greppable-separation test, never-score exclusions (C0 outcome present but uncounted).

### D9-W6 — Human global promotion + hardening

- `odo learning promote --global <hash>` (stage row only + prints the rule line; never writes user.md), `odo learning drop/apply` CLIs, stall reporting (`learning_stall` advisory: candidate aging > 30 main epochs short of cohort minimums — surfaced, never auto-promoted on staleness), docs + ledger sections.
- Tests: promotion never-writes-user.md pin; global stage row shape; stall advisory fires.

---

## 7. Failure modes, pins, and tests

| Failure mode | Mechanism | Pin / test |
|---|---|---|
| **Rollback→re-propose loop**: learner re-proposes rolled-back rule, cycle repeats | candidate freeze (§4.3) consulted at vet + lint; frozen texts ride the learner prompt marker | fixture: rollback at epoch N, re-propose at N+1 ⇒ rejected `oscillation_guard`; N+4 ⇒ free |
| **Promotion starvation / silent stall**: quiet project never reaches 10 outcomes | minimums are explicit; `learning_stall` advisory; NEVER auto-promote on age | staged fixture with 9 outcomes over 40 epochs ⇒ still canary + advisory present |
| **Self-reinforcing cohort**: candidate graded with no live contrast | paired-cohort requirement §3.4 (both ≥10) | fixture with canary-only traffic ⇒ no promotion |
| **Cross-cohort misattribution**: steer-continuation injects a different block than the send | chain-bound cohort (§3.2); continuation never re-rolls | fixture: stage flip mid-chain ⇒ continuation keeps first block hash |
| **Candidates.jsonl tampering / drift**: stage rows cite hashes that don't resolve | fold validates hash chain: stage row with unresolvable hash ⇒ stage reads `invalid`, journaled, transitions refuse (fail-closed) | corrupt one byte in fixture jsonl ⇒ fold reports invalid + row |
| **Replay nondeterminism**: map iteration / clock leaks flip results | replay-e: double execution must be byte-identical (runtime) + fixed-fixture bytes (unit) | both pins above |
| **Moved replay inputs** (journal rotated, patch pruned) silently weakening tests | `unverifiable` = FAIL, journaled with the missing key | rotated-away fixture slice ⇒ fail + key named |
| **Metric gaming**: learner drafts rules that pass replay but fail live | replay is hygiene-only by design; canary is the only path to active; canary stats decide; replay alone never promotes | structural (no promotion path skips canary) |
| **Budget blowout**: injected block grows until prompts degrade | replay-d byte-growth cap (+512B) + memoryCap absolute; canary adds verify_ms/token deltas to the no-worse predicate | fixture: +513B candidate fails lint/replay-d |
| **Stage-fold skew GUI vs daemon** | single fold: GUI renders `learning_status` IPC output (daemon fold), never re-folds | IPC shape test; no TS fold exists to skew |
| **Episode rows poisoning the fold guard**: learning bookkeeping counts as journal growth → phantom distills / supersession false-trips | whitelist additions in `unownedFoldGrowth` (server.go:5259) enumerated and pinned | episode-only window ⇒ "nothing new"; mid-fold episode ⇒ attributed |
| **Auto-rollback over-reach**: trigger replaces more than the candidate's own harm | restore set = harmful candidate's delta.add ∩ current content, retraction-with-record; opaque/other-candidate rules unreachable by construction | fixture: two candidates share a text ⇒ rollback journals per-text outcome, never double-removes; human opaque lines present ⇒ untouched |
| **Threshold float-trap** at 1.25× / 2× boundaries | integer cross-multiplication, the rules_audit.go:553–556 precedent | exact-boundary fixture promotes/fails on the locked side |
| **Canary contaminating live stats** | exclusion line in rules_audit cohort resolution (`canary_outcomes`) | fixture: 100% canary traffic ⇒ live rows empty, line correct |

---

## Appendix A — Verified seam index (file:line, checked 2026-08-30)

| Purpose | Location |
|---|---|
| Rules-audit attribution join + thresholds | internal/ipc/rules_audit.go:233–330 (scan), 330–452 (conv outcomes), 452–560 (aggregate), 94–97 (flag constants), 620 (NovelFlags) |
| Flag consumption + oscillation guard | internal/ipc/memory_flags.go:48–78 (collect), 93–146 (frozen), 224–265 (vet) |
| Pure apply projection | internal/ipc/learner.go:690–801 (`planMemoryApply`) |
| Snapshot cohort pinning | internal/ipc/learner.go:135–197 (`journalRuleSnapshots`) |
| Injection + receipt seam | internal/ipc/server.go:1459–1517 (`memoryLayers`), 1487–1489 (project receipt) |
| Episode hook point | internal/ipc/server.go:4806–5154 (`distillCore`; tail after 5137 ledger) |
| Fold-growth whitelist (episode attribution) | internal/ipc/server.go:5259–5302 (`unownedFoldGrowth`) |
| Auto-apply hook → candidate creation | internal/ipc/memory_autogate.go:34–61 (`autoApplyProposals`) |
| Panel decision + blocked evidence rows | internal/ipc/autoland.go:292–668 (`autoLand`), 1316–1372 (journal fns) |
| Settle table (untouched by these waves) | internal/ipc/settle.go:196–218 (`settlementClass`) |
| Shadow seam (policy scope, named) | internal/ipc/server.go:3119–3128 (drain tail), autoland.go:271–290 (`maybeAutoLand`) |
| Tier structure | internal/ipc/gatepolicy.go:30–56 (Tiers), 38–43 (prefixes) |
| Receiver-first write doctrine (rollback rides it) | internal/ipc/memory_replay.go:1–120 (doctrine), 197–300 (receipt fold) |
| Paged project reads (replay cost bound) | internal/store/events.go:99–148 (`ListProjectEventsPage`) |
| Session usage extractor | internal/adapter/omp.go:1029–1061 (`SessionUsage`) |
| Usage receipts (loops) | internal/ipc/loop_run.go:586–700 (`loop_run_usage` rows) |
| Gate-source structural coverage for learners' artifacts | internal/ipc/server.go:5596–5599 (`isMemoryPath` covers `.odo/learning/`) |
