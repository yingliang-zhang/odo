# DESIGN LOCK — Control-Plane Hardening (pre-self-learning waves)

- Date: 2026-08-27. Anchor: odo HEAD `bd95898`.
- Provenance: 4-leg blind-sealed design (kimi-k3 / glm-5.2 / deepseek-v4-flash / gpt-5.6-sol, `--thinking max`, 900s each, same brief) + deep audit findings (1×P0, 3×P1, 2×P2). Full leg outputs: `/tmp/tri-dcp/{k3,glm,dsf,sol}/out.md` (GLM full snapshot `/tmp/control-plane-hardening-waves-1-2.md`).
- User rulings: **① K3/Sol** (boundary) · **② Sol** (manifest/Tier-0) · **③ K3** (verdict policy) · **④ Sol hybrid** (memory candidates).
- Doctrine: journal-first (evidence rows before actions), fail-closed on ambiguity, additive journal shapes only (ADR-0002 immune), minimal diff inside existing file shapes.

---

## D1 [P0] Structural gate-source protection + loop Mode A reroute

### Boundary (ruling ① K3/Sol)

`internal/ipc/gatepolicy.go`:

```go
// Tier-0: human-only. Editing these files IS an exemption grant; no pipeline
// actor may land them — attestation included (panel judging a policy edit is
// downstream of the policy it judges: circular).
var gateTier0Files = []string{"internal/ipc/gatepolicy.go", "internal/ipc/gate_manifest.json"}

// Tier-1: the control plane. Directory-boundary rule — every current and
// future file under these prefixes is gate source.
var gateProtectedPrefixes = []string{
    "internal/ipc/", "internal/store/", "internal/git/",
    "internal/moa/", "internal/adapter/",
}
// Root entry is gate source too (ruling ① includes 根 CLI): main.go and any
// standalone cmd/ tree. internal/ipc/cmd_*.go stay Tier-1 via the ipc prefix
// (K3 lean recorded; they are the human's control levers — attestable, not
// Tier-0). Deliberately NOT protected: internal/modelspec (timeouts/budgets
// only — misuse fails closed), gui/.
```

`isGateSourcePath(p)` = case-folded prefix match (both lists). `isGateTier0Path(p)` = Tier-0 membership.

### Manifest + drift latch (ruling ② Sol)

- Tracked `internal/ipc/gate_manifest.json`: `{version, protected_prefixes, tier0_files, tier0_sha16{...}, pinned_at, pinned_by:"human"}`. Manifest's own hash slot empty (self-reference); Tier-0 status is compiled into gatepolicy.go — deleting the manifest cannot widen the rule.
- Startup (NewServer, before serving): recompute sha16 of each tier0 file. Mismatch/missing ⇒ journal `memory_update{layer:"gate_policy", cause:"gate_source_drift", detail, expected_sha16, actual_sha16}` + latch `gateDrift`. While latched: `autoLand`, `loopFixPipeline`, and `settleDraft`'s majority valve refuse with `auto_land_blocked{reason:"gate_policy_drift"}` — fail-closed until human re-acknowledges.
- Human-only CLI `odo gate re-pin` (cmd_retract.go shape): recomputes hashes, rewrites manifest, instructs human to commit both files. Never commits.

### Enforcement

- `handleDiffAction` accept-time guard (before attestation branch): Tier-0 hit + `actor != ""` ⇒ hard error naming the file, diff stays pending. Human Accept (`actor == ""`) is the unconditional escape. Tier-1 hit + `!panelVerdictAttestsDiff` ⇒ existing block, predicate repointed to `isGateSourcePath`.
- `autoLandCheck`: pre-panel hard block `auto_land_blocked{reason:"gate_core_path"}` on Tier-0 (saves the panel spend).
- **Mode A reroute (Sol)**: `loopFixPipeline` classification order: memory prefix → Tier-0 (`loop_suspended{cause:"risk:gate_core"}`) → supply-chain → Tier-1 via `s.autoLand(ctx, d, wtPath, goal, false, "")` **verbatim** (C8 inherit-never-fork) — REPLACES `runVerifyGate` + `handleDiffAction(loopActor)`. The verify-only landing path for loop fixes is deleted: the judged cannot rewrite its judge without its judges.
- Fold attribution: `deriveLoopStates` attributes an accept/blocked row to the open Mode A fix phase iff (a) actor ∈ {loopActor, autoActor} AND (b) a `loop_diff_bound{round}` row names the diff. No binding ⇒ no attribution (fail-closed).

### Migration + guards

- Delete `protectedGateFiles` (server.go:5528-5539); all 10 legacy entries are covered by the ipc prefix. `isProtectedPath` = memory prefixes OR `isGateSourcePath`. One-time human commit adds gatepolicy.go + gate_manifest.json.
- **classifyDiff guard (K3)**: autonomy C0 remains memory-prefix-only. Do NOT fold gate-source hits into C0 — widening C0 would mark every daemon diff C0 and pollute the autonomy ladder. Dedicated test pins this.
- Renames: `git.PatchPaths` returns pre-image paths too (both-sides, rejectMemoryPaths discipline) — a rename out of a protected prefix is gate-source on both sides.
- `server_test.go:1119` currently pins `internal/ipc/server.go == false` as desired — flip to true, delete the stale comment.
- settle.go majority-valve exclusion extends by construction via the repointed predicate; minimize settle.go edits (protected-path human Accept expected if touched).

Journal shapes (additive): `review_action{action:"gate_policy_check", cause:"ok"|"drift", tier0:[{file,sha16,ok}], actor:"daemon"}` once per start; `auto_land_blocked{reason:"gate_core_path"|"gate_policy_drift"}`; `loop_suspended{cause:"risk:gate_core"}`.

Tests: structural walk (all `internal/ipc/*.go` true; modelspec/gui false; case-fold true), protected-path flip, Tier-0 refused for actors / human lands, drift latch blocks + re-pin restores, loop fix Tier-1 routes through panel (moa_review row BEFORE accept; no verdict ⇒ never landed), Tier-0 suspends, fold attribution with/without binding, classifyDiff C0 purity.

---

## D2 [P1] Repo-grounded reviewer leg

- **One grounded leg per fan-out**, deterministic: prefs `grounded_reviewer: model@provider`; resolution = exact match against the fan-out's model line, fallback = line's FIRST entry; journaled `resolved_by: prefs|first`. Its verdict weighs like any other leg this wave (no extra authority; D8 covers weighting later).
- Executor: `newFSToolExecutorRooted(projectRoot)` wrapped in `scopedToolExecutor` allowlist decorator → `client.QueryWithTools` (design-leg plumbing, design_moa.go:165-183).
- Allowlist (LLM-free, hashed `scope_sha16`): touched paths ∪ touched package siblings (same dir) ∪ repo files importing touched packages (bounded grep, existing caps) ∪ repo-internal packages the touched files import. Non-Go paths degrade to touched + same-dir. Computation failure ⇒ touched-only + `scope_truncated: true` (fail-visible). Out-of-allowlist reads return a model-visible tool error, journaled — **model-visible ⟺ logged holds for refusals too**.
- Caps: `maxRounds = 8` (client ceiling 16), `fsReadBytesCap` 64KB/read, new total cap 256KB across rounds; wall deadline unchanged.
- Prompts: `buildReviewPrompt` gains `grounded` framing (read-only tools, check missed callers / interface constraints / schema drift / cross-file invariants; every read journaled). `auditSystem`'s "Do not review what the diff does not touch" is REPLACED for the grounded leg only (diff is the subject; repo reads scoped to the import neighborhood); ungrounded legs byte-unchanged.
- **Gate-source grounding required (Sol)**: prefs `grounded_review_required: always|gate_sources|never`, default `gate_sources`. A degraded grounded leg on a gate-source diff = `Infra` (round fails closed via panelInfraLeg).
- Budget exhaustion ⇒ the leg still owes a verdict (flagged `tool_budget_exhausted: true`, journaled); no verdict token ⇒ existing fail-closed degradation.
- Receipts (additive): `ReviewResult`/`auditLegResult` gain `grounded`, `tool_calls` (cap 64 + truncated flag), `read_bytes`, `scope_sha16`, `scope_files`, `tool_budget_exhausted`; rows: `moa_review`, `loop_audit_round.legs[]`. Executor appends each ToolAudit BEFORE output returns to the model (citing-without-calling is test-detectable).

Tests: one-hop scope fixture, receipt mirror (every read appears in tool_calls and vice versa), budget caps trip + verdict still parsed, gate-diff-requires-grounded (degrade ⇒ round fails closed), grounded/ungrounded prompt bytes.

---

## D3 [P1] Real executor token ledger (measured, not estimated)

- Extractor (internal/adapter, next to parseSessionJSONL): `SessionUsage(sessionDir) (Usage, bool)` — sums per-assistant-message `usage{input, output, cacheRead, cacheWrite, cost}` across the run's `*.jsonl` (live-transcript-verified shape, adapter/omp.go:288). LLM-free; missing/no-usage ⇒ ok=false (fail-soft, never fabricated).
- Receipt at drain (terminal tail, BEFORE pipeline branch): `loop_event{kind:"loop_run_usage", loop_id, mode, kind_run:"fix"|"implement", run_id, covers_spawn_seq, usage_available, reason?, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, spent_tokens}`. `covers_spawn_seq` = the phase's spawn row seq (0 ⇒ fold matches by round/task).
- `loopRowSpend` folds: executor `input + output + cache_write` from usage rows (cacheRead journaled, NOT budgeted); per leg/proposal/consolidator `request_bytes/4` input estimates.
- Double-count rule: spawn row's `prompt_tokens_est` folds only until its covering usage row lands (`estPending map[spawnSeq]est`; on usage: `spent -= est; spent += usage`). Writer recomputes the same cumulative (C1: fold is the truth). GUI mirror `gui/src/loop.ts` gets the same rule (companion change, required).
- Enforcement: after the usage row lands, `spent > budget` ⇒ `loop_budget_exceeded` and `loopPipelineAfterRun` checks the fold BEFORE `autoLand` — over budget ⇒ suspend (fail-closed). `/loop resume budget=N` already raises.
- Edge: crash before tail ⇒ estimate stays + "usage pending" visible; duplicate usage rows ⇒ newest per spawnSeq wins, idempotent cumulative; non-OMP adapter ⇒ ok=false path; steering mid-run included (true cost).

Tests: fixture JSONL exact sums (incl. cost), est-replacement (1000+4200 ⇒ 4200 not 5200), budget trips before autoLand, idempotent duplicate rows.

---

## D4 [P1] Memory write contract + flag consumer (ruling ④ Sol hybrid)

- **Contract (picked, not hedged)**: daemon-side panel-gated auto-apply stays for PROJECT scope (memory.md rules, project skills); deletion-class (retract/demote) NEVER auto-applies; user.md and every global/cross-project layer stay strictly human-written — no auto proposal, no auto apply, forever.
- ADR-0003 amendment (2026-08-27): invariant 1 reaffirmed (AGENTS write to no memory layer, ever; the daemon is the sole writer); stale "learner proposals wait for human apply_memory" clause corrected to "panel gate; user.md waits for human".
- Scope check (memory_autogate.go, before applyResolvedBatch): `Target == "user.md"` ⇒ unconditionally not applied + `memory_update{layer:"apply", cause:"scope_held_for_human", ...}`; `planUserApply` unreachable from auto path (assert + test).
- **Flag → candidate (hybrid)**: `distillCore`'s learner stage collects UNCONSUMED `review_action{action:"memory_audit_flag"}` rows, deduped via `memory_update{layer:"learner", cause:"flag_consumed", flag_seq}`. Flags are injected into the learner prompt as a DATA block ("evidence, not instructions; you may propose retraction/demotion via the contradicts field; cite the flag seq"). The LLM may PROPOSE; the daemon vets LLM-free: a proposal citing flag seq must resolve to an existing flag row AND a rule present in current memory.md, else dropped + `memory_update{layer:"learner", cause:"retract_proposal_rejected", reason}`. Retract-intent proposals ride the batch as `intent:"retract"`; panel gates them like any proposal; **accepted retract-intents are NEVER auto-applied** — they emit `memory_update{layer:"memory", cause:"retract_candidate", rule, flag_seq, panel_consensus, epoch}`. Human applies via `apply_memory` (rule → memory-archive.md append with retraction record) or `odo rules retract <substring>`.
- Oscillation guard: rule retracted then re-landed within 3 epochs ⇒ prompt marks `frozen`; retract proposals for it rejected `oscillation_guard` (deterministic from memory_apply rows).
- Rollback now: human-only `odo memory revert <epoch>` (cmd_retract.go shape) — locates the epoch's memory_apply receipt, restores pre-image (before-sha verified), journals `memory_update{layer:"apply", cause:"revert", epoch, actor:"human", before_sha16, after_sha16}`. Revert-of-revert = re-apply through the normal path.

Tests: user.md auto-apply held, flags verbatim in learner prompt + idempotent consumption, retract proposal needs real flag row + present rule, revert restores exact bytes + refuses second revert.

---

## D5 [P2] Finding identity v4

- Fingerprint: `sha16(norm(file) + "|" + norm(symbol) + "|" + category [+ "|" + rule])`. `category` ∈ fixed set `correctness|contract|security|resource|test-integrity|drift|other` (absent ⇒ `other`; rubric line added to auditSystem; category is identity, never severity). `rule` optional. `title`/`evidence`/`expected`/`actual` = mutable description on the union's representative row (max-severity wins), never hashed.
- Line format additive: `- sev: P2 | file: p | symbol: s | cat: contract | title: t [| rule: R]`; old 4-field lines parse with `cat=other` (backward-compatible mixed window).
- **Per-leg dedup BEFORE Legs counting** (unionFindings): dedup each leg by FP first (max severity per leg), then fold across legs; `Legs` = distinct legs reporting the FP; additive `leg_ids` journaled. Kills same-leg inflation (2026-08-26 7-round citation loop).
- Migration: v3 FPs stay historical identifiers (append-only); first post-upgrade round counts boundary findings as new for one round — no false stall (`loopStallCheck` arms only after a landed fix); C6 closure matches verbatim row text, not hash — unaffected.

Tests: same file/symbol/cat different titles ⇒ same FP; per-leg dedup (1 leg twice ⇒ Legs=1; 2 legs ⇒ Legs=2); old rows parse; mixed-window no false stall.

---

## D6 [P2] Design-MoA diversity gate

- Additive `DesignProposal` fields: `endpoint` (scrubBaseURL — honesty about the single gateway), `model_family` (pure fn in modelspec: basename prefix before first `-`; `t9s/kimi-k3`→`kimi`, `gpt-5.6`→`gpt`; unknown ⇒ raw basename).
- `runDesignMoa` computes over the GOOD set: `diversity{legs_successful, distinct_families, distinct_endpoints, single_endpoint}`; rides existing rows (additive payload keys).
- Auto gate (`loop_design_gate:auto`): require `legs_successful >= 2 AND distinct_families >= 2`. Failure ⇒ lock journaled with `auto_gate:"refused_diversity"`, `spawnLoopImplement` NOT called — designLockSeq stays pending for the human gate (exact `loop_design_gate: human` state). Fail-closed to the stronger gate, never skip the task. Manual path unchanged (visibility only).
- Same model under two provider labels ⇒ same family ⇒ refused (label diversity is not model diversity).

Tests: modelFamily table; 2 legs same family refused / 2 families pass / 1 leg refused; manual 1-leg still locks; refused ⇒ pending + no implement spawn.

---

## D7 [P1] Verdict policy (ruling ③ K3 — general settle table)

- `settlementClass` additive classes:
  - all-accept, no infra ⇒ landed (unchanged).
  - **`reject_independent`**: ≥2 reject legs from ≥2 distinct model families ⇒ auto-reject (existing M20 mechanics: reject row + supersedeChain + advisory).
  - **`reject_minority`**: exactly 1 reject leg, OR ≥2 rejects all same family, no infra ⇒ SUSPEND: journal `auto_land_blocked{reason:"panel_minority_reject", detail, reviews, consensus_verdict:"reject_minority", patch_sha16, repanel_count}` BEFORE action; NO reject row, NO supersedeChain; diff stays PENDING; transcript advisory (human triage via inbox accept/reject).
  - infra leg ⇒ panel_infra blocked (unchanged, retryable).
- **Verify failure stays OUT of the auto-reject set**: verify_failed lives in the blocked+revise lane only (implementation evidence, not direction evidence; a flaky verify must not kill direction).
- Single-family panel ⇒ no auto-reject capacity at all (everything minority-suspends) — fail-closed, visible.
- Recovery: `pipelineTerminalDiffIDs` treats `panel_minority_reject` as NOT terminal ⇒ restart recovery re-fires the pipeline once (fresh panel); `repanel_count >= 2` ⇒ terminal, parked human-only (inbox still shows the diff).
- Unanimous accept, needs_fixes ladder, `panelInfraLeg`, `consensusVerdict` tally: unchanged. Classification lives in `settlementClass` + the recovery filter (~40 lines, no new event types).
- Loop interaction (post-D1): a minority blocked row attributes via loop_diff_bound ⇒ `fixOutcome="unlanded"` — same advisory lane as verify failures; the loop's audit engine owns convergence (no loop suspension).

Tests: settlement classes (1 reject / same-family 2 / distinct-family 2), minority suspends (no reject row, pending, blocked row + advisory), independent auto-rejects, recovery re-fire bounded at 2, loop fold unlanded.

---

## D8 — Leg calibration (report-only this wave)

- Leg key = (scrubbed endpoint, model_family, model); rows already accumulate — no new storage.
- Ground truth = journaled human outcomes (reject-after-accept, accept-after-minority, verify_failed-on-accepted, confirmed findings). Per-leg precision computed daemon-side, journaled `review_action{action:"leg_calibration", leg, window, decided, agreed, precision, n}`; published only at n ≥ 20; below that legs ride unweighted.
- **Never in consensus math, never self-scored** (report-only; weighting is a future wave and may only tighten).
- `diversity:{endpoints, families}` marked on every moa_review row; prefs validation warns when the review line collapses to one endpoint+family pair (`single_judge_panel` advisory precedent).

## D9 — Learning Control Plane roadmap (waves 3-6; NOT this wave)

Terminal outcome → learning_episode → immutable candidate (content-addressed, append-only `.odo/learning/candidates.jsonl`) → lint/security + **frozen replay** (= LLM-free projection of the candidate rule set against a FROZEN journal slice; zero model calls — K3 disambiguation and Sol mechanics agree) → shadow → canary → project_active → global_active (**human-only forever**). Per-version provenance `{artifact_hash, source_seq, scope, uses, cost, supersedes}`; auto-rollback on rules_audit's harmful tuple; oscillation freeze. **Never-score-own-changes**: scoring cohorts exclude C0 diffs and learning-scope candidates; panel/loop verdicts are evidence, never stage-movers. This wave implements NONE of D9 except D4's groundwork (flags→candidates, candidate row shape).

---

## Implementation waves (journal-first, one dispatch per wave)

| Wave | Items | Note |
|---|---|---|
| W1 | **D1** → **D3** → **D5** | D1 first (P0; D2/D7 sit on its reroute). D3/D5 small, independent. Separate diffs, sequential dispatches. |
| W1b | **D7** → **D2** | D7 needs D1's fold change; D2 needs D1's Tier-1 classification. |
| W2 | **D4** → **D6** | Contract alignment + diversity gate. |
| W3-6 | D9 roadmap | Only after W1-W2 green and control-plane findings closed. |

Every wave: diff → verify → panel (3-model blind) → attestation for gate sources → land. D1 itself touches gate files ⇒ expect protected-path human Accept at the gate.
