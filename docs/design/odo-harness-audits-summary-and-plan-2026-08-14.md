# Odo × Harness Audits — Summary & Forward Plan (2026-08-13/14)

> Umbrella for one working session: **four blind-sealed tri-model MoA runs**
> (12 sealed legs, kimi-k3 / glm-5.2 / deepseek-v4-flash, `--thinking max`,
> orchestrator pure-consolidator) plus orchestrator mechanical arbitrations.
> Everything here is PROPOSED until the user marks waves GO.

## 1. The four runs

| # | Run | Report (+ legs) | One-line outcome |
|---|---|---|---|
| 1 | Harness architecture audit: dsh vs grok-build vs codex | `../compare/harness-tri-model-audit-2026-08-13.md` | All three are readable-source but accept zero external PRs; differentiation moved to approval automation + session durability — Odo's exact bet; ranked borrow list P0×3 / P1×5 |
| 2 | Harness GUI/interaction audit | `../compare/harness-gui-tri-model-audit-2026-08-13.md` | grok = cockpit/dashboard, codex = meticulous transcript ledger, dsh = projection host; Odo's parked-review + DiffViewer lanes remain peerless; borrow P0×3 / P1×7 |
| 3 | Odo model-routing boundary: direct-router vs OMP-core | `../compare/router-vs-omp-eval-2026-08-14.md` | **B (3/3): expand direct for thinking tasks — distill/learner/curator migrate off OMP one-shots; write path stays OMP forever; C/E rejected** |
| 4 | moa/client.go × harness client layers | `../compare/moa-client-vs-harness-audit-2026-08-14.md` | **Shore-up-first (3/3):** zero transport resilience is the only gap; four axes already beat peers (replay, truncation split, budget ledger, fail-loud) |

Provisioning notes: DSF leg of run 2 required three attempts (timeout →
instant-dead resume → fresh leg with pre-injected evidence); K3 legs of
runs 3 recovered from session JSONL after print-mode recap-only exits.
Mechanical arbitrations by orchestrator: grok `RewindMode` 3-way enum
(settled K3-vs-GLM contradiction — both half-right); codex
`resume_picker.rs` = 7,010 lines verified ×2 clones; **distill tool-free
gate verified against the live tree** (server.go:3491/3502/3587 — zero
read/glob directives; migration safe).

## 2. Cross-run synthesis (what the four runs jointly say)

1. **Odo's direction is validated, not challenged.** Every run
   independently confirmed the moats: parked async review + machine
   grading (no peer), six-layer receipted memory (dsh has none, grok's is
   off, codex's is new), unanimous panel vs codex's single Guardian judge,
   deterministic LLM-free metrics, workstream concurrency vs
   single-occupant designs.
2. **The industry converged on the Claude-Code surface** (SKILL.md,
   AGENTS.md, MCP, hooks). Differentiation lives exactly where Odo lives:
   approval automation, session durability, review ledgers.
3. **Odo's model access should be two-lane by rule** (run 3): OMP for
   worktree side effects, direct moa for daemon-held-input thinking tasks —
   because direct is the only lane where model-visible ⟺ logged is
   byte-enumerable, and the observed OMP failure class (recap exits, dead
   resumes, mid-gather kills) lands precisely on Odo's one-shots.
4. **The moa client is one S-wave away from carrying that lane** (run 4):
   add bounded retry + typed errors + usage surfacing before anything
   migrates.
5. **GUI borrows cluster around watching, not gating** (run 2): background
   task registry + attention ordering + ledger-cell rendering — all serve
   long unattended autonomy; every "synchronous modal" pattern was
   rejected.

## 3. Unified forward plan (all waves across all sources)

Legend: cost S <1d / M 1–3d / L >3d. Status: ✈ in flight · ⏳ queued ·
◎ proposed-needs-your-GO · ⇢ deferred/conditional.

| # | Wave | Content | Src | Cost | Depends | Status |
|---|---|---|---|---|---|---|
| 0 | fix-INT W1 | accept TOCTOU / `base_stale_at_land` / bridge REVIEW 330→900s | prior session | — | — | ✅ `86b2351` |
| 1 | fix-INT W2 | fold allowlist / cap-drop journaling / #14 memory-pins + **send-closure assertion** | ledger | — | — | ✅ `f17da7b` |
| 1a | fix-INT W4 | `moa_fs_deny` replace→union merge (ADR-0004) | ledger | — | — | ✅ `8ad385c` |
| 2 | **R-W1 moa resilience** | bounded retry (≤3, jitter, Retry-After; never 4xx) + typed `Error{Status,Class}` + `Result{InputTokens,Wall,TokPerSec}`; hermetic httptest pins | run 4 #1–3 | S | none — client-internal | ◎ |
| 3 | R-W1.5 receipts fill | panel/review payloads += `request_sha16`+`request_bytes` | run 3 §4 | S | ~~fix-INT~~ **unblocked** (W1/W2 landed) | ✅ worktree `6a802429` — receipt computed at the moa client's marshal point (the prompt's R-W1 premise was absent; see thread), final-request convention, wire-exact pins in `TestRequestReceiptWireExact`/`TestReviewDiff`/`TestPanelTruncationFlagged` |
| 4 | A-P0 #1 Guardian taxonomy + ledger cells | risk classes + actor/outcome/TimedOut on every `review_action`; aggregate in `odo autonomy audit`; GUI renders the cells (LedgerPanel) | run 1 §5#1 + run 2 #3 | S | own tri-model design round | ⏳ |
| 5 | A-P0 #2 visible⟺logged assert | daemon-side pre-send assertion on the send path | run 1 §5#2 | S | — | ⚠ partially landed via fix-INT W2's send-closure assertion (`f17da7b`) — residual coverage needs a 5-min code check before scheduling anything |
| 6 | A-P0 #3 durable steer inbox + queue dock | journal `steer/queued/spliced` + ChatSurface QueueDock (auto-drain, send-now chord) | run 1 §5#3 + run 2 #4 | S–M + M | park-and-switch design session | ✅ daemon impl landed (W6); GUI dock = future GUI wave |
| 7 | **R-W2 distill → moa** | behind prefs flag `distill_via: omp`; deadline policy for 1446s worst case; modelspec entry precondition | run 3 §4 + run 4 §2 | S | **2** (resilience first) | ◎ |
| 8 | R-W3 learner/curator → moa | parsers/vet untouched; ADR-0003 inv7 wording | run 3 §4 | S | 7 telemetry | ⏳ |
| 9 | R-W4 Design-MoA | consolidator (`moa.Query`, `design_lock` event, strict truncation) + blind legs (`QueryWithTools`, round-cap decoupling, per-round context accounting, executor root param) | run 3 §4 + run 4 #4–6 | M | 2, 7, 8 | ⏳ |
| 10 | GUI Wave A | background-task registry + StatusBar "still running" + attention-ordered Sidebar workstreams (never steal focus) | run 2 #1–2 | M | daemon task registry substrate; rows ride #4's schema for ledger cells | ⏳ |
| 11 | GUI Wave B | context-pressure meter; plan per-line comments (opt-in per workstream); session-scoped edit grant (post-reviewer only); per-turn stats strip; MoA panel picker | run 2 #5–9 | S–M | 10 | ⏳ |
| 12 | Backlog P1/P2 | declarative rules file (can-only-tighten); OMP structured-verdict flag check; turn-fork store op (+ GUI fork action, P2); hybrid journal recall; paste-chips / compaction disclosure / FTS / statusline items / git-action buttons / `/loop`-`monitor` | runs 1–2 §lists | varies | per item | ⇢ |
| 13 | R-W0 ADR-0005 | model-routing boundary rule as ADR; file upstream asks (echo/schema/compaction events) | run 3 §3 | S (doc) | user's GO on run 3 | ◎ |

### Immediate next three actions (my recommendation)

1. **◎ R-W0 ADR-0005** — I draft `docs/adr/0005-model-routing-boundary.md`
   (doc-only, encodes the boundary rule + the action→mechanism table).
2. **◎ R-W1 moa resilience** (S, fully hermetic) — single K3 implementation
   + 3-way blind review ≥2/3 ACCEPT, per the mechanical-fix route.
3. Then R-W2 distill migration behind the flag; GUI Wave A waits for the
   daemon task-registry design (its own tri-model design session).

## 4. Decision log / open questions for the user

- [ ] GO on run 3 verdict (B) → unlocks #13/#2/#7.
- [ ] GO on run 4 shore-up (#2) — same dispatch, adjacent files; can bundle
      with the ADR in one wave since ADR is doc-only.
- [ ] GUI Wave A start timing (after fix-INT lands? in parallel track?).
- [ ] A-P0 #1/#2/#3 sequencing vs fix-INT W2 (fold or split).
- [ ] Persist format preference (this umbrella + 4 reports is the current
      shape; alternatives: ADR-first, or milestone specs per wave).

## 5. Anti-scope (locked by the runs — do not re-litigate)

C (all-OMP), E (own write-runner), single-judge Guardian, whole-process
sandboxing, hashline edit grammar, plugin marketplaces, capability-seam
formalism, one-shot-only approval vocab, sync modal approvals with no park
path, display-only diffs, reject-reverts-shared-tree, resume-on-view
spawns agent, SSE in moa/client, peer telemetry/auth/plugin machinery.
