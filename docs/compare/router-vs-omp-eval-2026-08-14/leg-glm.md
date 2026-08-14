Working...
I have complete grounding. Writing the A–F deliverable now.

## A. Recommendation

**Primary: B — expand direct-router deliberately, phased conservatively.** The decisive axis is receipts exactness, and it is *structurally asymmetric*: on the moa path the daemon builds the entire wire body (`internal/moa/client.go:148` `messageRequest`, `:246` `post`), so the model-visible⇔logged closure can cover everything sent; on the OMP path the daemon writes a prompt *file* (`internal/adapter/omp.go:276`) and OMP assembles its own system prompt, tool definitions, and compaction outside both daemon control and the closure. That closure is **already shipped** — M18 W2 item 4 — fail-closed on both the send path (`internal/ipc/server.go:648` `assembleRunPrompt`) and the slash path (`internal/ipc/slashctx.go:186` `assertSlashReceipts`, called at `server.go:2190` and `:2417` before any moa call). It attests the injected memory layers byte-identical against journaled sha16 receipts; OMP's internal additions escape it by construction.

The specific actions the user names are all *thinking tasks with no write surface*, and three are already on moa today (review `server.go:1942`, /panel `:2162`, vision `:2421`). Distill is the textbook migration candidate: `runDistillAgent` (`server.go:3491`) calls `runOneShot` (`:3502`) which spawns a *full OMP agent process* (wrapper + session dir + output file + 200 ms poll loop) for what is a single completion — the exact overhead `moa/client.go:1-4` says it exists to eliminate. MoA consolidate (Design-MoA) and mid-run context repair are net-new thinking tasks; routing them direct is the natural shape and is the only path that can give schema-constrained verdict output, which the prior audit found "depends on OMP exposing a flag" (`docs/compare/harness-tri-model-audit-2026-08-13.md` §5 #5) — i.e. unattainable on OMP without upstream work (Option D).

OMP stays the core runtime for *write-capable* agent runs (`handleSendMessage` → `adapter.Start`), where the agent loop (edit/shell/verify/retries), the M7 streaming preview bubble (`omp.go:282-296` `--mode json`), and session durability are the value Odo deliberately doesn't reimplement. No topology change there.

**Runner-up: A — formalize the status-quo boundary.** A and B share the same first wave (the closure is already shipped; new thinking actions go direct per the ADR). They diverge only on distill migration + tool-loop productization.

**Trigger that flips B→A:** the distill OMP run is *not* tool-free — if `runDistillAgent`'s orchestrator run uses read/glob tools to explore files (rather than answering from the daemon-assembled journal context), a single `moa.Query` can't replicate it and distill must stay OMP. At that point B collapses into A. Secondary flip: journaling the exact moa request body collides with the in-flight fix-INT journal-semantics wave (`server.go:648` comment, uncommitted W1), making the gap-fill itself a blocker.

---

## B. Option-by-axis matrix

| Axis | A. Formalize status quo | B. Expand direct (deliberate) | C. OMP-core everything | D. Invest in OMP flags upstream | E. Full direct write-runner |
|---|---|---|---|---|---|
| **Receipts exactness** (can daemon ASSERT visible⇔logged?) | Closure already shipped on both paths (`slashctx.go:186`); attests injected *layers*. OMP-internal assembly still escapes it. | Same closure + daemon builds full wire body (`client.go:148`) → can additionally journal exact request body. **Only path that can close it fully** on the thinking paths. | **Loses** it everywhere: OMP black-box assembly on *all* paths; review/panel/vision regress from receipt-complete to receipt-partial. | Achievable *if* OMP ships exact-prompt echo; compaction still invisible unless OMP journals it. **Not verified** OMP supports this. | Full receipts — daemon owns the entire loop. |
| **Context ownership** (compaction journaled, repair, steering depth) | Compaction decision owned by Odo (`omp.go:341` `compactionOverlayArgs` writes `thresholdTokens`), execution invisible inside OMP. Repair = daemon thinking task → direct per rule. Mid-run steering = `omp.go:359` `Send` → `steering.txt` (best-effort, OMP may ignore). | Distill/summaries migrate to direct → journaled in/out. Repair direct. Compaction *execution* still OMP-internal unless Odo owns it (B-lite: add `compaction/*` journal event types, audit §5 #9). | Worst: compaction invisible AND no direct path to repair from journal. | Compaction visibility needs an OMP compaction-event surface — **not verified** it exists. | Odo owns compaction fully → journaled. Highest context ownership. |
| **Consolidation cost/latency/flexibility** (spawn-agent vs one HTTP) | Consolidate (new) goes direct per ADR → one HTTP call, parallel legs already are (`server.go:2196` goroutines). Distill stays spawn-agent. | Distill migrates spawn→HTTP; consolidate direct. **Best cost/latency** on the thinking paths. | **Worst**: N parallel reviews = N subprocess spawns with full agent runtime; consolidate = agent spawn per synthesis. Latency explodes. | Same as A until the flag ships; then marginally better if OMP schema-output lands. | One HTTP call per consolidation; but the write-runner rebuild dwarfs the savings. |
| **Harness reimplementation scope** (edit/shell/verify/policy, streaming, effort flags) | Zero. OMP owns the agent loop. | Small on thinking paths (moa loop + fstools already exist `fstools.go:Execute`); zero on write path (stays OMP). | Zero new code; **retires** paid moa quirk-tail (`client.go:46` escalation, `:184` verbatim replay, `:313` stop whitelist). | Zero Odo code; cost is upstream negotiation. | **Massive**: edit/shell/verify/policy tools, streaming protocol, effort flags — codex/grok/dsh scope (GUI audit: codex `resume_picker.rs` = 7,010 lines). Violates "single researcher, lightweight." |
| **Streaming UX cost** of leaving OMP `--mode json` | Zero: write runs stay OMP (M7 bubble `omp.go:282`); thinking tasks are background (spinner/toast, not live stream). | Zero: same. Thinking tasks were never live-streamed. | Loses nothing on write path; but review/panel lose the *option* (today moa has no stream — they didn't have one anyway). | Zero. | Must reimplement streaming for write runs. |
| **Session durability/resume** (OMP sessions vs none) | OMP sessions for write runs (`omp.go:268` session dir, wrapper `--session-dir`). Thinking tasks are one-shot — no resume needed. | Same. Distill/consolidate are one-shot completions (`runOneShot:3502` blocks to terminal); a crash re-runs in one HTTP call. | Sessions for everything — but one-shot thinking tasks don't benefit, and you pay spawn cost for the privilege. | Unchanged. | Odo must build session store + resume (the 7k-line problem). |
| **Parse/verdict reliability for MoA** (free-form vs schema) | Free-form today (`reviewWithModel` parses verdict from text `server.go:2031`). Consolidate free-form. | Can constrain consolidate to a JSON schema (Anthropic tools/schema native; `client.go:114` `Tool`/`InputSchema` already in protocol). **Best** for Design-MoA DESIGN LOCK reliability. | Free-form everywhere; schema needs OMP flag (audit §5 #5: "depends on OMP exposing a flag"). | Best *if* flag ships; blocked otherwise. | Full control — schema trivial. |
| **Credential/security surface** (SUDO key in-process vs subprocess) | Unchanged: key already in-process for moa (`client.go:109` `os.Getenv`) AND injected to OMP subprocess (`omp.go:177` `enrichedEnv`). | Unchanged — no new surface. fstools deny-list already hardened (`fstools.go:61` `defaultFSDeny`, SEC audit batch). | Key still subprocess-injected for all paths; actually *widens* subprocess exposure (N review spawns). | Unchanged. | Key in-process only — slightly smaller surface, but the write-runner's own tool surface dwarfs it. |
| **Maintenance/upstream-drift** | OMP improves for free; moa quirk-tail already paid (`client.go:46,56,184,313`). Two surfaces to maintain. | Same two surfaces; but consolidates thinking-task logic onto the already-paid moa path. Quirk-tail is an asset, not debt. | **Retires** moa surface — one path. But loses the quirk investment and must re-tune OMP for thinking-model behavior OMP may not expose. | Odo code stable; **but timeline owned by an external project** — drift risk is *their* roadmap, not Odo's. | Odo owns all drift — maximum maintenance burden. |
| **Fit to roadmap + "single researcher, lightweight"** | Fits: ADR codifies the existing split. Design-MoA consolidate goes direct. A1 ratchet unchanged. | Fits best: Design-MoA (3 blind → consolidator → LOCK → implementer) gets schema-constrained consolidate + exact receipts; context-repair serves the A1 evidence loop. Lightweight: builds on existing moa/fstools. | Fights it: heavyweight spawns for thinking tasks; retires a working, invested path. | Defers the decision to an external timeline — anti-agile for a single-researcher OS. | **Violates** "lightweight" (README:6) and "agents (OMP) never write memory layers" boundary spirit (README:265). |
| **Failure-mode comparison** (black-box JSONL forensics vs journaled exact requests) | OMP write runs: black-box (`omp.go:432` rebuilds from session JSONL on degenerate stream). moa thinking runs: journaled response + layer receipts. | Thinking tasks gain journaled exact request+response → falsifiable ledger. Write runs unchanged (black-box). | **All** paths black-box. Worst forensics. | Better forensics *if* OMP echo ships; until then, A's state. | All journaled — best forensics, but at rebuild cost. |

---

## C. The boundary proposal

Exact action → mechanism mapping. "moa" = direct router client (`internal/moa/client.go`); "OMP" = adapter (`internal/adapter/omp.go`).

| Model-touching action | Today | Proposed | Mechanism & justification |
|---|---|---|---|
| **Write-capable agent run** (user `send_message` → code edits) | OMP (`server.go:handleSendMessage` → `adapter.Start`) | **OMP** (unchanged) | Agent loop (edit/shell/verify/retries) is the value; M7 streaming bubble is user-facing; session resume matters. Odo deliberately doesn't own this (`README:265`). |
| **MoA review fan-out** (diff → N models → verdict) | moa `Query` (`server.go:2031` `reviewWithModel`, `:1942` `handleReviewDiff`) | **moa** (unchanged) | Parallel HTTP, no spawn; receipt closure enforced (`slashReceipts` sibling on send path). |
| **Auto-land panel gate** (unanimous verdict before landing) | moa `Query` (`server.go:2048` `reviewWithModel` inside autoland) | **moa** (unchanged) | Same as review; `settle.go:125` `settlementClass` consumes the verdict. |
| **/panel read-only tool loop** (research/advisory) | moa `QueryWithTools` (`server.go:2200`) + `fstools.go:Execute` | **moa** (unchanged) | 16-round cap (`client.go:63`), scoped reads, every call journaled (`PanelResult.ToolCalls` `server.go:2240`). |
| **/vision** (image + text) | moa `QueryWithImages` (`server.go:2421`) | **moa** (unchanged) | Receipt closure enforced (`server.go:2417`). |
| **Distill** (epoch note from journal) | OMP `runOneShot` (`server.go:3491` `runDistillAgent`) | **moa** (migrate, gated — see D) | Single completion, daemon-assembled prompt, throwaway tmpdir (`runOneShot:3503`). Textbook moa case. **Gated on confirming the run is tool-free.** |
| **MoA consolidate** (Design-MoA synthesizer: 3 blind → 1) | *does not exist* | **moa** (new) | Pure thinking, no write surface; schema-constrained output for DESIGN LOCK reliability; one HTTP call. |
| **Context-management: distill/compaction summaries** | Distill = OMP (above); compaction = OMP-internal | Distill → moa (above); compaction *decision* stays Odo (`omp.go:341`), *execution* stays OMP | Odo already owns the threshold (`modelspec.go:80` `CompactThresholdTokens`); only the summary *generation* migrates if distill migrates. |
| **Context-management: mid-run context repair** | *does not exist* | **moa** (new) | Daemon rebuilds/repairs a context block from journal → one completion. Thinking task, no writes. |
| **Auto-revise repair run** (settlement ladder) | OMP (`settle.go` synthesizes prompt → fresh `adapter.Start` run) | **OMP** (unchanged) | The repair run *writes code* (new worktree, same conversation) — it's a write-capable agent run, not a thinking task. `settle.go:180` `settleRepairPrompt` builds the brief; the run edits files. |

**Boundary rule (for the ADR):** *If the action writes to the repo or needs the agent loop, it goes through OMP. If it produces a verdict, summary, or repair-text from data the daemon already holds, it goes through moa.* The receipt closure (`slashctx.go:186` / `assembleRunPrompt`) is the enforcement seam on both sides; the moa side is the only one where it can be made *complete*.

---

## D. Phased plan

B is recommended, so the waves (cost S/M/L) and their journal/ADR consequences:

**Wave 0 — Formalize (S, no code).** Write `docs/adr/NNNN-model-routing-boundary.md` codifying the C boundary. Journal consequence: none (documentation only). This is A's entire content; B inherits it.

**Wave 1 — Close the moa receipt gap (S).** Today the closure attests injected *layers* (`slashctx.go:189-208`) and the *response* is journaled (`PanelResult` `server.go:2236`), but the exact assembled *request body* (system string + tools + messages + max_tokens) is not stored verbatim — it's reconstructable from receipts + daemon-constant assembly. To make visible⇔logged a *daemon-enforced invariant on the thinking paths*, journal the request body (or its sha16 + the deterministic inputs) alongside the response. Journal consequence: extend the `agent_text` panel payload (`server.go:2215`) with a `request_sha16` + `request_bytes` field; non-breaking append. **Risk:** collides with the in-flight fix-INT journal-semantics wave — if so, defer to after W1/W2 land (this is the secondary B→A flip trigger).

**Wave 2 — MoA consolidate direct (S–M, net-new).** Add a `moa.Query` (or `QueryWithTools` if it needs to read the 3 proposals from the journal) call for the Design-MoA synthesizer. Constrain output to a JSON schema (native to the Anthropic tools protocol, `client.go:114`). Journal the consolidated verdict as a `review_action` (DESIGN LOCK) with exact request+response receipts. Journal consequence: new `review_action` payload variant `design_lock` — consistent with `settle.go`'s "no new event types" discipline if it reuses `review_action`; else one new type. This is the roadmap's Design-MoA item (`README:55`).

**Wave 3 — Mid-run context repair direct (S–M, net-new).** A daemon action: when a run's injected context is detected stale/oversized, rebuild the context block from the journal and emit a repair summary via `moa.Query`. Journal consequence: a `context_repair` event (or reuse `memory_update` with a `cause:context_repair`). Serves the A1 evidence loop (`odo autonomy audit`).

**Wave 4 — Distill migration (M, gated).** Migrate `runDistillAgent` (`server.go:3491`) from `runOneShot`/OMP to `moa.Query`. **Gate:** (a) fix-INT W1/W2 landed (uncommitted tree stable), (b) confirm the distill orchestrator run is tool-free — i.e. the prompt is self-contained from journal data and the OMP run doesn't use read/glob to explore. If it *does* use tools, fall back to `moa.QueryWithTools` + a journal-reader executor (M cost) or abandon the migration (→ A). Journal consequence: distill output already journals as epoch notes; the *input* receipt becomes exact (moa closure) instead of OMP-black-box.

**Not undertaken (explicitly):** OMP-internal compaction visibility (needs Option D or E — not worth it for a single-researcher OS), full direct write-runner (E), retiring moa (C).

---

## E. Risks & failure modes (per option, top-2, honest)

**A. Formalize status quo**
1. *Distill stays black-box.* The epoch-note generation's exact prompt-to-model bytes remain unattested (OMP assembles beyond Odo's prompt file). `odo journal range` can show what layers were *injected* but not what OMP *sent*. Mitigated only if distill is later migrated (→ B).
2. *Compaction invisibility persists.* Odo owns the *threshold* (`omp.go:341`) but OMP owns the *act*; a compacted run's context is unrecoverable from the journal. The audit's own §5 #9 concedes this is "OMP-side backlog." Tonight's brief-asserted failure modes (print-mode recap-only exit, exact-UUID resume instant exit, 900s timeout mid-gathering) are all OMP-black-box forensics — A inherits all of them on the write+distill paths.

**B. Expand direct (recommended)**
1. *Distill migration regression.* Distill is a working M4/M10 feature. If the OMP run uses tools the daemon can't replicate in one completion, the migration silently degrades epoch-note quality (the model answers from less context). **Mitigation:** the Wave 4 gate (confirm tool-free) + keep `runOneShot` as a fallback adapter behind `runDistillAgent` until the migrated path is telemetry-confirmed.
2. *Thinking-block quirk tail grows.* moa already handles kimi signature-400 (`client.go:184` rawContent verbatim replay), stop-reason whitelist (`:313`), output-budget escalation (`:46,368`). A *new* model on the consolidate/repair path may introduce a new quirk OMP would have absorbed for free. **Mitigation:** `modelspec.Lookup` (`modelspec.go`) + the escalation ledger make quirks falsifiable; the cost is bounded and already-proven manageable.

**C. OMP-core everything**
1. *MoA latency explosion.* N parallel reviews become N full agent-runtime spawns (wrapper + omp + session dir each) instead of N parallel HTTP calls (`server.go:2196` goroutines). A 3-model panel goes from ~seconds to ~minutes per review. This directly fights the "review scales with blast radius" principle (`README:130`).
2. *Receipts lost on all paths.* The one path that *can* enforce visible⇔logged gets retired; every thinking task regresses to OMP black-box forensics. Retires the paid quirk-tail investment.

**D. Invest in OMP flags upstream**
1. *External timeline dependency.* Odo cannot verify OMP exposes exact-prompt echo, structured-output schema, or compaction-event surfaces (audit §5 #5: "depends on OMP exposing a flag"). The decision defers to a project Odo doesn't control — anti-agile for a single-researcher OS.
2. *Compaction may stay invisible even with flags.* An exact-prompt echo helps receipts but doesn't journal *when* OMP compacted or *what* was dropped — that needs a compaction-event surface, a separate upstream ask.

**E. Full direct write-runner**
1. *Rebuild cost.* Edit/shell/verify/policy tools + streaming protocol + session store/resume is codex/grok/dsh scope (GUI audit: codex `resume_picker.rs` alone is 7,010 lines). Months of work regressing every working feature. Violates "single researcher, lightweight" (`README:6`).
2. *Tool-loop reliability greenfield.* OMP's hardened edit/apply/verify loop is battle-tested; a daemon-owned equivalent starts at zero. The tonight failure modes (recap-only, resume-instant-exit) would be *Odo's* to fix instead of OMP's.

---

## F. What would change my mind

Falsifiable conditions that would invalidate the B recommendation:

1. **Distill is not tool-free (kills the headline migration).** If reading `runDistillAgent`'s prompt assembly shows the distill prompt instructs the orchestrator to explore files (glob/read via OMP tools) rather than answering from the daemon-assembled journal context, a single `moa.Query` cannot replicate it. *Test:* grep the distill-prompt builder for "read"/"glob"/"explore" directives, or run a distill and inspect the OMP session JSONL for `tool_execution_*` events. If tools fire → B collapses to A (formalize + route new actions direct, leave distill on OMP).

2. **The exact-request journaling collides with fix-INT.** If extending the panel `agent_text` payload (`server.go:2215`) with request receipts breaks the journal-semantics tests that the in-flight W1/W2 wave is stabilizing (the `server.go:648` "model-visible ⇔ logged closure" lives in the same code area), the gap-fill is itself blocked. *Test:* attempt the Wave 1 append against the current test suite; if `settle_test.go` or the receipt-regression tests fail, defer Wave 1 post-fix-INT and treat B as A until then.

3. **OMP ships exact-prompt echo + compaction events (makes D viable).** If `omp --help` or a verified OMP changelog exposes `--echo-prompt` / `--output-schema` / a compaction-event stream, the receipts-exactness asymmetry collapses and D (stay OMP-topology, get receipts upstream) becomes strictly lower-effort than B. *Test:* `omp --help | grep -i 'echo\|schema\|compact'`. None found today (not verified to exist).

4. **Consolidate needs write tools (rare but possible).** If the Design-MoA synthesizer must *apply* the 3 proposals to a worktree to judge them (rather than reasoning over their text), it's a write run → OMP, not moa. *Test:* the Design-MoA milestone spec (`README:55`: "3 blind design proposals → consolidator → DESIGN LOCK → single implementer") — the consolidator's job is synthesis+lock, the *implementer* is the separate write run. So this is unlikely by design; would change only if the spec evolves.

None of conditions 1–4 are currently met on the evidence I read; B stands. The strongest single fact: `runDistillAgent` (`server.go:3491`→`:3502`) spawns a full agent process for a single completion — the exact cost `moa/client.go:1-4` was written to eliminate — and the receipt closure that makes the moa path the *only* attestable one is already shipped (`slashctx.go:186`).
