Working...
## A. Recommendation

**Primary: B — expand direct-router deliberately, scoped to single-completion thinking jobs; OMP stays the core runtime for write/agent-loop work.** The direct `moa` client is the only surface where Odo can make "model-visible ⟺ logged" byte-exact and fail-closed; the jobs the question names (Design-MoA consolidation, distill/context summaries, context repair steering) are exactly the jobs where exact input attribution is most load-bearing, and they are all single-completion or read-only shapes. OMP is irreplaceable for what it actually manages (edit/shell loops, tiers, sessions, streaming) — that's a reimplementation wall, not a fee. So: B for consolidate + distill/context-thinking + productized read-only tool loop; the write ladder stays OMP.

**Runner-up: D** — if Design-MoA isn't imminent, the cheapest honest bet is: keep today's topology, and push OMP for the flags the prior audit already tagged as gaps (`docs/compare/harness-tri-model-audit-2026-08-13.md:91` #5 structured verdict "depends on OMP exposing a flag"; `:95` #9 compaction/steering "OMP-side backlog"). D flips to primary if OMP ships exact-prompt echo + structured output: then the daemon can assert through the adapter and C (all-through-OMP) becomes viable with no new direct surface.

**The B↔A↔D decision hinge (evidence):**
- `internal/ipc/server.go:1039` `assertPromptReceipts` + `server.go:1127` `assembleRunPrompt` already enforce a fail-closed model-visible⟺logged closure on the **OMP** run path (M18 W2 item 4; refusal before adapter start, `server.go:1356-1359` `receipt_assert_failed`). This is the prior audit's P0 #2 (`harness-…:88`) landed for runs.
- But that closure deliberately stops at Odo's own injected layers: the run prompt bytes are receipted, yet OMP's internal assembly (compaction, convo re-wrap, tool echo) is **not** — `server.go:1026-1038` exemption ledger lists OMP-internal tool results and leg-assembly as outside the gate. Tonight's observed failure modes (recap-only exits, empty exact-UUID resume, 900s mid-gather) are unreported from the journal; they had to be forensically recovered from session JSONL per the wrapper's `[OMP_TIMEOUT]` diagnostics (`~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh:1070-1101`).
- The jobs you propose to add — MoA consolidate, distill/context summaries, repair-boundary steering — are **single completions with inputs the daemon already holds in full** (three proposal texts, a folded journal window, a diff+comments for repair). For those, direct = one HTTP call with byte-complete request construction; OMP = a full subprocess agent run whose internal assembly you cannot receipt. Direct is both cheaper and strictly more receiptable.
- The write/steer runs remain OMP because the daemon owns neither the edit grammar, the tool executor scope, nor the session state — driving them type-wise via `adapter.go:Adapter` (Start/Send/Events/Cancel/Close) is the existing contract, and reimplementing the agent loop in-daemon is the L-cost.

**Flip to C (retire moa direct) trigger:** OMP exposes (i) an exact-prompt echo / verifiable receipt flag, (ii) structured output schema, (iii) `--thinking`-level steering on a single shot (`omp --help` allowed but unrun — not verified which exist today). If all three land, the direct client's quirk-payment (escalation, 900s floor, thinking replay) becomes redundant and C wins; today they don't exist, so the moa path is the only place receipts are exact.

## B. Option-by-axis matrix

Axes rows; A–E columns; terse cells. C/E are worst on everything evidence-related; A is today+ADR; B is the only one that adds exactness where it's most valuable.

| Job axis | A (formalize status quo) | B (expand direct deliberately) | C (ChiroT OMP all) | D (invest in OMP flags) | E (direct write-runner) |
|---|---|---|---|---|---|
| Receipts exactness (daemon ASSERT visible⟺logged) | Run path: yes (`server.go:1127`); direct moa: attest-but-not-byte-exact | Brings byte-exact to new fibers; extend `assertPromptReceipts` to the classifier/consolidator prompts | Only where OMP chooses to report; Odo can't assert its internals | Best if upstream ships echo flag; write reachable today | Only if you re-size the whole loop in-daemon; otherwise none of it changes |
| Context ownership (compaction jour, repair, mid-run steering) | Compaction invisible (`audit:95` #9 → OMP backlog); repair = full re-run (`settle.go:305`) | Consolidator/lm uses daemon-held inputs; steering of the live run stays adapter `Send`; distill gets own journal event | Everything under OMP's black box; compaction stays invisible | Same as A until a flag exists | Full (you own the loop; rebuild anyway) |
| Consolidation cost/latency (spawn-agent vs one HTTP) | Spawn per step (adapter.Start → process → events poll) | 1 HTTP POST for consolidate/summunizations; changes pass from process to —status | Spawn per step; plus wrapper watchdog/slot overhead | Spawn per step (no change) | 1 HTTP + your own executor |
| Harness reimplementation / edit+shell scope | none (OMP owns) | none for read kinds; E1 loop already in daemon (`fstmos.go:250`) | none (OMP) | none | L (edit grammar, shell executor, verify/policy, protocol, effort) |
| Streaming UX cost of leaving `--mode json` | none for OMP runs; direct already has no streaming | none added: consolidate/distill are after-run artifacts, not live bubbles | none (keeps `--mode json`); direct paths no worse | whatever OMP gives; |
| Session durability / resume | OMP sessions + resume (`omp.go` `--resume` exact-UUID); | zero for one-shots (no state); keep OMP for steerable | OMP (its sessions) | OMP | you'd build a daemon session store |
| Parse/verdict reliability for MoA | verdict parsing already daemon-side (`review.go:2446` `parseVerdict`) | same parser reused for the consolidator's DESIGN LOCK output | depends on OMP exposing structured output (not verified tonight) | structured ours if flag exists | same sleep only |
| Credential surface | SUDO key in rep + injected to subprocess env (`omp.go:enrichedEnv`) | key stays in daemon memory; no new subprocess per call | key copied into every OMP subprocess env | unchanged | key in daemon (one surface, bigger loop |
| Maintenance / upstream drift | moa quirk-tail paid and pinned (escalation, 900s, thinking replay) | expands the paid quirk surface by N simple wrappers | quirk-tail retired, but a new OMP dependency (single-source for everything) | depends on upstream cadence/plans | whole agent-loop surface becomes Odo's to maintain |
| Fit to roadmap (Design-MoA, A1 ratchet, GUI wave A) | Design-MoA would spawn 3+1 OMP runs for full budget; GUI wave A (#1 daemon registry) works either way | Consolidator + blind-proposal legs = most natural; `consensusVerdict` already says "no 4th model call" (`server.go:2010`) — the new call becomes that 4th, explicitly | 3+1 OMP spawns; heavier; A1 audit joins unchanged | delays it until flags | over-build for the roadmap |
| Failure-mode forensics | black-box JSONL for OMP internals (wrapper `omp_timeout` diagnostics); journaled preview | journaled exact requests; same JSONL forensics for whatever OMP does | OMP-only forensics | like A | OO |
| "Single researcher, lightweight" | a full process per one-shot completion (spawn + wrapper + watchdog + slot) | one HTTP call for one-shot thinking; process only where tools/edits needed | a full process per job | unchanged | the app owns the whole loop — heavy |

## C. The boundary proposal

**Design rule:** *A model call that cannot itself change files, and whose inputs the daemon holds byte-complete, goes through the direct `moa` client, journaled with the byte-exact request (daemon-assembled, `assertPromptReceipts` extended). A model call that needs tools/shell/edit/write/steer/session goes through `adapter.go` (OMP), where receipts cover the daemon-assembled prompt's injected layers (already asserted) but the OMP-internal assembly is accepted as a documented gray zone.*

Concrete action → mechanism (change column marks what moves):

| Model-touching action | Today | Mechanism after B |
|---|---|---|
| `odo` run start (worktree edit run) | `internal/ipc/server.go:683` `ad.Start` | OMP — unchanged |
| continuation `a2` | `server.go:1388` `ad.Start` | OMP — unchanged |
| auto-revise ladder (repair round) | `settle.go:605` `ad.Start` | OMP — unchanged (write-capable; fresh worktree; receipts asserted pre-start) |
| run-level context repair **steering** (mid-run) | not present as a verb | **OMP `adapter.Send`** — add (steer of the live session is OMP-owned) |
| distill (epoch → wiki note) | `server.go:3502` `runOneShot` via `s.distillAdapter`/OMP keys | **→ direct `moa.Query`** (single completion; journaled `distill` event + note_sha; prompt bytes = folded-window deterministic) — S |
| curator (topic pages) | `curator.go:573` `runOneShot` | **→ direct (same reasoning)** |
| learner/user.md promotion | `learner.go:545` `runOneShot` | **→ direct** — note: touch base with the ADR-0003 inv7 "distill is the only LLM write cadence" wording; these are the daemon's own cadences, not agents, so the invariant survives |
| review_diff fan-out | `server.go:1942/2031` `moa.Query` | direct** — unchanged (already direct) |
| auto-land review gate | `server.go:2031` `reviewWithModel` | direct** — unchanged |
| /panel (E1 tool loop) | `server.go:2162` `moa.QueryWithTools` + `fstools.go:250` executor | direct** — keep; productize the executor interface out of boolean |
| /vision | `server.go:2421` `moa.QueryWithImages` | direct** — unchanged |
| **Design-MoA consolidator** (roadmap `README.md:55`) | none | **off — direct `moa.Query`, daemon builds exact synthesis prompt from the 3 proposal texts, journals it byte-exact, parse schema `DESIGN` verdict** (this becomes the explicit "4th model call" that `server.go:2010` says is still absent) |
| "3 blind proposals" legs (Design-MoA) | none | **read-only tool-loop direct (`QueryWithTools`)** — they need repo reads, no writes |
| OMP-session compaction summary | none | **neither — belongs to the OMP backlog** (`harness-tri-model-audit:95` #9); Odo journals `compaction/*` only when OMP exposes it |

## D. Phased plan

**Wave 0 — boundary ADR (S):** write the rule above (mechands + the exemption ledger's wording) into `docs/adr/0005-model-boundary.md`. Journal consequence: prose only, zero schema change. Cost S. (Satisfies the top half of A on its own; no topology change.)

**Wave 1 — consolidator + exact journal extension (M):** implement Design-MoA's single consolidation handler on `internal/moa` (byte-concat 3 proposals → one `Query`), plus the `_consolidate` journal event carrying `input_shas` per leg + `prompt_sha16` + `total_prompt_bytes`; extend `assertPromptReceipts` (`server.go:1039`) with a content-hash layer size. Journal: new deterministic event, all receipts asserted before send. ADR: amendment listing consolidation as the "4th, consolidator call → direct".

**Wave 2 — distill family to direct (M):** move `runDistillAgent` (`server.go`), curator (`curator.go`), learner to direct `moa.Query`; journal the exact prompt (deterministic from the folded window) + `note_sha`/`proposal` hashes as today. Keep `distillAdapter`/OMP fallback if moa key unset (belt). Journal: distill prompts get `prompt_sha16` + full injected-receipt closure like runs. ADR: amend the "OMP only" memory-writer wording to "the daemon's writer cadence happens via the direct client; OMP agents still never write memory".

**Wave 3 — productize E1 + steer (S initial):** move the FS executor (`fstools.go:250`) to a small package so both the panel and the new design legs reuse one executor; add `adapter.Send` mid-run re-steer path if a pain point shows (/panel already covers manual second opinions per `README.md:57` cross-examiner deferral).

**Verification each wave:** run the seahorse (journal events `moa_review`, `distill`, `consolidate` with `prompt_sha16`; truncation policy still `reviewVerdict`/`parseVerdict` fail-closed; consensus `server.go:2011` unchanged). No tests in the spec unless a wave changes a tested contract, matching repo conventions.

## E. Risks & failure modes (top-2 per option)

**A:** (1) Topology stays mismatch: distill→OMP spawn stay and heavy for one-shot; (2) `ADo` keeps the "no 4th model call" rule, so Design-MoA's consolidator would debut as an unconstrained ad-hoc (worst: an undebated OMP run per proposal).
**B:** (1) Direct-moa new paths extend the quirk-tail (escalation/900s replay already paid — new regression surface); only surfaces with byte-exact assertions and the model's own failure path (everything else is an HTTP timeout — already handled). (2) Two mechanisms = two code paths; drift risk (e.g. prompt-assembly divergence between `assembleRunPrompt` and new consolidate assembly) — mitigated by both funneling through the extended `assertPromptReceipts`.
**C:** (1) Single-supply OMP: every thinking task pays process+wrapper+streams, and receipts get strictly weaker (OMP-internal opaque, `omp` diag forensics were the recovery path tonight). (2) You can't assert anything about what OMP sent `consolidate`/distill; would violate README §strength "the audit trail for what did run N see is complete" for the single most load-bearing steps.
**D:** (1) Upstream OMP may never ship the flags (verified: not present tonight; structured-output is "P1, depends on OMP exposing a flag" — `harness-audit:91`); you gate a whole roadmap item on a third party's backlog. (2) Meanwhile the current pain (distill resize by spawn; consolidate absent) remains open — D alone doesn't land anything.
**E:** (1) L-rebuild: edit grammar, shell executor, verify policy, streaming protocol, session store, effort flags — everything OMP already manages (`adapter` + wrapper machinery seen above); violates the "no sandbox/containment, thin" principle that README holds. (2) On-path security surface multiplies (executors, grants) with zero evidence the direct write loop is needed — the current worktree+accept ladder already gates writes.

## F. What would change my mind

1. **Flip B→Aggregate if cost evidence contradicts the "one-shot is cheap" claim.** If a real `distill`/consolidate milestone shows the direct path's per-call latency (gateway 900s + escalations, `moa/client.go:baseRequestTimeout`) delaying the memory cadence more than a parallel OMP spawn, then Design-MoA consolidator still benefits the same way (single HTTP vs process) — but distill might stay. That falsifiable fact: distill OMP vs direct wall-clock on the same epoch.
2. **Flip B→D/C if OMP ships (verifiable):** an exact-prompt echo flag + structured-output schema. Then re-evaluate whaof the new direct surfaces against the cost of re-implementing; if the echo is complete enough for byte receipting, C becomes viable and the moa quirk-tail goes away. Fail test: run one OMP rebuild task with `--output-schema`-style constraint and see receipts capture the model-level prompt exactly.
3. **B→A if the user picks A as the transient bridge before Design-MoA** (cost containment): then the boundary ADR still goes in (it's correct regardless), but the consolidation/denote-direct moves wait until Design-MoA enters scope. I cannot falsify the "not now, but locked" framing — ownership changes with sequencing, which the user owns.
4. **Require direct for the consolidator** if the user's Design-MoA acceptance criteria include "blind legs can't conspire" — byte-preserving the three blind proposals into the consolidator prompt (and journaling the 1st addressable receipt) is the only mechanism that makes that provable. If the criteria doesn't need attribution, the choice is purely economic, and D/A could win.
