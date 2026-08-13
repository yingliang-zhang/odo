# Harness Tri-Model Audit (2026-08-13): deepseek-harness vs grok-build vs openai/codex

> Comparative audit of three vendor coding-agent harnesses, and what is worth
> borrowing into Odo. Consolidated from a blind-sealed tri-model MoA run.
>
> Raw leg outputs and the frozen brief: `./harness-tri-model-audit-2026-08-13/`
> (`brief.md`, `leg-k3.md`, `leg-glm.md`, `leg-dsf.md`).

## 0. Provenance & method

| Leg | Model | Output | Notes |
|---|---|---|---|
| 1 | kimi-k3 (--thinking max, 900s) | 31.8K, highest precision | Independent GitHub-API governance re-checks; one factual error (claimed dsh history "squashed") — corrected by orchestrator bare-clone verification |
| 2 | glm-5.2 (--thinking max, 900s) | 36.8K, best evidence tables | Unique finds: grok XOR-obfuscated system prompt, codex docs are all redirect stubs, doom-loop detection |
| 3 | deepseek-v4-flash (--thinking max, 900s) | 34.2K (recovered from session JSONL after print-mode recap-only exit) | Full-cloned dsh: 12,293 commits since 2026-06-10 (verified) |

Conventions: **3/3** = all legs agreed; **2/3** = converged with one recorded
dissent; **1/3+** = unique addition adopted as gap-fill. Orchestrator
additionally verified the dsh commit count with
`git clone --bare --filter=blob:none` (12,293 commits; first commit
2026-06-10; HEAD "Merge pull request #2519" while GitHub reports 0 PR
objects — internal numbering preserved, PR objects not published).

**3/3 shared premise (verbatim-verified in each repo's CONTRIBUTING): none of
the three accepts external pull requests.** "Open reading ≠ open writing" is
the new normal for this category.

## 1. What the three are

- **deepseek-harness** (TypeScript, MIT): Cordis microkernel "everything is a
  plugin" harness; Web UI on :3080 (no TUI) + headless profile; event-sourced
  append-only session log with a runtime "model-visible ⟺ logged" invariant.
  Published 2026-08-13 with full internal history (12k commits since
  2026-06-10) — a product unveil, not a dump.
- **grok-build** (Rust, Apache-2.0): single-binary fullscreen ratatui TUI +
  leader/follower session daemon (`~/.grok/leader.sock`); pure sync mirror of a
  private monorepo (28 commits, all `grokkybara[bot]` "Synced from monorepo").
- **openai/codex** (Rust, Apache-2.0): mature multi-process setup — ratatui
  TUI + app-server JSON-RPC daemon + VS Code/desktop; Guardian LLM approval
  reviewer; 16-month public history, 105k stars.

## 2. Technical differences

| Axis | deepseek-harness | grok-build | openai/codex |
|---|---|---|---|
| Architecture | Single Node proc; Cordis plugin tree; Web UI :3080, no TUI | One ratatui binary; leader.sock daemon serves TUI/IDE/headless | Multi-process: TUI + app-server daemon + IDE/desktop |
| Agent loop | Event-sourced turn loop, "model-visible ⟺ logged" asserted | ACP run_loop + doom-loop detection + prompt queue | Submission/Event protocol + turn.rs, interleaved compaction |
| Approval | Coarse presets (ask/never), fail-closed | Static rule grammar `Bash(glob)` deny>ask>allow, 6 modes | execpolicy (Starlark) + **Guardian LLM auto-review** |
| Memory | **No cross-session memory subsystem** | Experimental hybrid FTS5+vec0, MMR, off by default | AGENTS.md hierarchy + new two-phase memories crate |
| Subagents | Richest: provider-pluggable, can drive codex/claude-code as children | spawn_subagent + persona I/O contracts + worktree isolation | Mailbox protocol multi-agent threads |
| Sandbox | Seatbelt/Landlock/bwrap/Win ACL, fail-closed probe order | Whole-process irreversible profiles (vendored nono) | Same triad + network MITM proxy + escalation-on-denial retry |
| Edit grammar | 2 literal-replace grammars | 3 grammars (incl. vendored codex apply_patch + own hashline) | apply_patch only |
| Wire protocols | 3 (chat/responses/messages), true BYOK | 3, true BYOK | **Responses-API only** (chat wire API hard-removed) |

## 3. Consequential divergences (3/3)

1. **Where state lives**: dsh treats the append-only log as sole truth with
   derived views; grok/codex run resident session daemons serving multiple
   frontends. Odo is philosophically with dsh — and already has both (daemon
   AND journal).
2. **Who may say yes to a tool call**: static rules (grok) vs an LLM judge
   (codex Guardian — the only one automating the approval decision itself) vs
   coarse presets (dsh). This is the human↔autonomy spectrum bet — validation
   that Odo's settlement-ladder bet is the same contested territory.
3. **Compat-clone vs own-standard**: grok/dsh clone the Claude/GPT surface
   (SKILL.md, AGENTS.md, MCP, hooks are now industry baseline across all
   three); codex deleted the chat wire API to push the Responses API as the
   interop standard. Differentiation has moved up to approval automation and
   session durability — exactly Odo's bet.

## 4. Openness (dimensional; 2/3 with recorded dissent)

K3+DSF judged grok=Funnel / codex=Open-ish; GLM judged grok=Open-ish /
codex=Funnel. All three legs agree on the underlying facts; they weighted
axes differently, so the consolidated verdict is dimensional.

| Repo | Contribution | Protocol neutrality | Governance transparency | Verdict |
|---|---|---|---|---|
| deepseek-harness | Closed "at the moment"; issues off, Discussions on, plugin ecosystem encouraged | True BYOK (3 wire protocols; can even wrap rivals as subagents) | Most real: full 12k-commit history, named human authors (+ oss regulars) | **Open-ish — most open of the three** |
| grok-build | Fully closed (no CLA "because external contributions are not accepted"); issues/discussions/releases all zero | True BYOK (3 wire protocols) — *GLM dissent: on this axis grok beats codex* | Pure mirror: bot-only commits, history invisible, binaries funnel to x.ai | **Funnel — source-transparency theater** |
| openai/codex | "By invitation only" + CLA; occasional external merges; staff PRs ceremonially bot-merged with zero reviews | **Protocol funnel**: chat wire API hard-removed (`CHAT_WIRE_API_REMOVED_ERROR`); ChatGPT default auth — *GLM dissent: this alone earns Funnel* | 16-month real history + 12,477 open issues, but docs are redirect stubs; Bazel adds friction | **Open-ish community / Funnel-ish protocol** |

## 5. Features worth borrowing into Odo (ranked)

| # | Feature (source, who does it best) | Convergence | What to take / what NOT to take | Odo mapping | Cost | Priority |
|---|---|---|---|---|---|---|
| 1 | **Guardian risk taxonomy + structured review receipts** (codex) | 3/3 (all reject the single-judge part) | Take the risk classes (data-exfil / credential-probe / security-weakening / destructive) + persist risk-class & outcome on every `review_action` decision; do NOT take the single-model judge | autoland gate classes already exist → tag + aggregate in `odo autonomy audit`; exactly the per-class evidence the A1 ratchet waits for | S | **P0** |
| 2 | **"Model-visible ⟺ logged" runtime assertion** (dsh) | GLM proposed P0; K3/DSF surface the invariant | Odo already receipts injected layers (sha16, bytes, dropped_seqs); add a daemon-side assertion before send — turns the principle into an enforced invariant | `internal/store` + prompt assembly point | S | **P0** |
| 3 | **Durable async steering / inbox** (dsh `Agent.steer/inject/whenIdle`) | DSF proposed P0 (1/3+ gap-fill, adopted) | Directly matches Odo's "parked async review" vision and the goal-queue / park-and-switch design item | daemon run loop + journal `steer`/`parked` events | S–M | **P0** |
| 4 | **Declarative rule file** (grok grammar most ergonomic; codex Starlark most expressive) | 3/3 | deny>ask>allow precedence + per-decision justification; risk: a writable policy file is a ratchet-bypass surface → ship file-can-only-tighten at first | `autoland.go` mechanical gates → `.odo/rules` | M | P1 |
| 5 | **Structured verdict output** (codex `--output-schema`) | K3 proposed | Constrain MoA legs' final answers to JSON schema; honest cost: depends on OMP exposing a flag, else schema-in-prompt + strict validator | adapter leg-parsing path | S–M | P1 |
| 6 | **Turn-fork / durable rewind** (codex id-preserving immutable revert; grok worktree-fork UX) | 3/3 coverage | Fork copies, never edits — append-only invariant untouched; real work is memory-layer scoping (epoch notes must not cross branches) | `internal/store` + M12 replay machinery | M | P1 |
| 7 | **Subagent sessions with report + `resume_from` + worktree isolation** (grok best) | 3/3 coverage | Directly serves the planned Design-MoA; persona I/O contracts (GLM) fit the proposal→implementer handoff | reuse MoA fan-out + worktree plumbing | M | P1 |
| 8 | **Cross-session hybrid retrieval** (grok FTS5+vec0, 0.7/0.3, MMR) | GLM+DSF; K3 excluded as "Odo is strictly ahead" | Not a contradiction: a vector index over the journal is derived, not authoritative; use a local embedding to stay provider-neutral | recall layer | M | P1–P2 |
| 9 | Small items: orphan-turn synthetic close (S); permission-preset bundles (S); capability catalog, adopt only "reject known-bad, pass unknown" (P2); compaction + tool-result pruning | per-leg | Compaction belongs to the **OMP-side backlog** (Odo does not own the agent loop); Odo only adds `compaction/*` journal event types | — | S | P2 |

## 6. Explicitly NOT borrowing (with reasons)

| Item | Reason |
|---|---|
| Guardian as a whole (single-model auto-approval judge) | 3/3: violates Odo's independent-panel principle; only taxonomy + receipts survive |
| Whole-process sandboxing | Odo deliberately ships no sandbox; worktree isolation + unanimous gates already bound the blast radius; revisit only after a real incident (K3, P2) |
| grok's hashline edit grammar | The edit layer belongs to the agent CLIs Odo drives, not to Odo (K3) |
| Plugin marketplaces / trust gates | Solves multi-user distribution Odo doesn't have (K3) |
| Capability-seam three-role formalism | L-cost, over-abstraction for a single-user app (GLM) |
| Two-pass compaction and other agent-loop internals | Belong to OMP, not Odo (K3) |

## 7. What Odo already does better (3/3 convergent — independent validation of the moat claims)

1. **Unanimous multi-model verdicts on the merge path** — codex's Guardian is
   a single trusted judge; the other two have no cross-model review at all.
2. **Decision-outcome audits as first-class CLIs** (`odo autonomy audit`,
   `odo skills audit`) — none of the three ships any self-audit of its own
   automation.
3. **Six-layer memory lifecycle with per-layer sha16 receipts** — dsh has no
   memory subsystem; grok's is experimental and off; codex's is new.
4. **Settlement ladder** (worktree diff lanes → mechanical gates → panel →
   auto-land → verify re-run) — all three let agents edit files and stop.
5. **A deterministic, LLM-free metrics path** (DSF unique): grok/dsh route
   consolidation/summaries through models; Odo's ledger does not.

## 8. Surprises / red flags (verified)

1. dsh published with **full 12,293-commit history** (from 2026-06-10) on day
   one, yet 0 GitHub PR objects — merge messages keep internal numbering.
   (K3's "squashed history" guess was wrong; orchestrator-verified.)
2. **Zero of three accept external PRs** — verbatim in all CONTRIBUTING files.
3. grok: bot-only mirror, no issue tracker, no releases, **vendored ports of
   openai/codex + sst/opencode tool code** (THIRD-PARTY notices), and an
   **XOR-obfuscated system prompt at rest** (GLM unique find).
4. codex hard-removed the chat wire API — BYOK must speak the Responses API.
5. codex in-repo docs are 1–3-line redirect stubs; real docs live on
   developers.openai.com (GLM unique find; contribution friction by design).
6. Telemetry: dsh `harness-telemetry.deepseeksvc.com` (default-off, plus an
   anonymous user id file regardless); grok ships mixpanel+OTLP+GCS upload
   crates compiled in; codex persists risk scores and defaults to ChatGPT auth.

## 9. Mapping to Odo's outstanding ledger (as of `962a116`, fix-SEC batch)

| Existing ledger item | Audit interaction |
|---|---|
| fix-INT Wave 1 (accept TOCTOU / base_stale recheck / bridge REVIEW 330→900s) | Unchanged first wave — same accept/autoland code area as audit #1; fix first, design receipts after |
| fix-INT Wave 2 (fold allowlist / cap-drop journaling / #14 memory-pins materialization) | **Add audit #2** (model-visible ⟺ logged assertion) — same journal-semantics wave, S cost |
| `moa_fs_deny` replace→merge contract change (tri-model mini round) | Independently queued; unrelated to the audit |
| **NEW wave: audit #1** (Guardian risk taxonomy + review receipts) | Own tri-model design round — extends M16/M18 gates; S1 trigger (journal event schema = persisted format) |
| goal-queue semantics → park-and-switch (large design, own session) | **Inject audit #3** (dsh `Agent.steer/inject/whenIdle` durable-inbox pattern) into that brief as the primary reference |
| Backlog: audit #4 rules file / #5 structured verdict (needs OMP flag check first) / #6 turn-fork / #7 subagent sessions / #8 hybrid retrieval | Queue after the above; #5 blocked on an OMP capability check |
